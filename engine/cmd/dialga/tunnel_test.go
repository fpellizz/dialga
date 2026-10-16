package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

// fakeTunnel has already ended (as after Ctrl-C); Close reports closed.
type fakeTunnel struct {
	done     chan struct{}
	onState  func(tunnel.State)
	closeErr error
	closed   int
}

func (f *fakeTunnel) Done() <-chan struct{} { return f.done }

func (f *fakeTunnel) Close() error {
	f.closed++
	f.onState(tunnel.State{Status: tunnel.Closed, Time: time.Now(), LocalPort: 41433, Endpoint: "db.example.rds.amazonaws.com:1433"})
	return f.closeErr
}

// withTunnel makes a open fake tunnels; port reports the local port asked for.
func withTunnel(a *app, f *fakeTunnel, openErr error) (port *int) {
	port = new(int)
	*port = -1
	a.openTunnel = func(_ context.Context, _ config.Target, p int, onState func(tunnel.State)) (tunnelHandle, error) {
		*port = p
		if openErr != nil {
			return nil, openErr
		}
		now := time.Now()
		onState(tunnel.State{Status: tunnel.Connecting, Time: now, LocalPort: 41433, Endpoint: "db.example.rds.amazonaws.com:1433",
			Detail: "looking for the bridge (Name=ssm-bridge) in vpc-a"})
		onState(tunnel.State{Status: tunnel.Ready, Time: now, LocalPort: 41433, Endpoint: "db.example.rds.amazonaws.com:1433",
			Bridge: "i-1", Detail: "127.0.0.1:41433 → db.example.rds.amazonaws.com:1433 via bridge i-1"})
		f.onState = onState
		f.done = make(chan struct{})
		close(f.done)
		return f, nil
	}
	return port
}

func TestTunnelText(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeTunnel{}
	port := withTunnel(a, f, nil)
	code, out, _ := run(a, "tunnel")
	if code != 0 || *port != 11433 || f.closed != 1 {
		t.Fatalf("code %d, port %d, closed %d:\n%s", code, *port, f.closed, out)
	}
	for _, want := range []string{"Opening the tunnel to dev-sql: instance dev-sqlserver, profile default, eu-west-1",
		"connecting   looking for the bridge", "ready        127.0.0.1:41433 → db.example.rds.amazonaws.com:1433 via bridge i-1",
		"SQL clients: server 127.0.0.1,41433; the certificate is issued for db.example.rds.amazonaws.com.", "] closed\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestTunnelJSONWithPort(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	port := withTunnel(a, &fakeTunnel{}, nil)
	code, out, _ := run(a, "tunnel", "dev-sql", "--port", "0", "-o", "json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || *port != 0 || len(lines) != 3 {
		t.Fatalf("code %d, port %d:\n%s", code, *port, out)
	}
	for i, want := range []tunnel.Status{tunnel.Connecting, tunnel.Ready, tunnel.Closed} {
		var doc struct {
			Target    string        `json:"target"`
			Status    tunnel.Status `json:"status"`
			LocalPort int           `json:"localPort"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &doc); err != nil || doc.Target != "dev-sql" || doc.Status != want || doc.LocalPort != 41433 {
			t.Fatalf("line %d %q: %+v, %v", i, lines[i], doc, err)
		}
	}
}

func TestTunnelErrors(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	port := withTunnel(a, &fakeTunnel{}, nil)
	if code, _, errOut := run(a, "tunnel", "--port", "70000"); code != exitFailure || *port != -1 || !strings.Contains(errOut, "--port") {
		t.Fatalf("bad port: code %d, stderr %q", code, errOut)
	}
	withTunnel(a, nil, errors.New("no instance tagged Name=ssm-bridge"))
	if code, _, errOut := run(a, "tunnel"); code != exitFailure || !strings.Contains(errOut, "dialga: no instance tagged") {
		t.Fatalf("open fails: code %d, stderr %q", code, errOut)
	}
	withTunnel(a, &fakeTunnel{closeErr: errors.New("ssm TerminateSession s-1: throttled")}, nil)
	if code, _, errOut := run(a, "tunnel"); code != exitFailure || !strings.Contains(errOut, "closing the tunnel: ssm TerminateSession") {
		t.Fatalf("close fails: code %d, stderr %q", code, errOut)
	}
}
