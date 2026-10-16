package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// fakeSSM numbers the sessions s-1, s-2... and records the terminated ones.
type fakeSSM struct {
	mu         sync.Mutex
	started    []*ssm.StartSessionInput
	terminated []string
	failStarts int // the next failStarts calls fail
}

func (f *fakeSSM) StartSession(_ context.Context, in *ssm.StartSessionInput, _ ...func(*ssm.Options)) (*ssm.StartSessionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStarts > 0 {
		f.failStarts--
		return nil, errors.New("TargetNotConnected")
	}
	f.started = append(f.started, in)
	n := strconv.Itoa(len(f.started))
	return &ssm.StartSessionOutput{SessionId: aws.String("s-" + n), TokenValue: aws.String("token-" + n),
		StreamUrl: aws.String("wss://ssmmessages.example/s-" + n)}, nil
}

func (f *fakeSSM) TerminateSession(_ context.Context, in *ssm.TerminateSessionInput, _ ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, aws.ToString(in.SessionId))
	return &ssm.TerminateSessionOutput{SessionId: in.SessionId}, nil
}

func (f *fakeSSM) counts() (started int, terminated []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started), append([]string(nil), f.terminated...)
}

// fakeLauncher starts fake plugins that listen on the requested local port
// (unless silent) and accept and drop connections.
type fakeLauncher struct {
	mu       sync.Mutex
	procs    []*fakeProc
	silent   bool   // processes never listen
	exitWith string // processes exit at once, printing this
	startErr error
}

func (l *fakeLauncher) Start(args, env []string) (Process, error) {
	if l.startErr != nil {
		return nil, l.startErr
	}
	var req struct{ Parameters map[string][]string }
	if err := json.Unmarshal([]byte(args[4]), &req); err != nil {
		return nil, err
	}
	p := &fakeProc{done: make(chan struct{}), env: env}
	if l.exitWith != "" {
		p.output = l.exitWith
		p.die(errors.New("exit status 1"))
	} else if !l.silent {
		ln, err := net.Listen("tcp", "127.0.0.1:"+req.Parameters["localPortNumber"][0])
		if err != nil {
			return nil, err
		}
		p.ln = ln
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	l.mu.Lock()
	l.procs = append(l.procs, p)
	l.mu.Unlock()
	return p, nil
}

func (l *fakeLauncher) proc(i int) *fakeProc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.procs[i]
}

type fakeProc struct {
	ln      net.Listener
	env     []string
	output  string
	once    sync.Once
	done    chan struct{}
	err     error
	mu      sync.Mutex
	stopped bool
}

// die ends the process with err, closing its port.
func (p *fakeProc) die(err error) {
	p.once.Do(func() {
		if p.ln != nil {
			_ = p.ln.Close()
		}
		p.err = err
		close(p.done)
	})
}

func (p *fakeProc) Wait() error {
	<-p.done
	return p.err
}

func (p *fakeProc) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.die(nil)
}

func (p *fakeProc) Output() string { return p.output }

func (p *fakeProc) wasStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

var testTarget = Target{Region: "us-east-1", Profile: "sales", VPC: "vpc-a", Host: "db.example.rds.amazonaws.com", Port: 1433}

type harness struct {
	ec2    *fakeEC2
	ssm    *fakeSSM
	plugin *fakeLauncher
	states chan State
	opts   Options
}

func newHarness() *harness {
	h := &harness{
		ec2:    &fakeEC2{instances: []ec2types.Instance{instance("i-1", "vpc-a", ec2types.InstanceStateNameRunning)}},
		ssm:    &fakeSSM{},
		plugin: &fakeLauncher{},
		states: make(chan State, 100),
	}
	h.opts = Options{EC2: h.ec2, SSM: h.ssm, Launcher: h.plugin, SSMEndpoint: "https://ssm.us-east-1.amazonaws.com",
		OnState: func(s State) { h.states <- s }, ReadyTimeout: 2 * time.Second,
		ProbeInterval: time.Hour, MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
	return h
}

func (h *harness) open(t *testing.T, ctx context.Context, target Target) *Tunnel {
	t.Helper()
	tun, err := Open(ctx, aws.Config{}, target, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tun.Close() })
	return tun
}

