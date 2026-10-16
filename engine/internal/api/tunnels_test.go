package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnel"
	"github.com/decisyon/dialga/engine/internal/tunnels"
)

// tunnelConfig has a tunnel-mode target (dev-sql) and an external one (ext-sql).
var tunnelConfig = strings.Replace(testConfig, "mode: external, local_port: 11433", "mode: tunnel, local_port: 41433", 1) +
	strings.ReplaceAll(strings.SplitN(testConfig, "targets:\n", 2)[1], "dev-sql:", "ext-sql:")

// fakeTunnels opens fake tunnels that report connecting then ready.
type fakeTunnels struct {
	mu     sync.Mutex
	err    error
	opened int
	closed int
}

type fakeAPITunnel struct{ f *fakeTunnels }

func (t fakeAPITunnel) Addr() string { return "127.0.0.1:41433" }

func (t fakeAPITunnel) Close() error {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.closed++
	return nil
}

func (f *fakeTunnels) open(_ context.Context, t config.Target, onState func(tunnel.State)) (tunnels.Tunnel, error) {
	onState(tunnel.State{Status: tunnel.Connecting, Time: time.Now(), LocalPort: t.Connect.LocalPort, Endpoint: "db.example:1433"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.opened++
	onState(tunnel.State{Status: tunnel.Ready, Time: time.Now(), LocalPort: t.Connect.LocalPort, Endpoint: "db.example:1433",
		Bridge: "i-1", Detail: "127.0.0.1:41433 → db.example:1433 via bridge i-1"})
	return fakeAPITunnel{f}, nil
}

func (f *fakeTunnels) counts() (opened, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened, f.closed
}

func newTunnelServer(t *testing.T, configDoc *string) (*Server, *fakeTunnels, *fakeCheck) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, fake := &fakeTunnels{}, &fakeCheck{}
	m := tunnels.New(tunnels.Options{Open: f.open})
	t.Cleanup(func() { _ = m.Close() })
	s, err := New(Options{Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path, Tunnels: m,
		Getenv: func(string) string { return "" }, RunCheck: fake.run})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, fake
}

func decodeTunnel(t *testing.T, rec *httptest.ResponseRecorder) TunnelState {
	t.Helper()
	var st TunnelState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	return st
}

func TestTunnelLifecycle(t *testing.T) {
	doc := tunnelConfig
	s, f, _ := newTunnelServer(t, &doc)
	h := s.Handler()
	if st := decodeTunnel(t, do(h, http.MethodGet, "/api/v1/targets/dev-sql/tunnel")); st.Status != "closed" || st.Since != nil || st.Target != "dev-sql" {
		t.Fatalf("before: %+v", st)
	}
	st := decodeTunnel(t, do(h, http.MethodPost, "/api/v1/targets/dev-sql/tunnel"))
	if st.Status != "ready" || !st.KeepOpen || st.LocalPort != 41433 || st.Bridge != "i-1" || st.Since == nil {
		t.Fatalf("after POST: %+v", st)
	}
	var list TunnelList
	rec := do(h, http.MethodGet, "/api/v1/tunnels")
	if json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Tunnels) != 1 || list.Tunnels[0].Target != "dev-sql" || list.Tunnels[0].Status != "ready" {
		t.Fatalf("list: %s", rec.Body)
	}
	decodeTunnel(t, do(h, http.MethodPost, "/api/v1/targets/dev-sql/tunnel")) // already open: same tunnel
	if st := decodeTunnel(t, do(h, http.MethodDelete, "/api/v1/targets/dev-sql/tunnel")); st.Status != "closed" || st.Detail != "closed on request" {
		t.Fatalf("after DELETE: %+v", st)
	}
	if opened, closed := f.counts(); opened != 1 || closed != 1 {
		t.Fatalf("opened %d, closed %d", opened, closed)
	}
}

