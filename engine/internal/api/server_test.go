package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// testToken is built, not written as a literal, so secret scanners do not
// mistake this throwaway test value for a real API key.
var testToken = strings.Repeat("0123456789abcdef", 2)

const testConfig = `apiVersion: v1
targets:
  dev-sql:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: default
    instance: dev-sqlserver
    bucket: dev-sqlserver-backups
    sql: { user: admin, password: "env:TEST_SQL_PASSWORD" }
    connect: { mode: external, local_port: 11433 }
`

type fakeCheck struct {
	req      check.Request
	password string
}

func (f *fakeCheck) run(_ context.Context, req check.Request, password string, emit check.Emitter) bool {
	f.req, f.password = req, password
	emit(check.Event{Step: "aws", Label: "AWS credentials", Status: check.Running})
	emit(check.Event{Step: "aws", Label: "AWS credentials", Status: check.OK})
	return true
}

// newTestServer serves configDoc (nil: no file) with a faked check.
func newTestServer(t *testing.T, configDoc *string, env map[string]string) (http.Handler, *fakeCheck) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeCheck{}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, Version: "test", ConfigPath: path,
		Getenv: func(k string) string { return env[k] }, RunCheck: fake.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), fake
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuard(t *testing.T) {
	doc := testConfig
	h, _ := newTestServer(t, &doc, nil)
	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		auth       string
		wantStatus int
		wantCORS   string
	}{
		{"health with token", http.MethodGet, "127.0.0.1:5000", "", "Bearer " + testToken, http.StatusOK, ""},
		{"localhost host", http.MethodGet, "localhost:5000", "", "Bearer " + testToken, http.StatusOK, ""},
		{"tauri origin", http.MethodGet, "127.0.0.1:5000", "tauri://localhost", "Bearer " + testToken, http.StatusOK, "tauri://localhost"},
		{"missing token", http.MethodGet, "127.0.0.1:5000", "", "", http.StatusUnauthorized, ""},
		{"wrong token", http.MethodGet, "127.0.0.1:5000", "", "Bearer " + strings.Repeat("x", 32), http.StatusUnauthorized, ""},
		{"rebinding host", http.MethodGet, "evil.example:5000", "", "Bearer " + testToken, http.StatusForbidden, ""},
		{"foreign origin", http.MethodGet, "127.0.0.1:5000", "https://evil.example", "Bearer " + testToken, http.StatusForbidden, ""},
		{"preflight allowed", http.MethodOptions, "127.0.0.1:5000", "tauri://localhost", "", http.StatusNoContent, "tauri://localhost"},
		{"preflight foreign", http.MethodOptions, "127.0.0.1:5000", "https://evil.example", "", http.StatusForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/health", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantCORS {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantCORS)
			}
		})
	}
}

func TestListTargets(t *testing.T) {
	doc := testConfig
	h, _ := newTestServer(t, &doc, nil)
	rec := do(h, http.MethodGet, "/api/v1/targets")
	var list TargetList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(list.Targets) != 1 || list.Targets[0].Name != "dev-sql" || list.Targets[0].AccountID != "123456789012" || list.Error != "" {
		t.Fatalf("list %+v", list)
	}
	if strings.Contains(rec.Body.String(), "TEST_SQL_PASSWORD") {
		t.Fatal("the password reference leaked into the target list")
	}
}

func TestListTargetsFirstRunAndInvalid(t *testing.T) {
	h, _ := newTestServer(t, nil, nil)
	var list TargetList
	rec := do(h, http.MethodGet, "/api/v1/targets")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Targets) != 0 || list.Error != "" || list.ConfigPath == "" {
		t.Fatalf("first run: status %d, body %s", rec.Code, rec.Body)
	}
	bad := strings.Replace(testConfig, "mode: external", "mode: vpn", 1)
	h, _ = newTestServer(t, &bad, nil)
	rec = do(h, http.MethodGet, "/api/v1/targets")
	if json.Unmarshal(rec.Body.Bytes(), &list) != nil || !strings.Contains(list.Error, "connect.mode") {
		t.Fatalf("invalid config: body %s", rec.Body)
	}
}

func TestCheckTargetStreams(t *testing.T) {
	doc := testConfig
	h, fake := newTestServer(t, &doc, map[string]string{"TEST_SQL_PASSWORD": "s3cret"})
	rec := do(h, http.MethodPost, "/api/v1/targets/dev-sql/checks")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "event: step") || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("status %d, body %s", rec.Code, body)
	}
	if strings.Contains(body, "event: notice") || fake.password != "s3cret" || fake.req.ExpectedAccount != "123456789012" {
		t.Fatalf("password %q, request %+v, body %s", fake.password, fake.req, body)
	}
}

