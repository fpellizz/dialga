// Package tunnel opens the SSM port-forwarding tunnel to an RDS endpoint
// through the bridge instance of its VPC, the way "aws ssm start-session"
// does: StartSession through the SDK, then session-manager-plugin with that
// session. It is the Go counterpart of the dev-mssql target of
// open-dloop-tunnels.sh: bridge tagged Name=ssm-bridge in the VPC of the
// target and running, document AWS-StartPortForwardingSessionToRemoteHost.
//
// The tunnel listens on 127.0.0.1. When the plugin exits or the port stops
// answering, it reconnects by itself on the same local port, with a capped
// backoff, until its context ends or Close is called: SQL pools and task
// polling through it resume once the new session is up.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// DefaultBridgeTag is the Name tag of the bridge instance.
const DefaultBridgeTag = "ssm-bridge"

// Document is the SSM document of the port forwarding to a remote host.
const Document = "AWS-StartPortForwardingSessionToRemoteHost"

// Timings that are not worth an option.
const (
	readyPoll        = 200 * time.Millisecond // between two readiness dials
	probeTimeout     = 3 * time.Second        // one dial of the local port
	probeMisses      = 2                      // failed probes in a row before a reconnection
	stableAfter      = time.Minute            // a session up this long resets the backoff
	terminateTimeout = 10 * time.Second       // TerminateSession, also after the context ended
)

// Target is the forwarding to open.
type Target struct {
	Region string
	// Profile is passed to session-manager-plugin, which calls SSM with it.
	Profile string
	// VPC of the RDS instance: the bridge must be in it.
	VPC string
	// Host and Port are the RDS endpoint.
	Host string
	Port int
	// LocalPort on 127.0.0.1; 0 picks a free one, kept across reconnections.
	LocalPort int
	// BridgeTag is the Name tag of the bridge (default ssm-bridge).
	BridgeTag string
}

// Status of the tunnel.
type Status string

const (
	Connecting   Status = "connecting"
	Ready        Status = "ready"
	Reconnecting Status = "reconnecting"
	Closed       Status = "closed"
)