// next waits for a state with the given status, skipping the others.
func (h *harness) next(t *testing.T, want Status) State {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-h.states:
			if s.Status == want {
				return s
			}
		case <-timeout:
			t.Fatalf("no %s state within 5s", want)
		}
	}
}

func dial(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

func TestOpenReadyThenClose(t *testing.T) {
	h := newHarness()
	tun := h.open(t, context.Background(), testTarget)
	if s := h.next(t, Connecting); !strings.Contains(s.Detail, "Name=ssm-bridge") || s.Endpoint != "db.example.rds.amazonaws.com:1433" {
		t.Fatalf("connecting state %+v", s)
	}
	ready := h.next(t, Ready)
	if ready.Bridge != "i-1" || ready.LocalPort != tun.LocalPort() || tun.LocalPort() == 0 ||
		ready.Detail != tun.Addr()+" → db.example.rds.amazonaws.com:1433 via bridge i-1" {
		t.Fatalf("ready state %+v, addr %s", ready, tun.Addr())
	}
	if err := dial(tun.Addr()); err != nil || tun.Bridge() != "i-1" {
		t.Fatalf("dial %s: %v, bridge %q", tun.Addr(), err, tun.Bridge())
	}
	in := h.ssm.started[0]
	if aws.ToString(in.Target) != "i-1" || aws.ToString(in.DocumentName) != Document ||
		in.Parameters["host"][0] != testTarget.Host || in.Parameters["portNumber"][0] != "1433" ||
		in.Parameters["localPortNumber"][0] != strconv.Itoa(tun.LocalPort()) {
		t.Fatalf("StartSession input %+v", in)
	}
	if env := h.plugin.proc(0).env; len(env) != 1 || !strings.Contains(env[0], `"TokenValue":"token-1"`) {
		t.Fatalf("plugin env %q", env)
	}

	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	h.next(t, Closed)
	if _, terminated := h.ssm.counts(); !equal(terminated, []string{"s-1"}) || !h.plugin.proc(0).wasStopped() {
		t.Fatalf("terminated %v, stopped %v", terminated, h.plugin.proc(0).wasStopped())
	}
	select {
	case <-tun.Done():
	default:
		t.Fatal("Done not closed after Close")
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, terminated := h.ssm.counts(); len(terminated) != 1 {
		t.Fatalf("second Close terminated again: %v", terminated)
	}
}

func TestOpenFixedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	h := newHarness()
	target := testTarget
	target.LocalPort = port
	if _, err := Open(context.Background(), aws.Config{}, target, h.opts); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("busy port: %v", err)
	}
	if started, _ := h.ssm.counts(); started != 0 {
		t.Fatal("a session was started on a busy port")
	}
	_ = ln.Close()
	if tun := h.open(t, context.Background(), target); tun.LocalPort() != port {
		t.Fatalf("local port %d, want %d", tun.LocalPort(), port)
	}
}

func TestOpenFailuresLeaveNothingBehind(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*harness)
		wantErr string
		started int
	}{
		{"no bridge", func(h *harness) { h.ec2.instances = nil }, "no instance tagged Name=ssm-bridge", 0},
		{"StartSession fails", func(h *harness) { h.ssm.failStarts = 1 }, "ssm StartSession on i-1: TargetNotConnected", 0},
		{"plugin does not start", func(h *harness) { h.plugin.startErr = errors.New("exec format error") }, "session-manager-plugin: exec format error", 1},
		{"plugin exits", func(h *harness) { h.plugin.exitWith = "An error occurred (TargetNotConnected)" },
			"session-manager-plugin exited (exit status 1): An error occurred (TargetNotConnected) before the tunnel was ready", 1},
		{"port never answers", func(h *harness) { h.plugin.silent = true; h.opts.ReadyTimeout = 300 * time.Millisecond },
			"did not answer on 127.0.0.1:", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			tc.setup(h)
			_, err := Open(context.Background(), aws.Config{}, testTarget, h.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err %v, want %q", err, tc.wantErr)
			}
			started, terminated := h.ssm.counts()
			if started != tc.started || len(terminated) != tc.started {
				t.Fatalf("started %d sessions, terminated %v", started, terminated)
			}
			for i := range h.plugin.procs {
				if p := h.plugin.proc(i); p.ln != nil && dial(p.ln.Addr().String()) == nil {
					t.Fatal("the plugin is still listening")
				}
			}
		})
	}
}

