package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

const replaceStamp = "20261016120000" // the fake clock of testEnv

func replaceReq(t *testing.T, mode string, backupFirst *bool) Request {
	t.Helper()
	return importOf(ImportItem{Files: []string{backup(t, "sales.bak", "new data")}, Database: "sales", Replace: mode, BackupFirst: backupFirst})
}

func TestBuildReplaceSwap(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	same := replaceReq(t, ops.ReplaceSwap, nil)
	p, err := Build(context.Background(), env, same)
	if err != nil {
		t.Fatal(err)
	}
	it := p.Items[0]
	if !p.Ready || p.Confirmation != "sales" || p.Stamp != replaceStamp {
		t.Fatalf("ready %v, confirmation %q, stamp %q, checks %+v", p.Ready, p.Confirmation, p.Stamp, it.Checks)
	}
	if got, want := actionIDs(it), "upload,restore,wait,single-user,rename-old,rename-new,multi-user,multi-user-old,post"; got != want {
		t.Fatalf("actions %s, want %s", got, want)
	}
	for _, want := range []string{
		"@restore_db_name = N'sales_dialga_new_" + replaceStamp + "',",
		"ALTER DATABASE [sales] SET SINGLE_USER WITH ROLLBACK IMMEDIATE;",
		"EXEC rdsadmin.dbo.rds_modify_db_name N'sales', N'sales_old_" + replaceStamp + "';",
		"EXEC rdsadmin.dbo.rds_modify_db_name N'sales_dialga_new_" + replaceStamp + "', N'sales';",
		"[destructive]",
		"Starting it asks to retype \"sales\".",
	} {
		if !strings.Contains(p.Preview, want) {
			t.Errorf("preview misses %q:\n%s", want, p.Preview)
		}
	}
	// A later preview dates its names differently but is the same plan.
	later := env
	later.Now = func() time.Time { return time.Date(2026, 10, 16, 12, 5, 0, 0, time.UTC) }
	again, err := Build(context.Background(), later, same)
	if err != nil || again.Digest != p.Digest || again.Stamp == p.Stamp {
		t.Fatalf("digest %s vs %s, stamp %s, err %v", again.Digest, p.Digest, again.Stamp, err)
	}
	// With the stamp sent back the names are the previewed ones.
	same.Stamp = p.Stamp
	if sent, err := Build(context.Background(), later, same); err != nil || sent.Stamp != p.Stamp || !strings.Contains(sent.Preview, "sales_old_"+replaceStamp) {
		t.Fatalf("stamp %s, err %v", sent.Stamp, err)
	}
	// Without replace an existing database is still refused.
	if refused, err := Build(context.Background(), env, replaceReq(t, "", nil)); err != nil || refused.Ready {
		t.Fatalf("ready %v, err %v", refused.Ready, err)
	}
}

func TestRunReplaceSwap(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	job := runJob(t, env, replaceReq(t, ops.ReplaceSwap, nil))
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s: %+v", job.State, job.Items)
	}
	it := job.Items[0]
	want := "upload:succeeded,restore:succeeded,wait:succeeded,single-user:succeeded,rename-old:succeeded,rename-new:succeeded,multi-user:succeeded,multi-user-old:succeeded,post:succeeded"
	if got := stepStatuses(it); got != want {
		t.Fatalf("steps %s\nwant  %s", got, want)
	}
	if rds.states["sales"] != "ONLINE" || rds.states["sales_old_"+replaceStamp] != "ONLINE" || rds.states["sales_dialga_new_"+replaceStamp] != "" {
		t.Fatalf("states %v", rds.states)
	}
	if !strings.Contains(it.Result, "kept as sales_old_"+replaceStamp) {
		t.Fatalf("result %q", it.Result)
	}
	if o := it.Replace; o == nil || o.Strategy != "swap" || o.OldDatabase != "sales_old_"+replaceStamp || o.BackupKey != "" {
		t.Fatalf("outcome %+v", o)
	}
}

