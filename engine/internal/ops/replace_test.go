package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

const gib = int64(1) << 30

var renameArgs = regexp.MustCompile(`rds_modify_db_name N'([^']*)', N'([^']*)'`)

// instance applies the renames and the drops of the replace to the states of
// the fake, and fails the statements that match failOn.
func instance(states map[string]string, failOn string) *fakeSQL {
	f := &fakeSQL{states: states, active: map[string]int{}, progress: []mssql.Lifecycle{mssql.Success}}
	f.onExec = func(f *fakeSQL, stmt string) error {
		if failOn != "" && strings.Contains(stmt, failOn) {
			return errors.New("boom: " + failOn)
		}
		if m := renameArgs.FindStringSubmatch(stmt); m != nil {
			f.states[m[2]], f.states[m[1]] = f.states[m[1]], ""
		}
		if m := regexp.MustCompile(`rds_drop_database @db_name = N'([^']*)'`).FindStringSubmatch(stmt); m != nil {
			f.states[m[1]] = ""
		}
		return nil
	}
	return f
}

func replaceSession(f *fakeSQL, free int64, freeErr error, bucket fakeBucket) *Session {
	s := newSession(f, bucket)
	s.FreeStorage = func(context.Context) (int64, error) { return free, freeErr }
	f.report = mssql.DatabaseReport{Database: mssql.DatabaseInfo{State: "ONLINE", CompatibilityLevel: 150}, Access: true, Files: []mssql.DatabaseFile{{Type: "ROWS", SizeMB: 1024}, {Type: "LOG", SizeMB: 4096}}}
	return s
}

