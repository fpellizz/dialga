package tunnels

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

// fakeOpener opens fake tunnels on 127.0.0.1:41001, 41002... reporting
// connecting then ready, like tunnel.Open.
type fakeOpener struct {
	mu      sync.Mutex
	tunnels []*fakeTunnel
	err     error
	block   chan struct{} // when set, Open waits for it (or its context)
}

func (f *fakeOpener) open(ctx context.Context, t config.Target, onState func(tunnel.State)) (Tunnel, error) {
	onState(tunnel.State{Status: tunnel.Connecting, Time: time.Now(), LocalPort: t.Connect.LocalPort, Endpoint: "db.example:1433"})
	f.mu.Lock()
	block, err := f.block, f.err
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	ft := &fakeTunnel{addr: "127.0.0.1:" + strconv.Itoa(41000+len(f.tunnels)+1), onState: onState}
	f.tunnels = append(f.tunnels, ft)
	f.mu.Unlock()
	ft.emit(tunnel.Ready, "ready")
	return ft, nil
}

func (f *fakeOpener) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tunnels)
}

func (f *fakeOpener) tunnel(i int) *fakeTunnel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tunnels[i]
}

type fakeTunnel struct {
	addr    string
	onState func(tunnel.State)
	mu      sync.Mutex
	closed  int
}

func (t *fakeTunnel) Addr() string { return t.addr }

func (t *fakeTunnel) Close() error {
	t.mu.Lock()
	t.closed++
	t.mu.Unlock()
	t.emit(tunnel.Closed, "") // like tunnel.Tunnel: ignored by the manager
	return nil
}

func (t *fakeTunnel) closes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *fakeTunnel) emit(st tunnel.Status, detail string) {
	t.onState(tunnel.State{Status: st, Time: time.Now(), LocalPort: 41433, Endpoint: "db.example:1433", Bridge: "i-1", Detail: detail})
}

var target = config.Target{AccountID: "123456789012", Region: "us-east-1", AWSProfile: "sales", Instance: "dev-sql",
	Connect: config.Connect{Mode: config.ModeTunnel, LocalPort: 41433}}

func newManager(t *testing.T, opts Options) (*Manager, *fakeOpener) {
	t.Helper()
	f := &fakeOpener{}
	opts.Open = f.open
	m := New(opts)
	t.Cleanup(func() { _ = m.Close() })
	return m, f
}

// eventually fails the test unless cond holds within 2 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("not within 2s: %s", what)
}

func TestAcquireSharesOneTunnel(t *testing.T) {
	m, f := newManager(t, Options{})
	var wg sync.WaitGroup
	leases := make([]tunnel.Lease, 5)
	errs := make([]error, 5)
	for i := range leases {
		wg.Go(func() { leases[i], errs[i] = m.Acquire(context.Background(), target) })
	}
	wg.Wait()
	for i := range leases {
		if errs[i] != nil || leases[i].Addr != "127.0.0.1:41001" || leases[i].Bridge != "i-1" {
			t.Fatalf("lease %d: %+v, %v", i, leases[i], errs[i])
		}
	}
	if st := m.State(target); f.count() != 1 || st.Status != tunnel.Ready || st.Users != 5 || st.Endpoint != "db.example:1433" {
		t.Fatalf("%d tunnels opened, state %+v", f.count(), st)
	}
	for _, l := range leases {
		l.Release()
		l.Release() // twice: no effect
	}
	if st := m.State(target); st.Users != 0 || st.Status != tunnel.Ready || f.tunnel(0).closes() != 0 {
		t.Fatalf("after release: %+v, closes %d", st, f.tunnel(0).closes())
	}
	if l, err := m.Lender(target).Lend(context.Background()); err != nil || l.Addr != "127.0.0.1:41001" || f.count() != 1 {
		t.Fatalf("Lend: %+v, %v, %d tunnels", l, err, f.count())
	}
}

