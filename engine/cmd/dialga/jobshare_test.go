package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/session"
	"github.com/decisyon/dialga/engine/internal/tunnel"
	"github.com/decisyon/dialga/engine/internal/tunnels"
)

// countedTunnels opens stub tunnels and counts them.
type countedTunnels struct {
	mu             sync.Mutex
	opens, closes  int
	leases, active int
}

func (c *countedTunnels) open(context.Context, config.Target, func(tunnel.State)) (tunnels.Tunnel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	return stubTunnel{c}, nil
}

type stubTunnel struct{ c *countedTunnels }

func (s stubTunnel) Addr() string { return "127.0.0.1:41999" }

func (s stubTunnel) Close() error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	s.c.closes++
	return nil
}

// tunnelJobApp is a job app in tunnel mode whose sessions borrow the job's
// tunnel and count their leases (active: borrowed and not yet released).
func tunnelJobApp(t *testing.T) (*app, *countedTunnels, *jobRDS) {
	t.Helper()
	doc := strings.Replace(testConfig, "mode: external, local_port: 11433", "mode: tunnel", 1)
	a, rds, s3 := jobApp(t, doc)
	tun := &countedTunnels{}
	a.jobTunnel = tun.open
	a.openOps = func(context.Context, string, config.Target, string) (*ops.Session, func(), error) {
		return nil, nil, errors.New("a tunnel-mode job opened a session with a tunnel of its own")
	}
	a.openOpsVia = func(s session.Sessions) func(context.Context, string, config.Target, string) (*ops.Session, func(), error) {
		return func(ctx context.Context, name string, tgt config.Target, _ string) (*ops.Session, func(), error) {
			lease, err := s.Tunnels.Acquire(ctx, tgt)
			if err != nil {
				return nil, nil, err
			}
			tun.mu.Lock()
			tun.leases++
			tun.mu.Unlock()
			tun.mu.Lock()
			tun.active++
			tun.mu.Unlock()
			release := func() {
				lease.Release()
				tun.mu.Lock()
				tun.active--
				tun.mu.Unlock()
			}
			return &ops.Session{Name: name, Target: tgt, SQL: rds, Bucket: jobObjects{s3}}, release, nil
		}
	}
	return a, tun, rds
}

func TestTunnelModeJobOpensOneTunnel(t *testing.T) {
	a, tun, _ := tunnelJobApp(t)
	code, out, errOut := run(a, "import", localFile(t, "a.bak", "a"), localFile(t, "b.bak", "b"), localFile(t, "c.bak", "c"),
		"--db", "a,b,c", "--apply", "--interval", "1ms")
	if code != 0 {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	// The pre-flight and the restore, wait and post of three databases:
	// ten sessions, one tunnel, closed with the job.
	if tun.opens != 1 || tun.closes != 1 || tun.leases != 10 || tun.active != 0 {
		t.Fatalf("%d tunnel(s) opened, %d closed, %d sessions, %d not released", tun.opens, tun.closes, tun.leases, tun.active)
	}
}

func TestTunnelModeJobFailureReleasesTheTunnel(t *testing.T) {
	a, tun, rds := tunnelJobApp(t)
	rds.end = mssql.Error
	code, _, _ := run(a, "import", localFile(t, "a.bak", "a"), localFile(t, "b.bak", "b"),
		"--db", "a,b", "--apply", "--interval", "1ms")
	if code == 0 {
		t.Fatal("a failed restore exited 0")
	}
	if tun.opens != 1 || tun.closes != 1 || tun.active != 0 {
		t.Fatalf("%d tunnel(s) opened, %d closed, %d not released", tun.opens, tun.closes, tun.active)
	}
}