func restoreOver(t *testing.T, s *Session, r ReplaceOptions) *Prepared {
	t.Helper()
	p, err := s.RestoreOver(context.Background(), "sales", "sales.bak", mssql.RestoreOptions{}, r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func checkOf(p *Prepared, step string) string {
	for _, c := range p.Checks {
		if c.Step == step {
			return string(c.Status) + ": " + c.Detail
		}
	}
	return ""
}

func actionIDs(p *Prepared) string {
	var ids []string
	for _, a := range p.Plan.Actions {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, " ")
}

func TestValidateReplace(t *testing.T) {
	no := false
	tests := []struct {
		name string
		o    ReplaceOptions
		r    mssql.RestoreOptions
		want string
	}{
		{"none", ReplaceOptions{}, mssql.RestoreOptions{}, ""},
		{"auto", ReplaceOptions{Mode: "auto"}, mssql.RestoreOptions{}, ""},
		{"drop without backup", ReplaceOptions{Mode: "drop", BackupFirst: &no}, mssql.RestoreOptions{}, ""},
		{"unknown", ReplaceOptions{Mode: "merge"}, mssql.RestoreOptions{}, "invalid replace"},
		{"differential", ReplaceOptions{Mode: "swap"}, mssql.RestoreOptions{Differential: true}, "differential"},
		{"norecovery", ReplaceOptions{Mode: "drop"}, mssql.RestoreOptions{NoRecovery: true}, "noRecovery"},
		{"backup without replace", ReplaceOptions{BackupFirst: &no}, mssql.RestoreOptions{}, "needs replace"},
		{"backup with swap", ReplaceOptions{Mode: "swap", BackupFirst: &no}, mssql.RestoreOptions{}, "for drop"},
		{"bad stamp", ReplaceOptions{Mode: "swap", Stamp: "tomorrow"}, mssql.RestoreOptions{}, "invalid stamp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReplace(tc.o, tc.r)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
	if err := Validate(OpDrop, Params{Database: "sales", Replace: ReplaceOptions{Mode: "swap"}}, testTarget); err == nil {
		t.Fatal("replace is for restore only")
	}
}

func TestReplaceAutoChoosesBySpace(t *testing.T) {
	bucket := fakeBucket{"import/sales.bak": 42 * gib}
	tests := []struct {
		name     string
		free     int64
		freeErr  error
		bucket   fakeBucket
		strategy string
		status   string
		reason   string
	}{
		{"two copies fit", 97*gib + gib/5, nil, bucket, "swap", "ok", "swap: two copies (2 × 42.0 GiB) fit in 97.2 GiB free"},
		{"not enough", 61*gib + gib/3, nil, bucket, "drop", "ok",
			"drop: not enough free storage for two copies (needs 84.0 GiB, 61.3 GiB free); the current database is backed up to S3 first"},
		{"free unknown", 0, errors.New("AccessDenied"), bucket, "swap", "warning", "free storage or the size of the backup is unknown"},
		{"size from the database", 10 * gib, nil, fakeBucket{"import/sales.bak": 0}, "swap", "ok", "2 × 1.0 GiB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := instance(map[string]string{"sales": "ONLINE"}, "")
			p := restoreOver(t, replaceSession(f, tc.free, tc.freeErr, tc.bucket), ReplaceOptions{Mode: ReplaceAuto, Stamp: "20261016091500"})
			if !p.Ready() || p.Replace == nil || p.Replace.Strategy != tc.strategy {
				t.Fatalf("ready %v, replace %+v, checks %+v", p.Ready(), p.Replace, p.Checks)
			}
			got := checkOf(p, "replace")
			if !strings.HasPrefix(got, tc.status+": ") || !strings.Contains(got, tc.reason) {
				t.Fatalf("replace check %q, want %s containing %q", got, tc.status, tc.reason)
			}
			if tc.strategy == "drop" && !p.Replace.BackupFirst {
				t.Fatal("auto picks drop only with the backup")
			}
		})
	}
}

func TestReplaceAutoDropIgnoresNoBackup(t *testing.T) {
	no := false
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	p := restoreOver(t, replaceSession(f, 50*gib, nil, fakeBucket{"import/sales.bak": 42 * gib}), ReplaceOptions{Mode: ReplaceAuto, BackupFirst: &no})
	if p.Replace.Strategy != "drop" || !p.Replace.BackupFirst || !strings.Contains(actionIDs(p), "backup") {
		t.Fatalf("%+v %s", p.Replace, actionIDs(p))
	}
}

func TestReplaceSwapPlan(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	f.sessions = map[string]int{"sales": 3}
	s := replaceSession(f, 200*gib, nil, fakeBucket{"import/sales.bak": 42 * gib})
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091500"})
	if !p.Ready() {
		t.Fatalf("checks %+v", p.Checks)
	}
	if got, want := actionIDs(p), "restore wait single-user rename-old rename-new multi-user multi-user-old post"; got != want {
		t.Fatalf("actions %s, want %s", got, want)
	}
	arn := mssql.S3ARN("dev-backups", "import/sales.bak")
	want := []string{
		mssql.RestoreDatabase("sales_dialga_new_20261016091500", arn, mssql.RestoreOptions{}),
		"SELECT * FROM msdb.dbo.rds_fn_task_status(NULL, <task id>) until the task ends",
		"ALTER DATABASE [sales] SET SINGLE_USER WITH ROLLBACK IMMEDIATE;",
		"EXEC rdsadmin.dbo.rds_modify_db_name N'sales', N'sales_old_20261016091500';",
		"EXEC rdsadmin.dbo.rds_modify_db_name N'sales_dialga_new_20261016091500', N'sales';",
		"ALTER DATABASE [sales] SET MULTI_USER;",
		"ALTER DATABASE [sales_old_20261016091500] SET MULTI_USER;",
		"read-only: state, files, login access, orphaned users, Windows users, objects",
	}
	for i, a := range p.Plan.Actions {
		if a.Statement != want[i] {
			t.Errorf("action %s: %q, want %q", a.ID, a.Statement, want[i])
		}
	}
	if got := checkOf(p, "sessions"); !strings.Contains(got, "warning: 3 active session(s) on sales will be disconnected") {
		t.Fatalf("sessions check %q", got)
	}
	if p.Plan.Confirmation() != "sales" || p.Params.Replace.Stamp != "20261016091500" {
		t.Fatalf("confirmation %q, stamp %q", p.Plan.Confirmation(), p.Params.Replace.Stamp)
	}
	s.Target.Protected = true
	if q := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap}); q.Plan.Confirmation() != "dev-sql" {
		t.Fatalf("protected target confirmation %q", q.Plan.Confirmation())
	}
	if len(f.ran) != 0 {
		t.Fatalf("a builder ran %v", f.ran)
	}
}

func TestReplaceDigestIgnoresTheStamp(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	s := replaceSession(f, 200*gib, nil, fakeBucket{"import/sales.bak": 42 * gib})
	a := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091500"})
	b := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091731"})
	c := restoreOver(t, s, ReplaceOptions{Mode: ReplaceDrop, Stamp: "20261016091500"})
	if a.Digest() != b.Digest() || a.Digest() == c.Digest() {
		t.Fatalf("digests %s %s %s", a.Digest(), b.Digest(), c.Digest())
	}
	if d := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap}); d.Params.Replace.Stamp != "20261016071500" {
		t.Fatalf("default stamp %q (the session clock is 09:15 CEST = 07:15 UTC)", d.Params.Replace.Stamp)
	}
}