func TestTunnelErrors(t *testing.T) {
	doc := tunnelConfig
	s, f, _ := newTunnelServer(t, &doc)
	h := s.Handler()
	f.err = errors.New("no instance tagged Name=ssm-bridge")
	tests := []struct {
		method, path string
		want         int
		body         string
	}{
		{http.MethodPost, "/api/v1/targets/dev-sql/tunnel", http.StatusBadGateway, "no instance tagged"},
		{http.MethodGet, "/api/v1/targets/ext-sql/tunnel", http.StatusUnprocessableEntity, "connect.mode of ext-sql is external"},
		{http.MethodPost, "/api/v1/targets/ext-sql/tunnel", http.StatusUnprocessableEntity, ""},
		{http.MethodGet, "/api/v1/targets/ext-sql/tunnel/events", http.StatusUnprocessableEntity, ""},
		{http.MethodDelete, "/api/v1/targets/nope/tunnel", http.StatusNotFound, ""},
	}
	for _, tc := range tests {
		if rec := do(h, tc.method, tc.path); rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("%s %s: status %d, body %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	if st := decodeTunnel(t, do(h, http.MethodGet, "/api/v1/targets/dev-sql/tunnel")); st.Status != "closed" || !strings.Contains(st.Detail, "no instance tagged") {
		t.Fatalf("failure not reported: %+v", st)
	}
	s, _, _ = newTunnelServer(t, nil)
	if rec := do(s.Handler(), http.MethodGet, "/api/v1/tunnels"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"tunnels":[]}` {
		t.Fatalf("no configuration: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := do(s.Handler(), http.MethodGet, "/api/v1/targets/dev-sql/tunnel"); rec.Code != http.StatusConflict {
		t.Fatalf("no configuration, one tunnel: status %d", rec.Code)
	}
}

func TestTunnelEvents(t *testing.T) {
	doc := tunnelConfig
	s, _, _ := newTunnelServer(t, &doc)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/targets/dev-sql/tunnel/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := bufio.NewScanner(resp.Body)
	next := func() TunnelState {
		t.Helper()
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				var st TunnelState
				if err := json.Unmarshal([]byte(data), &st); err != nil {
					t.Fatal(err)
				}
				return st
			}
		}
		t.Fatalf("stream ended: %v", lines.Err())
		return TunnelState{}
	}
	if st := next(); st.Status != "closed" {
		t.Fatalf("first event %+v", st)
	}
	go func() {
		openReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/targets/dev-sql/tunnel", bytes.NewReader(nil))
		openReq.Header.Set("Authorization", "Bearer "+testToken)
		if r, err := http.DefaultClient.Do(openReq); err == nil {
			_ = r.Body.Close()
		}
	}()
	seen := map[string]bool{}
	for !seen["ready"] {
		seen[next().Status] = true
	}
	if !seen["connecting"] {
		t.Fatalf("no connecting event before ready: %v", seen)
	}
	// The engine stops: the stream ends with the closed state.
	_ = s.opts.Tunnels.Close()
	st := next()
	for st.Status == "ready" {
		st = next()
	}
	if st.Status != "closed" || st.Detail != "the engine stopped" {
		t.Fatalf("last event %+v", st)
	}
	for lines.Scan() { // ends at EOF: the handler returned
		if strings.HasPrefix(lines.Text(), "data:") {
			t.Fatalf("the stream went on: %q", lines.Text())
		}
	}
}

func TestCheckBorrowsTheSharedTunnel(t *testing.T) {
	doc := tunnelConfig
	s, f, fake := newTunnelServer(t, &doc)
	h := s.Handler()
	do(h, http.MethodPost, "/api/v1/targets/dev-sql/checks")
	if !fake.req.Tunnel || fake.req.Lender == nil {
		t.Fatalf("tunnel target: request %+v", fake.req)
	}
	lease, err := fake.req.Lender.Lend(context.Background())
	if err != nil || lease.Addr != "127.0.0.1:41433" || lease.Bridge != "i-1" {
		t.Fatalf("Lend: %+v, %v", lease, err)
	}
	lease.Release()
	if opened, _ := f.counts(); opened != 1 {
		t.Fatalf("opened %d tunnels", opened)
	}
	do(h, http.MethodPost, "/api/v1/targets/ext-sql/checks")
	if fake.req.Tunnel || fake.req.Lender != nil {
		t.Fatalf("external target: request %+v", fake.req)
	}
}

func TestServeClosesTheTunnels(t *testing.T) {
	doc := tunnelConfig
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTunnels{}
	m := tunnels.New(tunnels.Options{Open: f.open})
	cfg, _ := config.Parse([]byte(doc))
	if _, err := m.Open(context.Background(), cfg.Targets["dev-sql"]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var ready bytes.Buffer
	go func() {
		done <- Serve(ctx, Options{Token: testToken, ConfigPath: path, Tunnels: m,
			RunCheck: func(context.Context, check.Request, string, check.Emitter) bool { return true }}, &ready)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); closed != 1 {
		t.Fatalf("closed %d tunnels at shutdown", closed)
	}
}
