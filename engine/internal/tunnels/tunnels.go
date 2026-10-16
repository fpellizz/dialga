// Package tunnels keeps one shared SSM tunnel per tunnel-mode target while
// the engine runs ("dialga serve"): every SQL session of the target goes
// through it, instead of opening its own, which takes seconds.
//
// A tunnel opens on first use or on request (Open) and reconnects by itself
// (package tunnel). One opened by use closes after IdleTimeout without
// users; one opened on request stays until CloseTarget. Close ends them all,
// with their SSM sessions, when the engine stops. Every change of state goes
// to the subscribers, for the tunnel indicator of the GUI.
package tunnels

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/session"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

// Defaults of Options.
const (
	DefaultIdleTimeout = 10 * time.Minute
	DefaultWaitTimeout = time.Minute
)

// ErrClosed means the engine is stopping: no tunnel opens any more.
var ErrClosed = errors.New("the engine is stopping")

// Tunnel is an open tunnel (*tunnel.Tunnel; tests fake it).
type Tunnel interface {
	Addr() string
	Close() error
}

// Opener opens the tunnel of a target, reporting its states to onState. The
// tunnel lives until ctx ends or it is closed.
type Opener func(ctx context.Context, t config.Target, onState func(tunnel.State)) (Tunnel, error)

// Options of a Manager; zero values select the defaults.
type Options struct {
	// Open opens a tunnel (default: session.OpenTunnel on connect.local_port).
	Open Opener
	// IdleTimeout closes a tunnel opened by use after this long without users.
	IdleTimeout time.Duration
	// WaitTimeout bounds how long Acquire and Open wait for a tunnel that is
	// connecting or reconnecting.
	WaitTimeout time.Duration
	Log         *slog.Logger
}

// State of a managed tunnel.
type State struct {
	Status    tunnel.Status
	LocalPort int
	Endpoint  string
	Bridge    string
	Detail    string
	// Since is when Status began; zero for a tunnel never opened.
	Since time.Time
	// Users are the sessions going through it now.
	Users int
	// KeepOpen: opened on request, no idle close.
	KeepOpen bool
}

// Manager owns the shared tunnels of one engine process.
type Manager struct {
	opts   Options
	ctx    context.Context // parent of every tunnel; canceled by Close
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	entries map[key]*entry
	opening sync.WaitGroup // attempts in flight, which Close waits for
}

// key identifies a tunnel by what it connects to: a target whose
// connection settings change in the configuration gets a new tunnel.
type key struct {
	account, region, profile, instance, bridgeTag string
	localPort                                     int
}

func keyOf(t config.Target) key {
	return key{t.AccountID, t.Region, t.AWSProfile, t.Instance, t.Connect.BridgeTag, t.Connect.LocalPort}
}

type entry struct {
	target   config.Target
	tun      Tunnel             // nil until open, and once closed
	cancel   context.CancelFunc // ends the tunnel or the attempt to open it
	opening  bool
	attempt  int   // open attempts so far
	err      error // why the last attempt failed
	gen      int   // bumped by every attempt and close: stale callbacks are ignored
	state    State
	changed  chan struct{} // closed and replaced on every change
	users    int
	keepOpen bool
	idle     *time.Timer
	subs     map[chan State]struct{}
}

// New returns a manager; Close it when the engine stops.
func New(opts Options) *Manager {
	if opts.Open == nil {
		opts.Open = func(ctx context.Context, t config.Target, onState func(tunnel.State)) (Tunnel, error) {
			tun, err := session.OpenTunnel(ctx, t, t.Connect.LocalPort, onState)
			if err != nil {
				return nil, err
			}
			return tun, nil
		}
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = DefaultWaitTimeout
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{opts: opts, ctx: ctx, cancel: cancel, entries: map[key]*entry{}}
}

// Acquire lends the tunnel of t, opening it when needed and waiting while
// it connects or reconnects (WaitTimeout at most). Release the lease when
// the session ends.
func (m *Manager) Acquire(ctx context.Context, t config.Target) (tunnel.Lease, error) {
	e, err := m.entry(t)
	if err != nil {
		return tunnel.Lease{}, err
	}
	defer m.mu.Unlock()
	if err := m.await(ctx, e); err != nil {
		return tunnel.Lease{}, err
	}
	e.users++
	m.stopIdle(e)
	m.publish(e)
	return tunnel.Lease{Addr: e.tun.Addr(), Bridge: e.state.Bridge, Release: m.releaser(e, e.gen)}, nil
}

// Lender lends the tunnel of t through Acquire (for check.Request).
func (m *Manager) Lender(t config.Target) tunnel.Lender { return lender{m, t} }

type lender struct {
	m *Manager
	t config.Target
}

func (l lender) Lend(ctx context.Context) (tunnel.Lease, error) { return l.m.Acquire(ctx, l.t) }

// Open opens the tunnel of t now, waits until it is ready and keeps it open
// without users until CloseTarget or Close.
func (m *Manager) Open(ctx context.Context, t config.Target) (State, error) {
	e, err := m.entry(t)
	if err != nil {
		return State{}, err
	}
	defer m.mu.Unlock()
	e.keepOpen = true
	m.stopIdle(e)
	m.publish(e)
	err = m.await(ctx, e)
	if err != nil && e.tun == nil {
		e.keepOpen = false
		m.publish(e)
	}
	return m.snapshot(e), err
}

// CloseTarget closes the tunnel of t now, users or not: their sessions
// fail and the next one opens a new tunnel.
func (m *Manager) CloseTarget(t config.Target) State {
	m.mu.Lock()
	e := m.entries[keyOf(t)]
	if e == nil {
		m.mu.Unlock()
		return State{Status: tunnel.Closed}
	}
	stop := m.shut(e, "closed on request")
	st := m.snapshot(e)
	m.mu.Unlock()
	stop()
	return st
}

// State is the state of the tunnel of t (closed, with no Since, when it was
// never opened).
func (m *Manager) State(t config.Target) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[keyOf(t)]; e != nil {
		return m.snapshot(e)
	}
	return State{Status: tunnel.Closed}
}