func TestOpenRejectsIncompleteTarget(t *testing.T) {
	h := newHarness()
	for _, target := range []Target{{Region: "us-east-1", VPC: "vpc-a"}, {Host: "h", Port: 1433, Region: "us-east-1"},
		{Host: "h", Port: 1433, Region: "us-east-1", VPC: "vpc-a", LocalPort: 70000}} {
		if _, err := Open(context.Background(), aws.Config{}, target, h.opts); err == nil {
			t.Fatalf("Open(%+v) accepted", target)
		}
	}
}

func TestReconnectWhenThePluginDies(t *testing.T) {
	h := newHarness()
	tun := h.open(t, context.Background(), testTarget)
	h.next(t, Ready)
	port := tun.LocalPort()

	h.plugin.proc(0).die(errors.New("signal: killed"))
	if s := h.next(t, Reconnecting); !strings.Contains(s.Detail, "session-manager-plugin exited (signal: killed): reconnecting in") {
		t.Fatalf("reconnecting state %+v", s)
	}
	if s := h.next(t, Ready); s.LocalPort != port {
		t.Fatalf("ready again on port %d, want %d", s.LocalPort, port)
	}
	if err := dial(tun.Addr()); err != nil {
		t.Fatalf("dial after the reconnection: %v", err)
	}
	if started, terminated := h.ssm.counts(); started != 2 || !equal(terminated, []string{"s-1"}) {
		t.Fatalf("started %d, terminated %v", started, terminated)
	}
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	if _, terminated := h.ssm.counts(); !equal(terminated, []string{"s-1", "s-2"}) {
		t.Fatalf("terminated %v", terminated)
	}
}

func TestReconnectWhenThePortStopsAnswering(t *testing.T) {
	h := newHarness()
	h.opts.ProbeInterval = 10 * time.Millisecond
	tun := h.open(t, context.Background(), testTarget)
	h.next(t, Ready)

	_ = h.plugin.proc(0).ln.Close() // alive, but no longer listening
	if s := h.next(t, Reconnecting); !strings.Contains(s.Detail, tun.Addr()+" stopped answering") {
		t.Fatalf("reconnecting state %+v", s)
	}
	h.next(t, Ready)
	if !h.plugin.proc(0).wasStopped() {
		t.Fatal("the stale plugin was not stopped")
	}
}

func TestReconnectRetriesWithBackoff(t *testing.T) {
	h := newHarness()
	tun := h.open(t, context.Background(), testTarget)
	h.next(t, Ready)

	h.ssm.mu.Lock()
	h.ssm.failStarts = 2
	h.ssm.mu.Unlock()
	h.plugin.proc(0).die(nil)
	h.next(t, Reconnecting)
	for _, want := range []string{"TargetNotConnected: reconnecting in 2ms", "TargetNotConnected: reconnecting in 4ms"} {
		if s := h.next(t, Reconnecting); !strings.Contains(s.Detail, want) {
			t.Fatalf("reconnecting state %+v, want %q", s, want)
		}
	}
	h.next(t, Ready)
	if err := dial(tun.Addr()); err != nil {
		t.Fatal(err)
	}
}

func TestContextEndClosesTheTunnel(t *testing.T) {
	h := newHarness()
	ctx, cancel := context.WithCancel(context.Background())
	tun := h.open(t, ctx, testTarget)
	h.next(t, Ready)
	cancel()
	select {
	case <-tun.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel outlived its context")
	}
	h.next(t, Closed)
	if _, terminated := h.ssm.counts(); !equal(terminated, []string{"s-1"}) {
		t.Fatalf("terminated %v", terminated)
	}
}

func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 5: 16 * time.Second,
		6: 30 * time.Second, 50: 30 * time.Second} {
		if got := backoff(n, time.Second, 30*time.Second); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
}
