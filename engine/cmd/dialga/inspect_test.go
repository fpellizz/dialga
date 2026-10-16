package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/inspect"
)

// fakeInspect records the request and answers with outcome (or err).
func fakeInspect(got *inspect.Request, outcome inspect.Outcome, err error) inspectFunc {
	return func(_ context.Context, req inspect.Request, emit check.Emitter) (*inspect.Result, error) {
		*got = req
		emit(check.Event{Step: "header", Label: "Backup headers", Status: check.Running})
		emit(check.Event{Step: "header", Label: "Backup headers", Status: check.OK, Detail: "app.bak: app"})
		if err != nil {
			return nil, err
		}
		b := inspect.Backup{
			Files: []string{"/data/app.bak"}, Outcome: outcome, RequiredBytes: 16 << 20, BackupSets: 1,
			Header: &inspect.Header{DatabaseName: "app", Product: "SQL Server 2017", Version: "14.0.3560",
				BackupType: "full", Compressed: true, BackupSize: 3 << 20, CompressedSize: 1 << 20},
			DatabaseFiles: []inspect.DatabaseFile{{LogicalName: "app", Type: "D", SizeBytes: 8 << 20, FileGroup: "PRIMARY"}},
		}
		if outcome == inspect.NotRestorable {
			b.Findings = []inspect.Finding{{Check: "version", Status: check.Failed, Message: "backup from SQL Server 2022"}}
			b.Remedy = "export a .bacpac"
		}
		return &inspect.Result{Outcome: outcome, Runtime: "podman 5.8.4", Backups: []inspect.Backup{b}}, nil
	}
}

func TestInspectHeadersOnlyNeedsNoConfig(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	a.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	var req inspect.Request
	a.runInspect = fakeInspect(&req, inspect.Read, nil)
	code, out, errOut := run(a, "inspect", "app.bak", "crm_1.bak", "--image", "localhost/mssql:2025")
	if code != 0 || !strings.Contains(out, "headers only") || !strings.Contains(out, "OK    Backup headers") ||
		!strings.Contains(out, "Headers read.") || !strings.Contains(out, "1.0 MiB compressed") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	if req.Target != nil || !slices.Equal(req.Paths, []string{"app.bak", "crm_1.bak"}) || req.Image != "localhost/mssql:2025" {
		t.Fatalf("request %+v", req)
	}
}

func TestInspectOutcomes(t *testing.T) {
	tests := []struct {
		outcome inspect.Outcome
		err     error
		code    int
		text    string
	}{
		{inspect.Restorable, nil, 0, "Restorable on dev-sql."},
		{inspect.NotRestorable, nil, exitCheckFailed, "What to do: export a .bacpac"},
		{inspect.Unreadable, nil, exitFailure, "Some files could not be read"},
		{"", errors.New("no container runtime: install podman"), exitFailure, "Inspecting 1 file(s) against dev-sql"},
	}
	for _, tc := range tests {
		t.Run(string(tc.outcome), func(t *testing.T) {
			a, _, _ := testApp(t, testConfig)
			var req inspect.Request
			a.runInspect = fakeInspect(&req, tc.outcome, tc.err)
			code, out, errOut := run(a, "inspect", "-t", "dev-sql", "app.bak")
			if code != tc.code || !strings.Contains(out, tc.text) || req.Target == nil {
				t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
			}
			if tc.err != nil && !strings.Contains(errOut, "dialga: no container runtime") {
				t.Fatalf("stderr %q", errOut)
			}
		})
	}
}

func TestInspectJSON(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	var req inspect.Request
	a.runInspect = fakeInspect(&req, inspect.NotRestorable, nil)
	code, out, _ := run(a, "inspect", "-t", "dev-sql", "app.bak", "-o", "json")
	var doc struct {
		Target string          `json:"target"`
		OK     bool            `json:"ok"`
		Steps  []check.Event   `json:"steps"`
		Result *inspect.Result `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != exitCheckFailed || doc.OK || doc.Target != "dev-sql" || len(doc.Steps) != 1 ||
		doc.Result.Backups[0].Findings[0].Check != "version" {
		t.Fatalf("code %d, doc %+v", code, doc)
	}

	a.runInspect = fakeInspect(&req, "", errors.New("image missing"))
	code, out, _ = run(a, "inspect", "app.bak", "-o", "json")
	doc.Result = nil
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != exitFailure || doc.OK || doc.Error != "image missing" || doc.Result != nil {
		t.Fatalf("error: code %d, doc %+v, %v", code, doc, err)
	}
}

func TestInspectArgsAndTarget(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	a.runInspect = func(context.Context, inspect.Request, check.Emitter) (*inspect.Result, error) {
		t.Fatal("inspect ran")
		return nil, nil
	}
	if code, _, errOut := run(a, "inspect"); code != exitFailure || !strings.Contains(errOut, "requires at least 1 arg") {
		t.Fatalf("no file: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "inspect", "-t", "nope", "app.bak"); code != exitFailure || !strings.Contains(errOut, "unknown target") {
		t.Fatalf("unknown target: code %d, stderr %q", code, errOut)
	}
}