// State is reported on every change: the CLI prints it, the GUI will show it.
type State struct {
	Status    Status    `json:"status"`
	Time      time.Time `json:"time"`
	LocalPort int       `json:"localPort"`
	// Endpoint is host:port of the RDS instance.
	Endpoint string `json:"endpoint"`
	Bridge   string `json:"bridge,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// EC2API is the part of the EC2 client the bridge lookup uses.
type EC2API interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// SSMAPI is the part of the SSM client the sessions use.
type SSMAPI interface {
	StartSession(context.Context, *ssm.StartSessionInput, ...func(*ssm.Options)) (*ssm.StartSessionOutput, error)
	TerminateSession(context.Context, *ssm.TerminateSessionInput, ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error)
}

// Options are the dependencies and timings of a tunnel; zero values select
// the real clients and the defaults.
type Options struct {
	EC2 EC2API
	SSM SSMAPI
	// SSMEndpoint is the URL the plugin talks to (default: resolved like the SDK does).
	SSMEndpoint string
	// Launcher starts session-manager-plugin (default: FindPlugin).
	Launcher Launcher
	// OnState receives every change of state, one call at a time.
	OnState func(State)
	// ReadyTimeout is how long a new session has to answer on the local port (30s).
	ReadyTimeout time.Duration
	// ProbeInterval is how often the local port is probed while ready (30s).
	ProbeInterval time.Duration
	// MinBackoff and MaxBackoff bound the delay before a reconnection (1s, 30s).
	MinBackoff, MaxBackoff time.Duration
}

// Tunnel is an open tunnel; Close ends it.
type Tunnel struct {
	target   Target
	opts     Options
	port     int
	addr     string
	endpoint string

	cancel   context.CancelFunc
	done     chan struct{}
	closeErr error // set by the supervisor before done is closed

	mu     sync.Mutex
	bridge string
}

// session is one SSM session with its plugin process.
type session struct {
	id      string
	bridge  string
	proc    Process
	exited  chan struct{}
	waitErr error // valid once exited is closed
	ready   time.Time
}

// Open finds the bridge, starts the session and waits until the local port
// answers. The tunnel then stays up, reconnecting when needed, until ctx
// ends or Close is called; both terminate the session.
func Open(ctx context.Context, cfg aws.Config, target Target, opts Options) (*Tunnel, error) {
	if target.BridgeTag == "" {
		target.BridgeTag = DefaultBridgeTag
	}
	if target.Host == "" || target.Port < 1 || target.Port > 65535 || target.VPC == "" || target.Region == "" {
		return nil, errors.New("tunnel: region, VPC and endpoint are required")
	}
	if target.LocalPort < 0 || target.LocalPort > 65535 {
		return nil, fmt.Errorf("invalid local port %d", target.LocalPort)
	}
	opts.defaults()
	if opts.Launcher == nil {
		l, err := FindPlugin(ctx)
		if err != nil {
			return nil, err
		}
		opts.Launcher = l
	}
	if opts.EC2 == nil {
		opts.EC2 = ec2.NewFromConfig(cfg)
	}
	if opts.SSM == nil {
		client := ssm.NewFromConfig(cfg)
		opts.SSM = client
		if opts.SSMEndpoint == "" {
			ep, err := endpointOf(ctx, client.Options())
			if err != nil {
				return nil, fmt.Errorf("SSM endpoint: %w", err)
			}
			opts.SSMEndpoint = ep
		}
	}
	if opts.SSMEndpoint == "" {
		ep, err := endpointOf(ctx, ssm.Options{Region: target.Region})
		if err != nil {
			return nil, fmt.Errorf("SSM endpoint: %w", err)
		}
		opts.SSMEndpoint = ep
	}
	port, err := freePort(target.LocalPort)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{
		target:   target,
		opts:     opts,
		port:     port,
		addr:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		endpoint: net.JoinHostPort(target.Host, strconv.Itoa(target.Port)),
		done:     make(chan struct{}),
	}
	t.emit(Connecting, "", fmt.Sprintf("looking for the bridge (Name=%s) in %s", target.BridgeTag, target.VPC))
	s, err := t.connect(ctx)
	if err != nil {
		return nil, err
	}
	t.emit(Ready, s.bridge, t.readyDetail(s))
	ctx, t.cancel = context.WithCancel(ctx)
	go t.supervise(ctx, s)
	return t, nil
}

func (o *Options) defaults() {
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = 30 * time.Second
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = 30 * time.Second
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = max(30*time.Second, o.MinBackoff)
	}
}

// Addr is 127.0.0.1:port, where SQL clients connect.
func (t *Tunnel) Addr() string { return t.addr }

// LocalPort is the port on 127.0.0.1.
func (t *Tunnel) LocalPort() int { return t.port }

// Bridge is the instance of the current session.
func (t *Tunnel) Bridge() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bridge
}

// Done is closed when the tunnel has ended for good (context or Close).
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Close stops the plugin and terminates the session. It waits for both and
// can be called more than once.
func (t *Tunnel) Close() error {
	t.cancel()
	<-t.done
	return t.closeErr
}

// connect opens one session: bridge, StartSession, plugin, readiness. On
// failure nothing is left behind.
func (t *Tunnel) connect(ctx context.Context) (*session, error) {
	bridge, err := FindBridge(ctx, t.opts.EC2, t.target.BridgeTag, t.target.VPC)
	if err != nil {
		return nil, err
	}
	in := &ssm.StartSessionInput{
		Target:       aws.String(bridge),
		DocumentName: aws.String(Document),
		Parameters: map[string][]string{
			"host":            {t.target.Host},
			"portNumber":      {strconv.Itoa(t.target.Port)},
			"localPortNumber": {strconv.Itoa(t.port)},
		},
	}
	out, err := t.opts.SSM.StartSession(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("ssm StartSession on %s: %w", bridge, err)
	}
	s := &session{id: aws.ToString(out.SessionId), bridge: bridge, exited: make(chan struct{})}
	args, env, err := pluginArgs(out, t.target.Region, t.target.Profile, in, t.opts.SSMEndpoint)
	if err == nil {
		s.proc, err = t.opts.Launcher.Start(args, env)
	}
	if err != nil {
		// Like the AWS CLI: no zombie session on the agent until its timeout.
		_ = t.terminate(ctx, s.id)
		return nil, fmt.Errorf("session-manager-plugin: %w", err)
	}
	go func() {
		s.waitErr = s.proc.Wait()
		close(s.exited)
	}()
	if err := t.waitReady(ctx, s); err != nil {
		_ = t.drop(ctx, s)
		return nil, err
	}
	s.ready = time.Now()
	t.mu.Lock()
	t.bridge = bridge
	t.mu.Unlock()
	return s, nil
}

// waitReady waits until the local port accepts connections.
func (t *Tunnel) waitReady(ctx context.Context, s *session) error {
	ctx, cancel := context.WithTimeout(ctx, t.opts.ReadyTimeout)
	defer cancel()
	tick := time.NewTicker(readyPoll)
	defer tick.Stop()
	for {
		if t.probe(ctx) == nil {
			return nil
		}
		select {
		case <-s.exited:
			return errors.New(s.exitReason() + " before the tunnel was ready")
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("the tunnel did not answer on %s within %s", t.addr, t.opts.ReadyTimeout)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// probe dials the local port once.
func (t *Tunnel) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// supervise keeps the tunnel up until ctx ends, then closes it.
func (t *Tunnel) supervise(ctx context.Context, s *session) {
	defer close(t.done)
	failures := 0
	for {
		reason := t.watch(ctx, s)
		err := t.drop(ctx, s)
		if ctx.Err() != nil {
			t.closeErr = err
			t.emit(Closed, s.bridge, "")
			return
		}
		if time.Since(s.ready) >= stableAfter {
			failures = 0
		}
		for {
			failures++
			delay := backoff(failures, t.opts.MinBackoff, t.opts.MaxBackoff)
			t.emit(Reconnecting, s.bridge, fmt.Sprintf("%s: reconnecting in %s", reason, delay))
			select {
			case <-ctx.Done():
				t.emit(Closed, s.bridge, "")
				return
			case <-time.After(delay):
			}
			next, err := t.connect(ctx)
			if err == nil {
				s = next
				t.emit(Ready, s.bridge, t.readyDetail(s))
				break
			}
			if ctx.Err() != nil {
				t.emit(Closed, s.bridge, "")
				return
			}
			reason = err.Error()
		}
	}
}

// watch returns why the session is no longer usable, or "" when ctx ended.
func (t *Tunnel) watch(ctx context.Context, s *session) string {
	tick := time.NewTicker(t.opts.ProbeInterval)
	defer tick.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-s.exited:
			return s.exitReason()
		case <-tick.C:
			if err := t.probe(ctx); err == nil {
				misses = 0
			} else if misses++; misses >= probeMisses && ctx.Err() == nil {
				return t.addr + " stopped answering"
			}
		}
	}
}

// drop ends a session: the plugin first, then the session on the SSM side,
// which also covers a plugin that died before it could say goodbye.
func (t *Tunnel) drop(ctx context.Context, s *session) error {
	s.proc.Stop()
	return t.terminate(ctx, s.id)
}

func (t *Tunnel) terminate(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminateTimeout)
	defer cancel()
	if _, err := t.opts.SSM.TerminateSession(ctx, &ssm.TerminateSessionInput{SessionId: aws.String(id)}); err != nil {
		return fmt.Errorf("ssm TerminateSession %s: %w", id, err)
	}
	return nil
}

func (t *Tunnel) emit(st Status, bridge, detail string) {
	if t.opts.OnState != nil {
		t.opts.OnState(State{Status: st, Time: time.Now(), LocalPort: t.port, Endpoint: t.endpoint, Bridge: bridge, Detail: detail})
	}
}

func (t *Tunnel) readyDetail(s *session) string {
	return fmt.Sprintf("%s → %s via bridge %s", t.addr, t.endpoint, s.bridge)
}

func (s *session) exitReason() string {
	msg := "session-manager-plugin exited"
	if s.waitErr != nil {
		msg += " (" + s.waitErr.Error() + ")"
	}
	if out := s.proc.Output(); out != "" {
		msg += ": " + out
	}
	return msg
}

// backoff is the delay before the n-th reconnection in a row: lo, 2·lo,
// 4·lo... up to hi.
func backoff(n int, lo, hi time.Duration) time.Duration {
	d := lo
	for i := 1; i < n && d < hi; i++ {
		d *= 2
	}
	return min(d, hi)
}

// freePort checks that port is free on 127.0.0.1, or picks a free one when
// port is 0. The plugin binds it right after.
func freePort(port int) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return 0, fmt.Errorf("127.0.0.1:%d is in use (another tunnel?): choose another local port", port)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	return p, ln.Close()
}
