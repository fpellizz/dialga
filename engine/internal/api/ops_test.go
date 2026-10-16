package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// fakeInstance is the SQL Server of a target in memory.
type fakeInstance struct {
	states   map[string]string
	active   int
	ran      []string
	progress []mssql.Lifecycle
	reads    int
	report   mssql.DatabaseReport
}

func (f *fakeInstance) DatabaseState(_ context.Context, db string) (string, error) {
	return f.states[db], nil
}

func (f *fakeInstance) ActiveTasks(context.Context, string) (int, error) { return f.active, nil }

func (f *fakeInstance) ActiveSessions(context.Context, string) (int, error) { return 0, nil }

func (f *fakeInstance) StartTask(_ context.Context, stmt, db string) (mssql.Task, error) {
	f.ran = append(f.ran, stmt)
	return mssql.Task{ID: 9, Type: "RESTORE_DB", Database: db, Lifecycle: mssql.Created}, nil
}

func (f *fakeInstance) Exec(_ context.Context, stmt string) error {
	f.ran = append(f.ran, stmt)
	return nil
}

func (f *fakeInstance) DatabaseReport(_ context.Context, db string) (mssql.DatabaseReport, error) {
	if f.states[db] == "" {
		return mssql.DatabaseReport{}, fmt.Errorf("%s: %w", db, mssql.ErrNoDatabase)
	}
	return f.report, nil
}

func (f *fakeInstance) List(context.Context, mssql.TaskFilter) ([]mssql.Task, error) { return nil, nil }

func (f *fakeInstance) Get(_ context.Context, id int) (mssql.Task, error) {
	l := mssql.InProgress
	if len(f.progress) > 0 {
		l = f.progress[min(f.reads, len(f.progress)-1)]
		f.reads++
	}
	return mssql.Task{ID: id, Type: "RESTORE_DB", Database: "sales", Lifecycle: l}, nil
}

type fakeObjects map[string]int64

func (b fakeObjects) Size(_ context.Context, key string) (int64, error) {
	if size, ok := b[key]; ok {
		return size, nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b fakeObjects) Keys(context.Context, string) ([]string, error) { return nil, nil }

// opsServer serves configDoc (nil: no file) with the target opened on f.
type opsServer struct {
	h        http.Handler
	password string
	opened   bool
	openErr  error
}

func newOpsServer(t *testing.T, configDoc *string, env map[string]string, f *fakeInstance, objects fakeObjects) *opsServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := &opsServer{}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path,
		Getenv: func(k string) string { return env[k] }, TaskInterval: time.Millisecond,
		OpenOps: func(_ context.Context, name string, tgt config.Target, password string) (*ops.Session, func(), error) {
			srv.opened, srv.password = true, password
			if srv.openErr != nil {
				return nil, nil, srv.openErr
			}
			return &ops.Session{Name: name, Target: tgt, SQL: f, Bucket: objects,
				Now: func() time.Time { return time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC) }}, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.h = s.Handler()
	return srv
}

