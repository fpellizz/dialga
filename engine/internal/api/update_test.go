package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/decisyon/dialga/engine/internal/update"
)

func updateServer(t *testing.T, env map[string]string) (http.Handler, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	gh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","published_at":"2026-10-20T10:00:00Z"}`))
	}))
	t.Cleanup(gh.Close)
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, Version: "1.0.0",
		Getenv: func(k string) string { return env[k] },
		Updates: &update.Checker{
			Version: "1.0.0", Endpoint: gh.URL, Client: gh.Client(),
			CacheFile: filepath.Join(t.TempDir(), "u.json"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), &hits
}

func TestUpdateEndpoint(t *testing.T) {
	h, hits := updateServer(t, nil)
	get := func(path string) update.Info {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var in update.Info
		if err := json.Unmarshal(rec.Body.Bytes(), &in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	in := get("/api/v1/update")
	if !in.Newer || in.Latest != "9.9.9" || in.Current != "1.0.0" || in.URL == "" {
		t.Fatalf("%+v", in)
	}
	get("/api/v1/update")
	if hits.Load() != 1 {
		t.Fatalf("cache not used: %d", hits.Load())
	}
	get("/api/v1/update?force=1")
	if hits.Load() != 2 {
		t.Fatalf("force ignored: %d", hits.Load())
	}
}

func TestUpdateEndpointDisabledAndGuarded(t *testing.T) {
	h, hits := updateServer(t, map[string]string{UpdateDisabledEnv: "1"})
	rec := do(h, http.MethodGet, "/api/v1/update?force=1")
	var in update.Info
	_ = json.Unmarshal(rec.Body.Bytes(), &in)
	if rec.Code != 200 || in.Newer || in.Error == "" || hits.Load() != 0 {
		t.Fatalf("%d %+v hits %d", rec.Code, in, hits.Load())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/update", nil)
	req.Host = "127.0.0.1:5000"
	r2 := httptest.NewRecorder()
	h.ServeHTTP(r2, req)
	if r2.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r2.Code)
	}
}
