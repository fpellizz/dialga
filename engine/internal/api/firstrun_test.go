package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/discover"
)

// fakeDiscovery answers the first-run questions without AWS.
type fakeDiscovery struct {
	profile, region string
	err             error
}

func (f *fakeDiscovery) Profiles() ([]discover.Profile, error) {
	return []discover.Profile{{Name: "default"}, {Name: "sales", Region: "us-east-1"}}, f.err
}

func (f *fakeDiscovery) Instances(_ context.Context, profile, region string) (discover.Instances, error) {
	f.profile, f.region = profile, region
	if f.err != nil {
		return discover.Instances{}, f.err
	}
	return discover.Instances{Profile: profile, Region: region, AccountID: "123456789012", Instances: []discover.Instance{
		{Identifier: "dev-sqlserver", Engine: "sqlserver-se", EngineVersion: "14.00", Class: "db.m6i.large",
			Status: "available", MasterUser: "admin", OptionGroup: "native", BackupRestore: true},
	}}, nil
}

func (f *fakeDiscovery) Prerequisites(context.Context) []discover.Prerequisite {
	return []discover.Prerequisite{{ID: discover.PrereqSSMPlugin, Program: "session-manager-plugin", Found: true,
		Path: "/usr/bin/session-manager-plugin", Version: "1.2.707.0", Purpose: "tunnels",
		Install: []discover.InstallHint{{Distro: "fedora", Command: "sudo dnf install x"}}}}
}

func (f *fakeDiscovery) Buckets(_ context.Context, profile, region string) ([]discover.Bucket, error) {
	f.profile, f.region = profile, region
	if f.err != nil {
		return nil, f.err
	}
	return []discover.Bucket{{Name: "a-bucket", Region: region}, {Name: "b-bucket", Region: region}}, nil
}

func (f *fakeDiscovery) CheckBucket(context.Context, string, string, string) error { return nil }

// newFirstRunServer serves configDoc (nil: no file) with a faked discovery.
func newFirstRunServer(t *testing.T, configDoc *string, fake *fakeDiscovery) (http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dialga", "config.yaml")
	if configDoc != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(*configDoc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{Token: testToken, AllowedOrigins: DefaultOrigins, ConfigPath: path, Discover: fake})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), path
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPrerequisitesAndProfiles(t *testing.T) {
	h, _ := newFirstRunServer(t, nil, &fakeDiscovery{})
	var prereqs PrerequisiteList
	rec := send(h, http.MethodGet, "/api/v1/prerequisites", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &prereqs) != nil ||
		len(prereqs.Prerequisites) != 1 || !prereqs.Prerequisites[0].Found {
		t.Fatalf("prerequisites: status %d, body %s", rec.Code, rec.Body)
	}
	var profiles ProfileList
	rec = send(h, http.MethodGet, "/api/v1/discovery/profiles", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &profiles) != nil ||
		len(profiles.Profiles) != 2 || profiles.Profiles[1].Region != "us-east-1" {
		t.Fatalf("profiles: status %d, body %s", rec.Code, rec.Body)
	}
	h, _ = newFirstRunServer(t, nil, &fakeDiscovery{err: errors.New("permission denied")})
	if rec := send(h, http.MethodGet, "/api/v1/discovery/profiles", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("unreadable files: status %d", rec.Code)
	}
}