func TestReplaceDropPlan(t *testing.T) {
	arn := mssql.S3ARN("dev-backups", "import/sales.bak")
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	s := replaceSession(f, 10*gib, nil, fakeBucket{"import/sales.bak": 4 * gib})
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceDrop, Stamp: "20261016091500"})
	if got, want := actionIDs(p), "backup wait-backup drop restore wait post"; got != want {
		t.Fatalf("actions %s, want %s", got, want)
	}
	backup := mssql.BackupDatabase("sales", mssql.S3ARN("dev-backups", "export/sales_before_replace_20261016091500.bak"))
	if p.Plan.Actions[0].Statement != backup || p.Plan.Actions[2].Statement != mssql.DropDatabase("sales") ||
		p.Plan.Actions[3].Statement != mssql.RestoreDatabase("sales", arn, mssql.RestoreOptions{}) {
		t.Fatalf("statements %+v", p.Plan.Actions)
	}
	if p.Plan.Actions[2].Risk != core.Destructive || p.Plan.Confirmation() != "sales" {
		t.Fatalf("drop risk %s, confirmation %q", p.Plan.Actions[2].Risk, p.Plan.Confirmation())
	}

	no := false
	q := restoreOver(t, s, ReplaceOptions{Mode: ReplaceDrop, BackupFirst: &no})
	if got := actionIDs(q); got != "drop restore wait post" || !strings.HasPrefix(checkOf(q, "replace"), "warning: drop: the current database is dropped WITHOUT a backup") {
		t.Fatalf("actions %s, check %q", got, checkOf(q, "replace"))
	}

	taken := replaceSession(instance(map[string]string{"sales": "ONLINE"}, ""), 10*gib, nil,
		fakeBucket{"import/sales.bak": 4 * gib, "export/sales_before_replace_20261016091500.bak": 1})
	if r := restoreOver(t, taken, ReplaceOptions{Mode: ReplaceDrop, Stamp: "20261016091500"}); r.Ready() || !strings.Contains(checkOf(r, "destination"), "already exists") {
		t.Fatalf("a taken backup key must fail: %+v", r.Checks)
	}
}

func TestReplacePreflightFailures(t *testing.T) {
	bucket := fakeBucket{"import/sales.bak": gib}
	f := instance(map[string]string{"sales": "ONLINE", "sales_old_20261016091500": "ONLINE"}, "")
	if p := restoreOver(t, replaceSession(f, 100*gib, nil, bucket), ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091500"}); p.Ready() ||
		!strings.Contains(checkOf(p, "names"), "sales_old_20261016091500 already exists") && !strings.Contains(verdicts(p), "names:failed") {
		t.Fatalf("an existing old name must fail: %+v", p.Checks)
	}
	offline := instance(map[string]string{"sales": "OFFLINE"}, "")
	p := restoreOver(t, replaceSession(offline, 100*gib, nil, bucket), ReplaceOptions{Mode: ReplaceSwap})
	if p.Ready() || len(p.Plan.Actions) != 0 || !strings.Contains(checkOf(p, "database"), "not ONLINE") {
		t.Fatalf("an offline database cannot be replaced: %+v %v", p.Checks, p.Plan.Actions)
	}
	missing := instance(map[string]string{}, "")
	if p := restoreOver(t, replaceSession(missing, 100*gib, nil, bucket), ReplaceOptions{Mode: ReplaceSwap}); p.Replace != nil || !p.Ready() || len(p.Plan.Actions) != 1 {
		t.Fatalf("a missing database is a plain restore: %+v", p)
	}
	exists := instance(map[string]string{"sales": "ONLINE"}, "")
	if p := restoreOver(t, replaceSession(exists, 100*gib, nil, bucket), ReplaceOptions{}); p.Ready() {
		t.Fatal("without replace an existing database is still refused")
	}
}

func TestExecuteSwap(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	s := replaceSession(f, 200*gib, nil, fakeBucket{"import/sales.bak": 42 * gib})
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091500"})
	f.states["sales_dialga_new_20261016091500"] = "ONLINE" // what the restore task creates
	var steps []string
	task, err := p.Execute(context.Background(), ExecOptions{
		Wait: true, Confirm: "sales", Interval: time.Millisecond,
		OnStep: func(e core.Event) {
			if e.Status != core.Running {
				steps = append(steps, e.Step+":"+string(e.Status))
			}
		},
	})
	if err != nil || task == nil || task.Lifecycle != mssql.Success {
		t.Fatalf("task %+v, err %v", task, err)
	}
	if got, want := strings.Join(steps, " "), "restore:ok wait:ok single-user:ok rename-old:ok rename-new:ok multi-user:ok multi-user-old:ok post:ok"; got != want {
		t.Fatalf("steps %s\nwant  %s", got, want)
	}
	if f.states["sales"] != "ONLINE" || f.states["sales_old_20261016091500"] != "ONLINE" || f.states["sales_dialga_new_20261016091500"] != "" {
		t.Fatalf("states %v", f.states)
	}
	if o := p.Outcome(); o == nil || o.Strategy != "swap" || o.OldDatabase != "sales_old_20261016091500" || o.BackupKey != "" {
		t.Fatalf("outcome %+v", o)
	}
	if !strings.Contains(p.Result(), "kept as sales_old_20261016091500") {
		t.Fatalf("result %q", p.Result())
	}
}

func TestExecuteReplaceGuards(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	s := replaceSession(f, 200*gib, nil, fakeBucket{"import/sales.bak": gib})
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap})
	if _, err := p.Execute(context.Background(), ExecOptions{Wait: false, Confirm: "sales"}); !errors.Is(err, ErrReplaceNoWait) {
		t.Fatalf("no-wait: %v", err)
	}
	if _, err := p.Execute(context.Background(), ExecOptions{Wait: true, Confirm: "other"}); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("wrong confirmation: %v", err)
	}
	if len(f.ran) != 0 {
		t.Fatalf("nothing may run: %v", f.ran)
	}
}