func TestIdleClose(t *testing.T) {
	m, f := newManager(t, Options{IdleTimeout: 20 * time.Millisecond})
	l, err := m.Acquire(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if f.tunnel(0).closes() != 0 {
		t.Fatal("closed while in use")
	}
	l.Release()
	eventually(t, "idle close", func() bool { return f.tunnel(0).closes() == 1 })
	if st := m.State(target); st.Status != tunnel.Closed || !strings.Contains(st.Detail, "without use") {
		t.Fatalf("state %+v", st)
	}
	if _, err := m.Acquire(context.Background(), target); err != nil || f.count() != 2 {
		t.Fatalf("reopen: %v, %d tunnels", err, f.count())
	}
}

func TestOpenKeepsTheTunnelUntilCloseTarget(t *testing.T) {
	m, f := newManager(t, Options{IdleTimeout: 10 * time.Millisecond})
	st, err := m.Open(context.Background(), target)
	if err != nil || st.Status != tunnel.Ready || !st.KeepOpen {
		t.Fatalf("Open() = %+v, %v", st, err)
	}
	l, _ := m.Acquire(context.Background(), target)
	l.Release()
	time.Sleep(50 * time.Millisecond)
	if f.tunnel(0).closes() != 0 || f.count() != 1 {
		t.Fatal("a tunnel opened on request was closed for idleness")
	}
	st = m.CloseTarget(target)
	if st.Status != tunnel.Closed || st.Detail != "closed on request" || st.KeepOpen || f.tunnel(0).closes() != 1 {
		t.Fatalf("CloseTarget() = %+v, closes %d", st, f.tunnel(0).closes())
	}
	if st := m.CloseTarget(config.Target{Instance: "other"}); st.Status != tunnel.Closed {
		t.Fatalf("unknown target: %+v", st)
	}
}

func TestReconnectIsVisible(t *testing.T) {
	m, f := newManager(t, Options{})
	events, cancel := m.Subscribe(target)
	defer cancel()
	if st := <-events; st.Status != tunnel.Closed || !st.Since.IsZero() {
		t.Fatalf("first event %+v", st)
	}
	l, err := m.Acquire(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	f.tunnel(0).emit(tunnel.Reconnecting, "session-manager-plugin exited (signal: killed): reconnecting in 1s")
	next := func(want tunnel.Status) tunnel.State {
		t.Helper()
		for {
			select {
			case st := <-events:
				if st.Status == want {
					return tunnel.State{Status: st.Status, Detail: st.Detail}
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("no %s event", want)
			}
		}
	}
	if st := next(tunnel.Reconnecting); !strings.Contains(st.Detail, "signal: killed") {
		t.Fatalf("reconnecting event %+v", st)
	}

	// While it reconnects, a session waits for it, or gives up with the reason.
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, err := m.Acquire(short, target); err == nil || !strings.Contains(err.Error(), "tunnel reconnecting: session-manager-plugin exited") {
		t.Fatalf("Acquire while reconnecting: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		l2, err := m.Acquire(context.Background(), target)
		if err == nil {
			l2.Release()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	f.tunnel(0).emit(tunnel.Ready, "127.0.0.1:41433 → db.example:1433 via bridge i-1")
	next(tunnel.Ready)
	if err := <-got; err != nil || f.count() != 1 {
		t.Fatalf("Acquire after the reconnection: %v, %d tunnels", err, f.count())
	}
	l.Release()
}

func TestOpenFailureIsSharedAndRetried(t *testing.T) {
	m, f := newManager(t, Options{})
	f.err = errors.New("no instance tagged Name=ssm-bridge")
	f.block = make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() { _, errs[i] = m.Acquire(context.Background(), target) })
	}
	eventually(t, "connecting", func() bool { return m.State(target).Status == tunnel.Connecting })
	close(f.block)
	wg.Wait()
	for i, err := range errs {
		if err == nil || err.Error() != "no instance tagged Name=ssm-bridge" {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if st := m.State(target); st.Status != tunnel.Closed || !st.Failed || st.Detail != "no instance tagged Name=ssm-bridge" {
		t.Fatalf("state %+v", st)
	}
	f.mu.Lock()
	f.err, f.block = nil, nil
	f.mu.Unlock()
	if _, err := m.Acquire(context.Background(), target); err != nil || f.count() != 1 {
		t.Fatalf("retry: %v, %d tunnels", err, f.count())
	}
}

func TestCloseTargetWithUsers(t *testing.T) {
	m, f := newManager(t, Options{})
	old, _ := m.Acquire(context.Background(), target)
	m.CloseTarget(target)
	old.Release() // the tunnel it lent is gone: no effect on the next one
	l, err := m.Acquire(context.Background(), target)
	if err != nil || f.count() != 2 || l.Addr != "127.0.0.1:41002" || m.State(target).Users != 1 {
		t.Fatalf("Acquire after CloseTarget: %+v, %v, state %+v", l, err, m.State(target))
	}
}

func TestChangedConfigurationReplacesTheTunnelOnTheSamePort(t *testing.T) {
	m, f := newManager(t, Options{})
	l, _ := m.Acquire(context.Background(), target)
	l.Release()
	changed := target
	changed.Connect.BridgeTag = "other-bridge"
	if _, err := m.Acquire(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 || f.tunnel(0).closes() != 1 || m.State(target).Status != tunnel.Closed {
		t.Fatalf("%d tunnels, first closed %d times, old state %+v", f.count(), f.tunnel(0).closes(), m.State(target))
	}
}

func TestCloseEndsEveryTunnel(t *testing.T) {
	m, f := newManager(t, Options{})
	other := target
	other.Instance, other.Connect.LocalPort = "mssql-test", 0
	if _, err := m.Acquire(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	events, cancel := m.Subscribe(target)
	defer cancel()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if f.tunnel(0).closes() != 1 || f.tunnel(1).closes() != 1 {
		t.Fatalf("closes %d, %d", f.tunnel(0).closes(), f.tunnel(1).closes())
	}
	var last State
	for st := range events { // closed by Close
		last = st
	}
	if last.Status != tunnel.Closed || last.Detail != "the engine stopped" {
		t.Fatalf("last event %+v", last)
	}
	if _, err := m.Acquire(context.Background(), target); !errors.Is(err, ErrClosed) {
		t.Fatalf("Acquire after Close: %v", err)
	}
	if _, err := m.Open(context.Background(), target); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open after Close: %v", err)
	}
	if ch, _ := m.Subscribe(target); func() bool { _, ok := <-ch; return ok }() {
		t.Fatal("Subscribe after Close sent a state")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCloseWhileOpening(t *testing.T) {
	m, f := newManager(t, Options{})
	f.block = make(chan struct{})
	got := make(chan error, 1)
	go func() {
		_, err := m.Acquire(context.Background(), target)
		got <- err
	}()
	eventually(t, "connecting", func() bool { return m.State(target).Status == tunnel.Connecting })
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err == nil {
		t.Fatal("Acquire succeeded while the engine stopped")
	}
	if f.count() != 0 {
		t.Fatal("a tunnel opened after Close")
	}
}
