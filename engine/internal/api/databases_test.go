package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/session"
)

// fakeDatabases records the session and serves fixed databases and usage.
type fakeDatabases struct {
	password string
	opened   bool
	openErr  error
	queryErr error
	usage    mssql.Usage
}

func (f *fakeDatabases) open(_ context.Context, _ config.Target, password string) (mssql.DatabaseReader, func(), error) {
	f.opened, f.password = true, password
	if f.openErr != nil {
		return nil, nil, f.openErr
	}
	return f, func() {}, nil
}

func (f *fakeDatabases) Databases(context.Context) ([]mssql.Database, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return []mssql.Database{{Name: "sales", State: "ONLINE", Recovery: "FULL", Compatibility: 140,
		Collation: "SQL_Latin1_General_CP1_CI_AS", Owner: "rdsa", UserAccess: "MULTI_USER",
		CreatedAt: "2026-10-15T13:05:40", DataMB: 16, LogMB: 8.5}}, nil
}

func (f *fakeDatabases) Usage(context.Context) (mssql.Usage, error) {
	if f.queryErr != nil {
		return mssql.Usage{}, f.queryErr
	}
	return f.usage, nil
}

// fakeDescribe answers a fixed instance, or err.
type fakeDescribe struct {
	info session.InstanceInfo
	err  error
}

func (f fakeDescribe) describe(context.Context, config.Target) (session.InstanceInfo, error) {
	return f.info, f.err
}

var sampleInstance = session.InstanceInfo{
	Instance: awsx.Instance{Status: "available", Engine: "sqlserver-se", EngineVersion: "14.00.3465.1.v1",
		Class: "db.m6i.large", AllocatedGiB: 200, MaxAllocatedGiB: 1000,
		BackupWindow: "04:29-04:59", MaintenanceWindow: "sun:05:00-sun:05:30"},
	FreeBytes: 104_475_983_462.4,
}

