package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// fakeInstance is the SQL Server of a target in memory: database states,
// the statements run, and the lifecycles a started task goes through.
type fakeInstance struct {
	states   map[string]string
	ran      []string
	execErr  error
	progress []mssql.Lifecycle
	reads    int
	report   mssql.DatabaseReport
}

func (f *fakeInstance) DatabaseState(_ context.Context, db string) (string, error) {
	return f.states[db], nil
}

func (f *fakeInstance) ActiveTasks(context.Context, string) (int, error) { return 0, nil }

func (f *fakeInstance) ActiveSessions(context.Context, string) (int, error) { return 0, nil }

func (f *fakeInstance) StartTask(_ context.Context, stmt, db string) (mssql.Task, error) {
	f.ran = append(f.ran, stmt)
	return mssql.Task{ID: 100, Type: "RESTORE_DB", Database: db, Lifecycle: mssql.Created}, f.execErr
}

func (f *fakeInstance) Exec(_ context.Context, stmt string) error {
	f.ran = append(f.ran, stmt)
	return f.execErr
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
	return mssql.Task{ID: id, Type: "RESTORE_DB", Database: "sales", Lifecycle: l, Info: "[2026-10-16 09:00:00.000] Done."}, nil
}

type fakeObjects map[string]int64

func (b fakeObjects) Size(_ context.Context, key string) (int64, error) {
	if size, ok := b[key]; ok {
		return size, nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b fakeObjects) Keys(context.Context, string) ([]string, error) { return nil, nil }

// withOps makes a open f as the target instance, with the bucket objects;
// opened reports whether a session was opened.
func withOps(a *app, f *fakeInstance, objects fakeObjects) (opened *bool) {
	opened = new(bool)
	a.openOps = func(_ context.Context, name string, t config.Target, _ string) (*ops.Session, func(), error) {
		*opened = true
		return &ops.Session{Name: name, Target: t, SQL: f, Bucket: objects}, func() {}, nil
	}
	a.readConfirm = func(string) (string, error) { return "", errors.New("stdin is not a terminal") }
	return opened
}

func TestRestoreDryRun(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{}
	withOps(a, f, fakeObjects{"import/sales.bak": 2048})
	code, out, _ := run(a, "restore", "sales", "sales.bak")
	if code != 0 || len(f.ran) != 0 {
		t.Fatalf("code %d, ran %v:\n%s", code, f.ran, out)
	}
	for _, want := range []string{
		"Pre-flight checks of restore on dev-sql (instance dev-sqlserver):",
		"OK    Backup in S3", "s3://dev-sqlserver-backups/import/sales.bak (2.0 KiB)",
		"Dry run: restore on dev-sql", "exec msdb.dbo.rds_restore_database",
		"@s3_arn_to_restore_from = N'arn:aws:s3:::dev-sqlserver-backups/import/sales.bak';",
		"Rerun with --apply to run it.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRestorePreflightFailedExits2(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{states: map[string]string{"sales": "ONLINE"}}
	withOps(a, f, fakeObjects{"import/sales.bak": 1})
	code, out, _ := run(a, "restore", "sales", "sales.bak", "-o", "json")
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != exitCheckFailed {
		t.Fatalf("code %d, json %v:\n%s", code, err, out)
	}
	if doc.Ready || doc.Applied || doc.Checks[1].Status != "failed" || len(doc.Digest) != 32 || doc.Plan.Operation != "restore" {
		t.Fatalf("doc %+v", doc)
	}
	code, out, _ = run(a, "restore", "sales", "sales.bak", "--apply")
	if code != exitCheckFailed || len(f.ran) != 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("apply: code %d, ran %v:\n%s", code, f.ran, out)
	}
}

func TestRestoreApplyWaits(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{progress: []mssql.Lifecycle{mssql.Created, mssql.InProgress, mssql.Success}}
	withOps(a, f, fakeObjects{"import/sales.bak": 1})
	code, out, _ := run(a, "restore", "sales", "sales.bak", "--apply", "--interval", "1ms")
	if code != 0 || len(f.ran) != 1 || f.reads != 3 {
		t.Fatalf("code %d, ran %v, reads %d:\n%s", code, f.ran, f.reads, out)
	}
	for _, want := range []string{"Running restore on dev-sql", "task 100 created", "Following the task every 1ms",
		"task 100: CREATED 0%", "task 100: SUCCESS 0%", "Task 100 completed: RESTORE_DB of sales.", "Next: dialga post sales -t dev-sql"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRestoreNoRecoveryHint(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{progress: []mssql.Lifecycle{mssql.Success}}
	withOps(a, f, fakeObjects{"import/full.bak": 1})
	code, out, _ := run(a, "restore", "sales", "full.bak", "--norecovery", "--apply", "--interval", "1ms")
	if code != 0 || !strings.Contains(f.ran[0], "@with_norecovery = 1") || strings.Contains(out, "dialga post") ||
		!strings.Contains(out, "sales stays RESTORING") || !strings.Contains(out, "dialga finish sales -t dev-sql") {
		t.Fatalf("code %d, ran %v:\n%s", code, f.ran, out)
	}
}

func TestRestoreTaskErrorExits3(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withOps(a, &fakeInstance{progress: []mssql.Lifecycle{mssql.Error}}, fakeObjects{"import/sales.bak": 1})
	code, out, _ := run(a, "restore", "sales", "import/sales.bak", "--apply", "--interval", "1ms", "-o", "json")
	var doc opResult
	if code != exitTaskFailed || json.Unmarshal([]byte(out), &doc) != nil || !doc.Applied || doc.Task.Lifecycle != mssql.Error {
		t.Fatalf("code %d, out %s", code, out)
	}
}

func TestExportNoWait(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{states: map[string]string{"sales": "ONLINE"}}
	withOps(a, f, fakeObjects{})
	code, out, _ := run(a, "export", "sales", "--apply", "--no-wait")
	if code != 0 || len(f.ran) != 1 || f.reads != 0 || !strings.Contains(out, "Follow it with: dialga wait 100 -t dev-sql") {
		t.Fatalf("code %d, ran %v, reads %d:\n%s", code, f.ran, f.reads, out)
	}
	if !strings.Contains(f.ran[0], "arn:aws:s3:::dev-sqlserver-backups/export/sales_") {
		t.Fatalf("statement %s", f.ran[0])
	}
}

func TestDropConfirmation(t *testing.T) {
	online := map[string]string{"sales": "ONLINE"}
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	tests := []struct {
		name     string
		doc      string
		args     []string
		answer   string // typed on the terminal; "" = no terminal
		wantCode int
		wantRan  bool
		wantErr  string
	}{
		{"dry run", testConfig, nil, "", 0, false, ""},
		{"no terminal, no --confirm", testConfig, []string{"--apply"}, "", exitFailure, false, "pass --confirm"},
		{"wrong --confirm", testConfig, []string{"--apply", "--confirm", "Sales"}, "", exitFailure, false, "not confirmed"},
		{"right --confirm", testConfig, []string{"--apply", "--confirm", "sales"}, "", 0, true, ""},
		{"retyped on the terminal", testConfig, []string{"--apply"}, "sales", 0, true, ""},
		{"mistyped on the terminal", testConfig, []string{"--apply"}, "sale", exitFailure, false, "not confirmed"},
		{"protected wants the target", protected, []string{"--apply", "--confirm", "sales"}, "", exitFailure, false, `expected "dev-sql"`},
		{"protected target name", protected, []string{"--apply", "--confirm", "dev-sql"}, "", 0, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := testApp(t, tc.doc)
			f := &fakeInstance{states: online}
			withOps(a, f, fakeObjects{})
			var prompt string
			if tc.answer != "" {
				a.readConfirm = func(p string) (string, error) { prompt = p; return tc.answer, nil }
			}
			code, out, errOut := run(a, append([]string{"drop", "sales"}, tc.args...)...)
			if code != tc.wantCode || (len(f.ran) == 1) != tc.wantRan || !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("code %d, ran %v, stderr %q:\n%s", code, f.ran, errOut, out)
			}
			if tc.answer != "" && !strings.Contains(prompt, "This drops sales on dev-sql for good") {
				t.Fatalf("prompt %q", prompt)
			}
		})
	}
}

func TestOpsBadInputOpensNothing(t *testing.T) {
	for _, args := range [][]string{
		{"restore", "x;y", "a.bak"}, {"restore", "sales", "it's.bak"}, {"restore", "sales", "s3://other/a.bak"},
		{"export", "sales", "export/a*.bak"}, {"cancel", "abc"}, {"cancel", "0"}, {"drop", "master"},
		{"finish", "sales", "--interval", "0s"}, {"post", "x;y"},
	} {
		a, _, _ := testApp(t, testConfig)
		opened := withOps(a, &fakeInstance{}, fakeObjects{})
		if code, _, _ := run(a, args...); code != exitFailure || *opened {
			t.Fatalf("%v: code %d, opened %v", args, code, *opened)
		}
	}
}

func TestOpsActionFailureExits1(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{states: map[string]string{"sales": "RESTORING"}, execErr: errors.New("the database is in use")}
	withOps(a, f, fakeObjects{})
	code, out, errOut := run(a, "finish", "sales", "--apply")
	if code != exitFailure || !strings.Contains(out, "FAIL  Finish the restore of sales") || !strings.Contains(errOut, "the database is in use") {
		t.Fatalf("code %d, stderr %q:\n%s", code, errOut, out)
	}
}

func TestCancelApply(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := &fakeInstance{}
	withOps(a, f, fakeObjects{})
	code, out, _ := run(a, "cancel", "7", "--apply")
	if code != 0 || len(f.ran) != 1 || f.ran[0] != mssql.CancelTask(7) || !strings.Contains(out, "dialga wait 7") {
		t.Fatalf("code %d, ran %v:\n%s", code, f.ran, out)
	}
}

func TestPost(t *testing.T) {
	maxMB := 2048.0
	report := mssql.DatabaseReport{
		Database: mssql.DatabaseInfo{Name: "sales", State: "ONLINE", RecoveryModel: "FULL", CompatibilityLevel: 140,
			Collation: "SQL_Latin1_General_CP1_CI_AS", Owner: "rdsa", UserAccess: "MULTI_USER"},
		Files: []mssql.DatabaseFile{
			{Type: "ROWS", Name: "sales", SizeMB: 8, PhysicalName: `D:\rdsdbdata\DATA\sales.mdf`},
			{Type: "LOG", Name: "sales_log", SizeMB: 1.5, MaxSizeMB: &maxMB, PhysicalName: `D:\rdsdbdata\DATA\sales_log.ldf`},
		},
		Access: true, OrphanUsers: []string{"app"}, Objects: []mssql.ObjectCount{{Type: "USER_TABLE", Count: 12}},
	}
	a, _, _ := testApp(t, testConfig)
	withOps(a, &fakeInstance{states: map[string]string{"sales": "ONLINE"}, report: report}, fakeObjects{})
	code, out, _ := run(a, "post", "sales")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{"Database sales on dev-sql (instance dev-sqlserver)", "compatibility level 140",
		"owner rdsa, access MULTI_USER, read-write", "unlimited", "2048.0", "WARN  Orphaned users",
		"1 SQL user(s) without a login on the instance: app", "USER_TABLE  12"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}

	report.Access = false
	a, _, _ = testApp(t, testConfig)
	withOps(a, &fakeInstance{states: map[string]string{"sales": "ONLINE"}, report: report}, fakeObjects{})
	code, out, _ = run(a, "post", "sales", "-o", "json")
	var doc ops.Report
	if code != exitCheckFailed || json.Unmarshal([]byte(out), &doc) != nil || doc.OK || doc.Access {
		t.Fatalf("no access: code %d:\n%s", code, out)
	}

	a, _, _ = testApp(t, testConfig)
	withOps(a, &fakeInstance{}, fakeObjects{})
	if code, _, errOut := run(a, "post", "nope"); code != exitCheckFailed || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing: code %d, stderr %q", code, errOut)
	}
}