func doJSON(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var testEnv = map[string]string{"TEST_SQL_PASSWORD": "s3cret"}

func TestPreviewPlan(t *testing.T) {
	doc := testConfig
	f := &fakeInstance{}
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{"import/sales.bak": 1})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/restore", `{"database":"sales","key":"sales.bak"}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	want := mssql.RestoreDatabase("sales", mssql.S3ARN("dev-sqlserver-backups", "import/sales.bak"), mssql.RestoreOptions{})
	if !p.Ready || len(p.Checks) != 3 || p.Confirmation != "" || len(p.Digest) != 32 || p.Request.Key != "import/sales.bak" ||
		p.Plan.Actions[0].Statement != want || !strings.HasPrefix(p.Preview, "Dry run: restore on dev-sql") {
		t.Fatalf("preview %+v", p)
	}
	if len(f.ran) != 0 || srv.password != "s3cret" {
		t.Fatalf("ran %v, password %q", f.ran, srv.password)
	}
	// A failed check is an answer, not an error.
	f.states = map[string]string{"sales": "ONLINE"}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/restore", `{"database":"sales","key":"sales.bak"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Ready {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestPreviewPlanResolvesTheExportKey(t *testing.T) {
	doc := testConfig
	srv := newOpsServer(t, &doc, testEnv, &fakeInstance{states: map[string]string{"sales": "ONLINE"}}, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/export", `{"database":"sales"}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Request.Key != "export/sales_20261016-083000.bak" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestPlanErrors(t *testing.T) {
	doc := testConfig
	restore := `{"database":"sales","key":"sales.bak"}`
	tests := []struct {
		name    string
		doc     *string
		env     map[string]string
		openErr error
		path    string
		body    string
		want    int
		opened  bool
	}{
		{"unknown operation", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/rename", restore, http.StatusNotFound, false},
		{"unknown target", &doc, testEnv, nil, "/api/v1/targets/nope/plans/restore", restore, http.StatusNotFound, false},
		{"bad JSON", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/restore", `{"database":`, http.StatusBadRequest, false},
		{"unknown field", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/restore", `{"db":"sales"}`, http.StatusBadRequest, false},
		{"two objects", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/drop", `{"database":"a"}{}`, http.StatusBadRequest, false},
		{"bad database", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/drop", `{"database":"x;drop"}`, http.StatusBadRequest, false},
		{"other bucket", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/restore", `{"database":"s","key":"s3://x/a.bak"}`, http.StatusBadRequest, false},
		{"no task id", &doc, testEnv, nil, "/api/v1/targets/dev-sql/plans/cancel", `{}`, http.StatusBadRequest, false},
		{"missing config", nil, testEnv, nil, "/api/v1/targets/dev-sql/plans/restore", restore, http.StatusConflict, false},
		{"no password", &doc, nil, nil, "/api/v1/targets/dev-sql/plans/restore", restore, http.StatusUnprocessableEntity, false},
		{"session fails", &doc, testEnv, errors.New("nothing listening"), "/api/v1/targets/dev-sql/plans/restore", restore, http.StatusBadGateway, true},
	}
	for _, tc := range tests {
		for _, suffix := range []string{"", "/execute"} {
			t.Run(tc.name+suffix, func(t *testing.T) {
				srv := newOpsServer(t, tc.doc, tc.env, &fakeInstance{}, fakeObjects{})
				srv.openErr = tc.openErr
				if rec := doJSON(srv.h, http.MethodPost, tc.path+suffix, tc.body); rec.Code != tc.want || srv.opened != tc.opened {
					t.Fatalf("status %d (want %d), opened %v (body %s)", rec.Code, tc.want, srv.opened, rec.Body)
				}
			})
		}
	}
}

// events splits an SSE body into "event" names and their data.
func events(t *testing.T, body string) (names []string, data []string) {
	t.Helper()
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		name, rest, ok := strings.Cut(block, "\n")
		if !ok || !strings.HasPrefix(name, "event: ") || !strings.HasPrefix(rest, "data: ") {
			t.Fatalf("bad event block %q", block)
		}
		names = append(names, strings.TrimPrefix(name, "event: "))
		data = append(data, strings.TrimPrefix(rest, "data: "))
	}
	return names, data
}

func TestExecutePlanStreams(t *testing.T) {
	doc := testConfig
	f := &fakeInstance{active: 1, progress: []mssql.Lifecycle{mssql.Created, mssql.Success}}
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{"import/sales.bak": 1})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/restore/execute", `{"database":"sales","key":"sales.bak"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	names, data := events(t, rec.Body.String())
	if got := strings.Join(names, " "); got != "notice step step step task task step done" {
		t.Fatalf("events %s\n%s", got, rec.Body)
	}
	var done PlanDone
	if json.Unmarshal([]byte(data[len(data)-1]), &done) != nil || !done.OK || done.Task == nil || done.Task.Lifecycle != mssql.Success {
		t.Fatalf("done %s", data[len(data)-1])
	}
	if !strings.Contains(data[0], "RDS runs two at a time") || len(f.ran) != 1 {
		t.Fatalf("notice %s, ran %v", data[0], f.ran)
	}
}

func TestExecutePlanTaskErrorAndNoWait(t *testing.T) {
	doc := testConfig
	f := &fakeInstance{states: map[string]string{"sales": "ONLINE"}, progress: []mssql.Lifecycle{mssql.Error}}
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/export/execute", `{"database":"sales"}`)
	_, data := events(t, rec.Body.String())
	var done PlanDone
	if json.Unmarshal([]byte(data[len(data)-1]), &done) != nil || done.OK || done.Task.Lifecycle != mssql.Error {
		t.Fatalf("task error: %s", rec.Body)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/export/execute", `{"database":"sales","noWait":true}`)
	names, data := events(t, rec.Body.String())
	if strings.Join(names, " ") != "step step done" || json.Unmarshal([]byte(data[2]), &done) != nil || !done.OK || done.Task.ID != 9 {
		t.Fatalf("no wait: %s", rec.Body)
	}
}

func TestExecutePlanGuards(t *testing.T) {
	doc := testConfig
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	online := map[string]string{"sales": "ONLINE"}
	tests := []struct {
		name string
		doc  *string
		op   string
		body string
		want int
	}{
		{"pre-flight failed", &doc, "finish", `{"database":"sales"}`, http.StatusPreconditionFailed},
		{"plan changed", &doc, "drop", `{"database":"sales","confirm":"sales","digest":"0123"}`, http.StatusPreconditionFailed},
		{"drop without confirm", &doc, "drop", `{"database":"sales"}`, http.StatusUnprocessableEntity},
		{"drop wrong confirm", &doc, "drop", `{"database":"sales","confirm":"Sales"}`, http.StatusUnprocessableEntity},
		{"drop confirmed", &doc, "drop", `{"database":"sales","confirm":"sales"}`, http.StatusOK},
		{"protected wants the target", &protected, "drop", `{"database":"sales","confirm":"sales"}`, http.StatusUnprocessableEntity},
		{"protected confirmed", &protected, "drop", `{"database":"sales","confirm":"dev-sql"}`, http.StatusOK},
		{"protected export", &protected, "export", `{"database":"sales","noWait":true}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInstance{states: online}
			srv := newOpsServer(t, tc.doc, testEnv, f, fakeObjects{})
			rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/"+tc.op+"/execute", tc.body)
			if rec.Code != tc.want || (len(f.ran) == 1) != (tc.want == http.StatusOK) {
				t.Fatalf("status %d (want %d), ran %v, body %s", rec.Code, tc.want, f.ran, rec.Body)
			}
		})
	}
}

func TestExecuteWhatWasPreviewed(t *testing.T) {
	doc := testConfig
	f := &fakeInstance{states: map[string]string{"sales": "ONLINE"}}
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/drop", `{"database":"sales"}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Confirmation != "sales" {
		t.Fatalf("preview: status %d, body %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(ExecuteRequest{Database: p.Request.Database, Confirm: p.Confirmation, Digest: p.Digest})
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/drop/execute", string(body))
	names, _ := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "step step done" || len(f.ran) != 1 || f.ran[0] != mssql.DropDatabase("sales") {
		t.Fatalf("execute: status %d, ran %v, body %s", rec.Code, f.ran, rec.Body)
	}
}

func TestDatabaseReport(t *testing.T) {
	doc := testConfig
	f := &fakeInstance{states: map[string]string{"sales": "ONLINE"}, report: mssql.DatabaseReport{
		Database: mssql.DatabaseInfo{Name: "sales", State: "ONLINE"}, Access: true,
		Files: []mssql.DatabaseFile{}, OrphanUsers: []string{"app"}, Objects: []mssql.ObjectCount{{Type: "VIEW", Count: 2}},
	}}
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/databases/sales/report", "")
	var r ops.Report
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if !r.OK || r.Target != "dev-sql" || len(r.Findings) != 4 || r.OrphanUsers[0] != "app" || r.Objects[0].Count != 2 {
		t.Fatalf("report %+v", r)
	}
	for path, want := range map[string]int{
		"/api/v1/targets/dev-sql/databases/nope/report":   http.StatusNotFound,
		"/api/v1/targets/dev-sql/databases/tempdb/report": http.StatusBadRequest,
		"/api/v1/targets/other/databases/sales/report":    http.StatusNotFound,
	} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}