func newDatabasesServer(t *testing.T, configDoc *string, env map[string]string, dbs *fakeDatabases, desc fakeDescribe) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if configDoc != nil {
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{
		Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path,
		Getenv:        func(k string) string { return env[k] },
		OpenDatabases: dbs.open, DescribeTarget: desc.describe,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestListDatabases(t *testing.T) {
	doc := testConfig
	fake := &fakeDatabases{}
	h := newDatabasesServer(t, &doc, map[string]string{"TEST_SQL_PASSWORD": "s3cret"}, fake, fakeDescribe{})
	rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases")
	var list DatabaseList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(list.Databases) != 1 || list.Databases[0].Name != "sales" || list.Databases[0].LogMB != 8.5 ||
		fake.password != "s3cret" {
		t.Fatalf("list %+v, password %q", list, fake.password)
	}
	if !strings.Contains(rec.Body.String(), `"dataMB":16`) || !strings.Contains(rec.Body.String(), `"readOnly":false`) {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestListDatabasesErrors(t *testing.T) {
	doc := testConfig
	env := map[string]string{"TEST_SQL_PASSWORD": "s3cret"}
	tunnel := strings.Replace(testConfig, "mode: external", "mode: tunnel", 1)
	tests := []struct {
		name string
		doc  *string
		env  map[string]string
		fake *fakeDatabases
		path string
		want int
	}{
		{"unknown target", &doc, env, &fakeDatabases{}, "/api/v1/targets/nope/databases", http.StatusNotFound},
		{"missing config", nil, env, &fakeDatabases{}, "/api/v1/targets/dev-sql/databases", http.StatusConflict},
		{"no password", &doc, nil, &fakeDatabases{}, "/api/v1/targets/dev-sql/databases", http.StatusUnprocessableEntity},
		{"tunnel mode", &tunnel, env, &fakeDatabases{}, "/api/v1/targets/dev-sql/databases", http.StatusOK},
		{"session fails", &doc, env, &fakeDatabases{openErr: errors.New("nothing listening")}, "/api/v1/targets/dev-sql/databases", http.StatusBadGateway},
		{"query fails", &doc, env, &fakeDatabases{queryErr: errors.New("permission denied")}, "/api/v1/targets/dev-sql/databases", http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newDatabasesServer(t, tc.doc, tc.env, tc.fake, fakeDescribe{})
			if rec := do(h, http.MethodGet, tc.path); rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func getOverview(t *testing.T, h http.Handler) (Overview, string) {
	t.Helper()
	rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/overview")
	var o Overview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &o) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	return o, rec.Body.String()
}

func TestOverviewWithSQL(t *testing.T) {
	doc := testConfig
	fake := &fakeDatabases{usage: mssql.Usage{UserDatabases: 16, ActiveTasks: 2}}
	h := newDatabasesServer(t, &doc, map[string]string{"TEST_SQL_PASSWORD": "s3cret"}, fake, fakeDescribe{info: sampleInstance})
	o, _ := getOverview(t, h)
	if o.Target != "dev-sql" || o.Instance != "dev-sqlserver" || o.Status != "available" || o.Class != "db.m6i.large" ||
		o.EngineVersion != "14.00.3465.1.v1" || o.AllocatedGiB != 200 || o.MaxAllocatedGiB != 1000 {
		t.Fatalf("overview %+v", o)
	}
	if o.BackupWindow != "04:29-04:59" || o.MaintenanceWindow != "sun:05:00-sun:05:30" {
		t.Fatalf("windows %q %q", o.BackupWindow, o.MaintenanceWindow)
	}
	if o.FreeBytes == nil || *o.FreeBytes != 104_475_983_462 || o.FreeError != "" {
		t.Fatalf("free %v, error %q", o.FreeBytes, o.FreeError)
	}
	if o.SQL != overviewSQLOK || o.SQLDetail != "" || o.UserDatabases == nil || *o.UserDatabases != 16 ||
		o.ActiveTasks == nil || *o.ActiveTasks != 2 || fake.password != "s3cret" {
		t.Fatalf("sql part %+v, password %q", o, fake.password)
	}
}

func TestOverviewWithoutPasswordIsPartial(t *testing.T) {
	doc := testConfig
	fake := &fakeDatabases{}
	noFree := sampleInstance
	noFree.FreeErr = awsx.ErrNoDatapoint
	h := newDatabasesServer(t, &doc, nil, fake, fakeDescribe{info: noFree})
	o, body := getOverview(t, h)
	if o.SQL != overviewSQLSkipped || !strings.Contains(o.SQLDetail, "TEST_SQL_PASSWORD") || fake.opened {
		t.Fatalf("overview %+v, session opened %v", o, fake.opened)
	}
	if o.FreeBytes != nil || !strings.Contains(o.FreeError, "no recent datapoint") || o.Status != "available" {
		t.Fatalf("overview %+v", o)
	}
	// Absent, not zero: the GUI tells "unknown" from "none".
	for _, field := range []string{"userDatabases", "activeTasks", "freeBytes"} {
		if strings.Contains(body, field) {
			t.Fatalf("%s in %s", field, body)
		}
	}
}

func TestOverviewSQLSkippedOrFailed(t *testing.T) {
	env := map[string]string{"TEST_SQL_PASSWORD": "s3cret"}
	// connect.mode tunnel reads SQL through the integrated tunnel like the others.
	tunnel := strings.Replace(testConfig, "mode: external", "mode: tunnel", 1)
	h := newDatabasesServer(t, &tunnel, env, &fakeDatabases{}, fakeDescribe{info: sampleInstance})
	if o, _ := getOverview(t, h); o.SQL != "ok" {
		t.Fatalf("tunnel: %+v", o)
	}
	doc := testConfig
	h = newDatabasesServer(t, &doc, env, &fakeDatabases{openErr: errors.New("nothing listening")}, fakeDescribe{info: sampleInstance})
	if o, _ := getOverview(t, h); o.SQL != overviewSQLFailed || o.SQLDetail != "nothing listening" || o.UserDatabases != nil {
		t.Fatalf("session fails: %+v", o)
	}
	h = newDatabasesServer(t, &doc, env, &fakeDatabases{queryErr: errors.New("no rds_fn_task_status")}, fakeDescribe{info: sampleInstance})
	if o, _ := getOverview(t, h); o.SQL != overviewSQLFailed || o.SQLDetail != "no rds_fn_task_status" {
		t.Fatalf("query fails: %+v", o)
	}
}

func TestOverviewErrors(t *testing.T) {
	doc := testConfig
	env := map[string]string{"TEST_SQL_PASSWORD": "s3cret"}
	wrong := &awsx.WrongAccountError{Profile: "default", Got: "000000000000", Want: "123456789012"}
	h := newDatabasesServer(t, &doc, env, &fakeDatabases{}, fakeDescribe{err: wrong})
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/overview"); rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), "expected 123456789012") {
		t.Fatalf("AWS fails: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/api/v1/targets/nope/overview"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown target: status %d", rec.Code)
	}
	h = newDatabasesServer(t, nil, env, &fakeDatabases{}, fakeDescribe{info: sampleInstance})
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/overview"); rec.Code != http.StatusConflict {
		t.Fatalf("missing config: status %d", rec.Code)
	}
}