func TestSwapCompensatesWhenTheSecondRenameFails(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "N'sales_dialga_new_20261016091500', N'sales'")
	s := replaceSession(f, 200*gib, nil, fakeBucket{"import/sales.bak": gib})
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceSwap, Stamp: "20261016091500"})
	f.states["sales_dialga_new_20261016091500"] = "ONLINE"
	_, err := p.Execute(context.Background(), ExecOptions{Wait: true, Confirm: "sales", Interval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "the previous database sales_old_20261016091500 is back as sales") {
		t.Fatalf("err %v", err)
	}
	if f.states["sales"] != "ONLINE" || f.states["sales_old_20261016091500"] != "" || f.states["sales_dialga_new_20261016091500"] != "ONLINE" {
		t.Fatalf("states %v", f.states)
	}
	last := f.ran[len(f.ran)-1]
	if last != "ALTER DATABASE [sales] SET MULTI_USER;" {
		t.Fatalf("the restored name must be reopened, last statement %q", last)
	}
}

func TestRenameIsSafeToRepeat(t *testing.T) {
	f := instance(map[string]string{"sales_old_x": "ONLINE"}, "")
	s := replaceSession(f, 0, nil, nil)
	ctx := context.Background()
	if detail, err := s.Rename(ctx, "sales", "sales_old_x", ""); err != nil || !strings.Contains(detail, "already named") || len(f.ran) != 0 {
		t.Fatalf("%q %v ran %v", detail, err, f.ran)
	}
	if _, err := s.Rename(ctx, "nope", "gone", ""); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("err %v", err)
	}
	f.states["sales"] = "ONLINE"
	if _, err := s.Rename(ctx, "sales", "sales_old_x", ""); err == nil || !strings.Contains(err.Error(), "already exists") || len(f.ran) != 0 {
		t.Fatalf("err %v ran %v", err, f.ran)
	}
	// a rename that fails leaves the database open
	boom := instance(map[string]string{"sales": "ONLINE"}, "rds_modify_db_name")
	_, err := replaceSession(boom, 0, nil, nil).Rename(ctx, "sales", "sales_old_x", "")
	if err == nil || !strings.Contains(err.Error(), "left in place and is open") || boom.ran[len(boom.ran)-1] != "ALTER DATABASE [sales] SET MULTI_USER;" {
		t.Fatalf("err %v ran %v", err, boom.ran)
	}
}

func TestExecuteDropNamesTheBackupWhenTheRestoreFails(t *testing.T) {
	f := instance(map[string]string{"sales": "ONLINE"}, "")
	s := replaceSession(f, 10*gib, nil, fakeBucket{"import/sales.bak": 4 * gib})
	f.progress = []mssql.Lifecycle{mssql.Success, mssql.Error}
	p := restoreOver(t, s, ReplaceOptions{Mode: ReplaceDrop, Stamp: "20261016091500"})
	var steps []string
	_, err := p.Execute(context.Background(), ExecOptions{
		Wait: true, Confirm: "sales", Interval: time.Millisecond,
		OnStep: func(e core.Event) { steps = append(steps, fmt.Sprintf("%s:%s", e.Step, e.Status)) },
	})
	if err == nil || !strings.Contains(err.Error(), "sales was dropped: its backup is s3://dev-backups/export/sales_before_replace_20261016091500.bak") {
		t.Fatalf("err %v", err)
	}
	if !strings.HasSuffix(strings.Join(steps, " "), "wait:failed post:skipped") {
		t.Fatalf("steps %v", steps)
	}
}