func TestDiscoveryBuckets(t *testing.T) {
	fake := &fakeDiscovery{}
	h, _ := newFirstRunServer(t, nil, fake)
	rec := send(h, http.MethodGet, "/api/v1/discovery/buckets?profile=sales&region=us-east-1", "")
	var got BucketList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(got.Buckets) != 2 || got.Buckets[0].Name != "a-bucket" || fake.profile != "sales" || fake.region != "us-east-1" {
		t.Fatalf("got %+v, asked %s/%s", got, fake.profile, fake.region)
	}
	for _, q := range []string{"profile=&region=us-east-1", "profile=sales&region=nowhere"} {
		if rec := send(h, http.MethodGet, "/api/v1/discovery/buckets?"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	h, _ = newFirstRunServer(t, nil, &fakeDiscovery{err: fmt.Errorf("list: %w", discover.ErrBucketListDenied)})
	rec = send(h, http.MethodGet, "/api/v1/discovery/buckets?profile=sales&region=us-east-1", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "s3:ListAllMyBuckets") {
		t.Fatalf("denied: status %d, body %s", rec.Code, rec.Body)
	}
	h, _ = newFirstRunServer(t, nil, &fakeDiscovery{err: errors.New("SSO session expired")})
	rec = send(h, http.MethodGet, "/api/v1/discovery/buckets?profile=sales&region=us-east-1", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "SSO session expired") {
		t.Fatalf("AWS failure: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestDiscoveryInstances(t *testing.T) {
	fake := &fakeDiscovery{}
	h, _ := newFirstRunServer(t, nil, fake)
	rec := send(h, http.MethodGet, "/api/v1/discovery/instances?profile=sales&region=us-east-1", "")
	var got discover.Instances
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got.AccountID != "123456789012" || len(got.Instances) != 1 || fake.profile != "sales" || fake.region != "us-east-1" {
		t.Fatalf("got %+v, asked %s/%s", got, fake.profile, fake.region)
	}
	for _, q := range []string{"profile=&region=us-east-1", "profile=sales&region=nowhere", "profile=a%20b&region=us-east-1"} {
		if rec := send(h, http.MethodGet, "/api/v1/discovery/instances?"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	h, _ = newFirstRunServer(t, nil, &fakeDiscovery{err: errors.New("SSO session expired")})
	rec = send(h, http.MethodGet, "/api/v1/discovery/instances?profile=sales&region=us-east-1", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "SSO session expired") {
		t.Fatalf("AWS failure: status %d, body %s", rec.Code, rec.Body)
	}
}

const newTargetBody = `{"name":"prod-sql","accountId":"123456789012","region":"us-east-1","profile":"sales",
"instance":"prod-sqlserver","bucket":"prod-sqlserver-backups","sqlUser":"admin",
"passwordSource":"env:PROD_SQL_PASSWORD","connectMode":"external","localPort":11434,"protected":true}`

func TestSaveTargetFirstRunThenReplace(t *testing.T) {
	h, path := newFirstRunServer(t, nil, &fakeDiscovery{})
	rec := send(h, http.MethodPost, "/api/v1/config/targets", newTargetBody)
	var summary TargetSummary
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &summary) != nil ||
		summary.Name != "prod-sql" || summary.LocalPort != 11434 || !summary.Protected {
		t.Fatalf("add: status %d, body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "env:PROD_SQL_PASSWORD") {
		t.Fatal("the password reference came back")
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Targets["prod-sql"].SQL.Password != "env:PROD_SQL_PASSWORD" {
		t.Fatalf("Load: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}

	// The same name again: 409, then 200 with replace (and the hidden keys kept).
	if rec := send(h, http.MethodPost, "/api/v1/config/targets", newTargetBody); rec.Code != http.StatusConflict {
		t.Fatalf("add twice: status %d, body %s", rec.Code, rec.Body)
	}
	err = config.Update(path, func(c *config.Config) error {
		tgt := c.Targets["prod-sql"]
		tgt.MaxTasks = 1
		return c.SetTarget("prod-sql", tgt, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	replace := strings.Replace(newTargetBody, `"protected":true}`, `"protected":false,"replace":true}`, 1)
	if rec := send(h, http.MethodPost, "/api/v1/config/targets", replace); rec.Code != http.StatusOK {
		t.Fatalf("replace: status %d, body %s", rec.Code, rec.Body)
	}
	if cfg, _ = config.Load(path); cfg.Targets["prod-sql"].Protected || cfg.Targets["prod-sql"].MaxTasks != 1 {
		t.Fatalf("after replace: %+v", cfg.Targets["prod-sql"])
	}
}

func TestSaveTargetRejects(t *testing.T) {
	broken := strings.Replace(testConfig, "region:", "regoin:", 1)
	tests := []struct {
		name    string
		doc     *string
		body    string
		want    int
		mention string
	}{
		{"not JSON", nil, "{", http.StatusBadRequest, "invalid body"},
		{"a password field", nil, strings.Replace(newTargetBody, `"protected":true}`, `"protected":true,"password":"x"}`, 1),
			http.StatusBadRequest, "unknown field"},
		{"a password as source", nil, strings.Replace(newTargetBody, "env:PROD_SQL_PASSWORD", "hunter2", 1),
			http.StatusBadRequest, "prompt, env:VAR, keyring or secretsmanager"},
		{"bad secret ARN", nil, strings.Replace(newTargetBody, "env:PROD_SQL_PASSWORD", "secretsmanager:nope", 1),
			http.StatusBadRequest, "prompt, env:VAR, keyring or secretsmanager"},
		{"bad fields", nil, strings.NewReplacer(`"prod-sql"`, `"Prod"`, `"localPort":11434,`, "").Replace(newTargetBody),
			http.StatusBadRequest, "local_port"},
		{"invalid file on disk", &broken, newTargetBody, http.StatusConflict, "fix it first"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, path := newFirstRunServer(t, tc.doc, &fakeDiscovery{})
			rec := send(h, http.MethodPost, "/api/v1/config/targets", tc.body)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.mention) {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "hunter2") {
				t.Fatal("the rejected value was echoed")
			}
			data, err := os.ReadFile(path)
			if (tc.doc == nil && err == nil) || (tc.doc != nil && string(data) != *tc.doc) {
				t.Fatalf("the file changed: %v\n%s", err, data)
			}
		})
	}
}

func TestRemoveTarget(t *testing.T) {
	doc := testConfig
	h, path := newFirstRunServer(t, &doc, &fakeDiscovery{})
	if rec := send(h, http.MethodDelete, "/api/v1/config/targets/dev-sql", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no confirmation: status %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, "/api/v1/config/targets/dev-sql?confirm=dev", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation: status %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, "/api/v1/config/targets/nope?confirm=nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: status %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, "/api/v1/config/targets/dev-sql?confirm=dev-sql", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: status %d, body %s", rec.Code, rec.Body)
	}
	// The last target is gone: the file too, and the list is a first run again.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still there: %v", err)
	}
	var list TargetList
	if rec := send(h, http.MethodGet, "/api/v1/targets", ""); json.Unmarshal(rec.Body.Bytes(), &list) != nil ||
		len(list.Targets) != 0 || list.Error != "" {
		t.Fatalf("list after remove: %s", rec.Body)
	}
}

func TestPreflightAllowsDelete(t *testing.T) {
	h, _ := newFirstRunServer(t, nil, &fakeDiscovery{})
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/config/targets/dev-sql", nil)
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Origin", "tauri://localhost")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
		t.Fatalf("status %d, methods %q", rec.Code, rec.Header().Get("Access-Control-Allow-Methods"))
	}
}