func TestCheckTargetTunnelMode(t *testing.T) {
	tunnel := strings.Replace(testConfig, "mode: external, local_port: 11433", "mode: tunnel", 1)
	h, fake := newTestServer(t, &tunnel, map[string]string{"TEST_SQL_PASSWORD": "s3cret"})
	rec := do(h, http.MethodPost, "/api/v1/targets/dev-sql/checks")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !fake.req.Tunnel || fake.req.LocalPort != 0 {
		t.Fatalf("status %d, request %+v, body %s", rec.Code, fake.req, rec.Body)
	}
}

func TestCheckTargetNoticeWithoutPassword(t *testing.T) {
	doc := testConfig
	h, fake := newTestServer(t, &doc, nil)
	rec := do(h, http.MethodPost, "/api/v1/targets/dev-sql/checks")
	if !strings.Contains(rec.Body.String(), "event: notice") || fake.password != "" {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestCheckTargetErrors(t *testing.T) {
	doc := testConfig
	h, _ := newTestServer(t, &doc, nil)
	if rec := do(h, http.MethodPost, "/api/v1/targets/nope/checks"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown target: status %d", rec.Code)
	}
	h, _ = newTestServer(t, nil, nil)
	if rec := do(h, http.MethodPost, "/api/v1/targets/dev-sql/checks"); rec.Code != http.StatusConflict {
		t.Fatalf("missing config: status %d", rec.Code)
	}
}

// fakeTasks records the session and filter and serves a fixed list.
type fakeTasks struct {
	password string
	filter   mssql.TaskFilter
	openErr  error
}

func (f *fakeTasks) open(_ context.Context, _ config.Target, password string) (mssql.TaskReader, func(), error) {
	f.password = password
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f, func() {}, nil
}

func (f *fakeTasks) List(_ context.Context, filter mssql.TaskFilter) ([]mssql.Task, error) {
	f.filter = filter
	return []mssql.Task{{ID: 4, Type: "RESTORE_DB", Database: "sales", Lifecycle: mssql.InProgress, Percent: 30}}, nil
}

func (f *fakeTasks) Get(context.Context, int) (mssql.Task, error) { return mssql.Task{}, nil }

func newTasksServer(t *testing.T, configDoc *string, env map[string]string, fake *fakeTasks) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path,
		Getenv: func(k string) string { return env[k] }, OpenTasks: fake.open,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestListTasks(t *testing.T) {
	doc := testConfig
	fake := &fakeTasks{}
	h := newTasksServer(t, &doc, map[string]string{"TEST_SQL_PASSWORD": "s3cret"}, fake)
	rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/tasks?db=sales&limit=5")
	var list TaskList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].Lifecycle != mssql.InProgress || fake.password != "s3cret" ||
		fake.filter != (mssql.TaskFilter{Database: "sales", Limit: 5}) {
		t.Fatalf("list %+v, password %q, filter %+v", list, fake.password, fake.filter)
	}
}

func TestListTasksTunnelMode(t *testing.T) {
	tunnel := strings.Replace(testConfig, "mode: external", "mode: tunnel", 1)
	fake := &fakeTasks{}
	h := newTasksServer(t, &tunnel, map[string]string{"TEST_SQL_PASSWORD": "s3cret"}, fake)
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/tasks"); rec.Code != http.StatusOK || fake.password != "s3cret" {
		t.Fatalf("status %d, password %q, body %s", rec.Code, fake.password, rec.Body)
	}
}

func TestListTasksErrors(t *testing.T) {
	doc := testConfig
	env := map[string]string{"TEST_SQL_PASSWORD": "s3cret"}
	tests := []struct {
		name string
		doc  *string
		env  map[string]string
		fake *fakeTasks
		path string
		want int
	}{
		{"unknown target", &doc, env, &fakeTasks{}, "/api/v1/targets/nope/tasks", http.StatusNotFound},
		{"bad database", &doc, env, &fakeTasks{}, "/api/v1/targets/dev-sql/tasks?db=x%27drop", http.StatusBadRequest},
		{"bad limit", &doc, env, &fakeTasks{}, "/api/v1/targets/dev-sql/tasks?limit=500", http.StatusBadRequest},
		{"no password", &doc, nil, &fakeTasks{}, "/api/v1/targets/dev-sql/tasks", http.StatusUnprocessableEntity},
		{"session fails", &doc, env, &fakeTasks{openErr: errors.New("nothing listening")}, "/api/v1/targets/dev-sql/tasks", http.StatusBadGateway},
		{"missing config", nil, env, &fakeTasks{}, "/api/v1/targets/dev-sql/tasks", http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTasksServer(t, tc.doc, tc.env, tc.fake)
			if rec := do(h, http.MethodGet, tc.path); rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestNewRejectsShortToken(t *testing.T) {
	if _, err := New(Options{Token: "short"}); err == nil {
		t.Fatal("New accepted a short token")
	}
}