func TestRunReplaceDropBacksUpFirst(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	job := runJob(t, env, replaceReq(t, ops.ReplaceDrop, nil))
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s: %+v", job.State, job.Items)
	}
	key := "export/sales_before_replace_" + replaceStamp + ".bak"
	if got, want := stepStatuses(job.Items[0]), "upload:succeeded,backup:succeeded,wait-backup:succeeded,drop:succeeded,restore:succeeded,wait:succeeded,post:succeeded"; got != want {
		t.Fatalf("steps %s\nwant  %s", got, want)
	}
	if o := job.Items[0].Replace; o == nil || o.Strategy != "drop" || o.BackupKey != key || o.OldDatabase != "" {
		t.Fatalf("outcome %+v", o)
	}
	if s3.Get(testBucket, key) == nil || rds.states["sales"] != "ONLINE" || !strings.Contains(job.Items[0].Result, key) {
		t.Fatalf("backup %v, states %v, result %q", s3.Get(testBucket, key), rds.states, job.Items[0].Result)
	}

	// Without the backup the drop goes straight to the restore.
	env2, rds2, _ := testEnv(t, testTarget)
	rds2.states["sales"] = "ONLINE"
	no := false
	job = runJob(t, env2, replaceReq(t, ops.ReplaceDrop, &no))
	if got := stepStatuses(job.Items[0]); job.State != jobs.Succeeded || strings.Contains(got, "backup") {
		t.Fatalf("job %s steps %s", job.State, got)
	}
}

func TestReplaceRequestValidation(t *testing.T) {
	abs := backup(t, "sales.bak", "x")
	no := false
	tests := []struct {
		name string
		it   ImportItem
		want string
	}{
		{"unknown mode", ImportItem{Files: []string{abs}, Database: "sales", Replace: "merge"}, "invalid replace"},
		{"differential", ImportItem{Files: []string{abs}, Database: "sales", Replace: "swap", Differential: true}, "differential"},
		{"backupFirst alone", ImportItem{Files: []string{abs}, Database: "sales", BackupFirst: &no}, "needs replace"},
	}
	for _, tc := range tests {
		if err := importOf(tc.it).Validate(testTarget); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	req := importOf(ImportItem{Files: []string{abs}, Database: "sales", Replace: "auto"})
	req.Stamp = "soon"
	if err := req.Validate(testTarget); err == nil || !strings.Contains(err.Error(), "stamp") {
		t.Fatalf("stamp: %v", err)
	}
}

func TestParseFileReplace(t *testing.T) {
	f, err := ParseFile([]byte(`apiVersion: v1
kind: Import
target: dev-sql
replace: auto
items:
  - file: a.bak
    database: a
  - file: b.bak
    database: b
    replace: drop
    backupFirst: false
`), "/x")
	if err != nil {
		t.Fatal(err)
	}
	r := f.Request().Import
	if r[0].Replace != "auto" || r[0].BackupFirst != nil || r[1].Replace != "drop" || r[1].BackupFirst == nil || *r[1].BackupFirst {
		t.Fatalf("%+v", r)
	}
	if _, err := ParseFile([]byte("apiVersion: v1\nkind: Export\ntarget: t\nreplace: auto\nitems:\n  - database: a\n"), "/x"); err == nil {
		t.Fatal("replace is for imports")
	}
}

func TestSQLStepFactory(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	good := SQLParams{Op: ops.StepRename, Database: "sales_dialga_new_" + replaceStamp, To: "sales", Fallback: "sales_old_" + replaceStamp,
		Statement: mssql.RenameDatabase("sales_dialga_new_"+replaceStamp, "sales")}
	step, err := rebuildSQL(rebuildOf(t, env, jobs.StepInfo{ID: "rename-new", Title: "t"}, good))
	if err != nil || step.(jobs.Resumable).Kind() != KindSQL || step.Resource() != jobs.NoResource {
		t.Fatalf("step %v, err %v", step, err)
	}
	bad := good
	bad.Statement += "; drop database sales"
	other := SQLParams{Op: ops.StepDrop, Database: "sales", To: "x", Statement: mssql.DropDatabase("sales")}
	for name, p := range map[string]SQLParams{
		"tampered": bad, "unknown op": {Op: "shrink", Database: "sales"}, "bad name": {Op: ops.StepDrop, Database: "a b"}, "stray name": other,
	} {
		if _, err := rebuildSQL(rebuildOf(t, env, jobs.StepInfo{ID: "x"}, p)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// the restore of a swap goes to a name longer than a database name may be
	long := strings.Repeat("d", 100)
	p := TaskParams{Database: long + "_dialga_new_" + replaceStamp, Key: "import/x.bak"}
	p.Statement = mssql.RestoreDatabase(p.Database, mssql.S3ARN(testBucket, p.Key), mssql.RestoreOptions{})
	if _, err := rebuildTask(rebuildOf(t, env, jobs.StepInfo{ID: "restore"}, p)); err != nil {
		t.Fatal(err)
	}
	bk := TaskParams{Database: "sales", Key: "export/sales_before_replace_" + replaceStamp + ".bak"}
	bk.Statement = mssql.BackupDatabase(bk.Database, mssql.S3ARN(testBucket, bk.Key))
	if _, err := rebuildTask(rebuildOf(t, env, jobs.StepInfo{ID: ops.StepBackup}, bk)); err != nil {
		t.Fatal(err)
	}
}