// Subscribe sends the current state of the tunnel of t, then every change,
// until cancel is called or the manager closes (the channel then closes).
// A slow reader may miss intermediate states, never the last one.
func (m *Manager) Subscribe(t config.Target) (<-chan State, func()) {
	ch := make(chan State, 8)
	e, err := m.entry(t)
	if err != nil {
		close(ch)
		return ch, func() {}
	}
	defer m.mu.Unlock()
	e.subs[ch] = struct{}{}
	ch <- m.snapshot(e)
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, ok := e.subs[ch]; ok {
				delete(e.subs, ch)
				close(ch)
			}
		})
	}
}

// Close ends every tunnel and its SSM session, and refuses new ones. It
// waits for all of them.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	var stops []func()
	for _, e := range m.entries {
		stops = append(stops, m.shut(e, "the engine stopped"))
		for ch := range e.subs {
			delete(e.subs, ch)
			close(ch)
		}
	}
	m.mu.Unlock()
	m.cancel()
	var wg sync.WaitGroup
	for _, stop := range stops {
		wg.Go(stop)
	}
	wg.Wait()
	m.opening.Wait()
	return nil
}

// entry returns the entry of t, creating it, with the lock held. A tunnel
// with the same fixed local port but other settings (the configuration
// changed) and no users is closed first, or the new one could not bind.
func (m *Manager) entry(t config.Target) (*entry, error) {
	k := keyOf(t)
	m.mu.Lock()
	for {
		if m.closed {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		var stale *entry
		for k2, e2 := range m.entries {
			if k2 != k && k.localPort != 0 && k2.localPort == k.localPort && e2.users == 0 && e2.tun != nil {
				stale = e2
				break
			}
		}
		if stale == nil {
			break
		}
		stop := m.shut(stale, "replaced: the configuration of the target changed")
		m.mu.Unlock()
		stop()
		m.mu.Lock()
	}
	e := m.entries[k]
	if e == nil {
		e = &entry{target: t, changed: make(chan struct{}), subs: map[chan State]struct{}{},
			state: State{Status: tunnel.Closed, LocalPort: t.Connect.LocalPort}}
		m.entries[k] = e
	}
	e.target = t
	return e, nil
}

// await waits until the tunnel of e is ready, starting or joining an
// attempt to open it. Called and returns with the lock held.
func (m *Manager) await(ctx context.Context, e *entry) error {
	ctx, cancel := context.WithTimeout(ctx, m.opts.WaitTimeout)
	defer cancel()
	attempt := -1
	for {
		switch {
		case m.closed:
			return ErrClosed
		case e.tun != nil && e.state.Status == tunnel.Ready:
			return nil
		case e.opening:
			attempt = e.attempt // join it
		case e.tun == nil && attempt == e.attempt:
			// The attempt we waited for ended without a tunnel.
			if e.err != nil {
				return e.err
			}
			return errors.New("the tunnel was closed while opening")
		case e.tun == nil:
			attempt = m.startOpen(e)
		}
		wait := e.changed
		m.mu.Unlock()
		select {
		case <-wait:
			m.mu.Lock()
		case <-ctx.Done():
			m.mu.Lock()
			if e.state.Detail != "" {
				return fmt.Errorf("tunnel %s: %s", e.state.Status, e.state.Detail)
			}
			return fmt.Errorf("tunnel %s: %w", e.state.Status, ctx.Err())
		}
	}
}

// startOpen starts an attempt to open the tunnel of e; with the lock held.
func (m *Manager) startOpen(e *entry) int {
	e.attempt++
	e.gen++
	e.opening, e.err = true, nil
	gen, target := e.gen, e.target
	ctx, cancel := context.WithCancel(m.ctx)
	e.cancel = cancel
	e.state = State{Status: tunnel.Connecting, LocalPort: target.Connect.LocalPort, Since: time.Now(), Detail: "opening the tunnel"}
	m.publish(e)
	m.opts.Log.Info("shared tunnel opening", "instance", target.Instance)
	m.opening.Add(1)
	go func() {
		defer m.opening.Done()
		tun, err := m.opts.Open(ctx, target, func(s tunnel.State) { m.update(e, gen, s) })
		m.mu.Lock()
		e.opening = false
		if e.gen != gen || m.closed { // closed while opening
			m.publish(e)
			m.mu.Unlock()
			cancel()
			if tun != nil {
				_ = tun.Close()
			}
			return
		}
		if err != nil {
			cancel()
			e.err = err
			e.state = State{Status: tunnel.Closed, LocalPort: target.Connect.LocalPort, Since: time.Now(), Detail: err.Error()}
			m.opts.Log.Warn("shared tunnel failed", "instance", target.Instance, "err", err)
		} else {
			e.tun = tun
			if e.state.Status == tunnel.Connecting { // the opener reported no ready state
				e.state.Status = tunnel.Ready
			}
			if e.users == 0 && !e.keepOpen {
				m.armIdle(e)
			}
		}
		m.publish(e)
		m.mu.Unlock()
	}()
	return e.attempt
}

// update records a state reported by the tunnel of generation gen.
func (m *Manager) update(e *entry, gen int, s tunnel.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.gen != gen {
		return
	}
	e.state.Status, e.state.LocalPort, e.state.Endpoint = s.Status, s.LocalPort, s.Endpoint
	e.state.Detail, e.state.Since = s.Detail, s.Time
	if s.Bridge != "" {
		e.state.Bridge = s.Bridge
	}
	switch s.Status {
	case tunnel.Reconnecting:
		m.opts.Log.Warn("shared tunnel reconnecting", "instance", e.target.Instance, "detail", s.Detail)
	case tunnel.Ready:
		m.opts.Log.Info("shared tunnel ready", "instance", e.target.Instance, "detail", s.Detail)
	}
	m.publish(e)
}

// shut closes the tunnel of e (or cancels the attempt to open it); with the
// lock held. The returned stop does the waiting and must run without it.
func (m *Manager) shut(e *entry, why string) (stop func()) {
	tun, cancel := e.tun, e.cancel
	e.gen++
	e.tun, e.cancel = nil, nil
	e.users, e.keepOpen = 0, false
	m.stopIdle(e)
	if tun != nil || e.opening {
		e.state.Status, e.state.Detail, e.state.Since = tunnel.Closed, why, time.Now()
		m.opts.Log.Info("shared tunnel closed", "instance", e.target.Instance, "why", why)
	}
	m.publish(e)
	return func() {
		if cancel != nil {
			cancel()
		}
		if tun != nil {
			if err := tun.Close(); err != nil {
				m.opts.Log.Warn("shared tunnel: close", "instance", e.target.Instance, "err", err)
			}
		}
	}
}

// releaser hands back one lease of generation gen.
func (m *Manager) releaser(e *entry, gen int) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if e.gen != gen || e.users == 0 {
				return // the tunnel it lent is gone
			}
			e.users--
			if e.users == 0 && !e.keepOpen {
				m.armIdle(e)
			}
			m.publish(e)
		})
	}
}

func (m *Manager) armIdle(e *entry) {
	m.stopIdle(e)
	gen := e.gen
	e.idle = time.AfterFunc(m.opts.IdleTimeout, func() {
		m.mu.Lock()
		if e.gen != gen || e.users > 0 || e.keepOpen || e.tun == nil {
			m.mu.Unlock()
			return
		}
		stop := m.shut(e, fmt.Sprintf("closed after %s without use", m.opts.IdleTimeout))
		m.mu.Unlock()
		stop()
	})
}

func (m *Manager) stopIdle(e *entry) {
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
}

func (m *Manager) snapshot(e *entry) State {
	st := e.state
	st.Users, st.KeepOpen = e.users, e.keepOpen
	return st
}

// publish wakes the waiters and sends the state to the subscribers; with
// the lock held.
func (m *Manager) publish(e *entry) {
	close(e.changed)
	e.changed = make(chan struct{})
	st := m.snapshot(e)
	for ch := range e.subs {
		select {
		case ch <- st:
		default: // full: drop the oldest, keep the latest
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- st:
			default:
			}
		}
	}
}
