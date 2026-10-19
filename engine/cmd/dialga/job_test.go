package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/ops/opstest"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

// jobRDS is an instance whose native tasks end on their second read, in
// end (SUCCESS by default); a restore brings its database online.
type jobRDS struct {
	opstest.NoAccess
	mu     sync.Mutex
	states map[string]string
	tasks  map[int]*mssql.Task
	end    mssql.Lifecycle
}

func (f *jobRDS) DatabaseState(_ context.Context, db string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[db], nil
}

func (f *jobRDS) ActiveTasks(context.Context, string) (int, error) { return 0, nil }

func (f *jobRDS) ActiveSessions(context.Context, string) (int, error) { return 0, nil }

func (f *jobRDS) StartTask(_ context.Context, _, db string) (mssql.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &mssql.Task{ID: len(f.tasks) + 1, Type: "RESTORE_DB", Database: db, Lifecycle: mssql.Created}
	f.tasks[t.ID] = t
	return *t, nil
}

func (f *jobRDS) Exec(context.Context, string) error { return nil }

func (f *jobRDS) DatabaseReport(_ context.Context, db string) (mssql.DatabaseReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: db, State: f.states[db], CompatibilityLevel: 140}, Access: true}, nil
}

func (f *jobRDS) List(context.Context, mssql.TaskFilter) ([]mssql.Task, error) { return nil, nil }

func (f *jobRDS) Get(_ context.Context, id int) (mssql.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tasks[id]
	switch {
	case t.Lifecycle.Done():
	case t.Lifecycle == mssql.InProgress:
		t.Lifecycle, t.Percent = mssql.Success, 100
		if f.end != "" {
			t.Lifecycle = f.end
		}
		if t.Lifecycle == mssql.Success {
			f.states[t.Database] = "ONLINE"
		}
	default:
		t.Lifecycle, t.Percent = mssql.InProgress, 50
	}
	return *t, nil
}

type jobObjects struct{ s3 *transfertest.S3 }

func (b jobObjects) Size(_ context.Context, key string) (int64, error) {
	if o := b.s3.Get(testBucket, key); o != nil {
		return int64(len(o.Data)), nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b jobObjects) Keys(context.Context, string) ([]string, error) { return nil, nil }

func jobApp(t *testing.T, doc string) (*app, *jobRDS, *transfertest.S3) {
	t.Helper()
	s3 := transfertest.New()
	a := transferApp(t, doc, s3)
	rds := &jobRDS{states: map[string]string{}, tasks: map[int]*mssql.Task{}}
	a.openOps = func(_ context.Context, name string, tgt config.Target, _ string) (*ops.Session, func(), error) {
		return &ops.Session{Name: name, Target: tgt, SQL: rds, Bucket: jobObjects{s3}}, func() {}, nil
	}
	a.readConfirm = func(string) (string, error) { return "", errors.New("stdin is not a terminal") }
	return a, rds, s3
}

func TestImportDryRun(t *testing.T) {
	a, _, s3 := jobApp(t, testConfig)
	code, out, _ := run(a, "import", localFile(t, "sales.bak", "backup"))
	if code != 0 || !strings.Contains(out, "Dry run: import of 1 database(s) on dev-sql") ||
		!strings.Contains(out, "exec msdb.dbo.rds_restore_database") || !strings.Contains(out, "Run again with --apply to start the job.") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if s3.Calls("CreateMultipartUpload") != 0 {
		t.Fatal("a dry run uploaded")
	}
}

func TestImportApply(t *testing.T) {
	a, rds, s3 := jobApp(t, testConfig)
	code, out, errOut := run(a, "import", localFile(t, "a.bak", "first"), localFile(t, "b.bak", "second"),
		"--db", "sales,hr", "--apply", "--interval", "1ms")
	if code != 0 || !strings.Contains(out, "succeeded: 2 database(s) done.") || !strings.Contains(out, "sales restored: ONLINE") ||
		!strings.Contains(out, "OK    task 1 completed") || !strings.Contains(out, "OK    task 2 completed") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	if s3.Get(testBucket, "import/a.bak") == nil || rds.states["sales"] != "ONLINE" || rds.states["hr"] != "ONLINE" {
		t.Fatal("the instance is not as imported")
	}
	// Progress events are best effort (a small upload can end before the
	// command subscribes): TestJobReportProgress covers the printer.
}

func TestJobReportProgress(t *testing.T) {
	var out, errOut strings.Builder
	r := &jobReport{out: &out, errOut: &errOut, shown: map[string]int{}}
	for _, done := range []int64{5, 10, 15, 100} {
		r.event(jobs.Event{Type: jobs.EventUploadProgress, Item: "sales",
			Transfer: &jobs.TransferProgress{Direction: jobs.Upload, Bytes: done << 20, Total: 100 << 20}})
	}
	lines := strings.Split(strings.TrimSpace(errOut.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "upload   5%") || !strings.Contains(lines[1], "upload  10%") ||
		!strings.Contains(lines[2], "upload 100%  100 MiB of 100 MiB") {
		t.Fatalf("progress lines %q", lines)
	}
	if out.Len() != 0 {
		t.Fatalf("progress on stdout: %q", out.String())
	}
}

func TestImportArguments(t *testing.T) {
	a, _, _ := jobApp(t, testConfig)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"import", localFile(t, "my backup.bak", "x")}, "pass --db"},
		{[]string{"import", localFile(t, "a.bak", "x"), localFile(t, "b.bak", "y"), "--db", "a"}, "one database per file"},
		{[]string{"import", localFile(t, "a.bak", "x"), localFile(t, "b.bak", "y"), "--key", "k.bak"}, "--key names the object of one file"},
		{[]string{"import", localFile(t, "a.bak", "x"), "--db", "msdb"}, "system database"},
		{[]string{"import", localFile(t, "a.bak", "x"), "--interval", "0s"}, "--interval must be positive"},
	}
	for _, tc := range tests {
		if code, _, errOut := run(a, tc.args...); code != exitFailure || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: code %d, stderr %q", tc.args, code, errOut)
		}
	}
}

func TestImportPreflightAndTaskFailures(t *testing.T) {
	a, rds, s3 := jobApp(t, testConfig)
	rds.states["sales"] = "ONLINE"
	code, out, _ := run(a, "import", localFile(t, "sales.bak", "x"), "--apply")
	if code != exitCheckFailed || !strings.Contains(out, "FAIL  Target database") || !strings.Contains(out, "Nothing was started.") ||
		s3.Calls("CreateMultipartUpload") != 0 {
		t.Fatalf("pre-flight: code %d, out %q", code, out)
	}
	rds.end = mssql.Error
	code, out, _ = run(a, "import", localFile(t, "hr.bak", "x"), "--apply", "--interval", "1ms")
	if code != exitTaskFailed || !strings.Contains(out, "failed at wait: task 1 ended in ERROR") {
		t.Fatalf("task error: code %d, out %q", code, out)
	}
}

func TestImportConfirmation(t *testing.T) {
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	a, _, s3 := jobApp(t, protected)
	file := localFile(t, "sales.bak", "x")
	if code, _, errOut := run(a, "import", file, "--apply"); code != exitFailure || !strings.Contains(errOut, `asks to retype "dev-sql"`) {
		t.Fatalf("no confirmation: code %d, stderr %q", code, errOut)
	}
	if s3.Calls("CreateMultipartUpload") != 0 {
		t.Fatal("started without the confirmation")
	}
	if code, out, _ := run(a, "import", file, "--apply", "--confirm", "dev-sql", "--interval", "1ms"); code != 0 {
		t.Fatalf("confirmed: code %d, out %q", code, out)
	}
}

func TestImportJSON(t *testing.T) {
	a, _, _ := jobApp(t, testConfig)
	code, out, errOut := run(a, "-o", "json", "import", localFile(t, "sales.bak", "x"), "--apply", "--interval", "1ms")
	var doc struct {
		Applied bool     `json:"applied"`
		Ready   bool     `json:"ready"`
		Job     jobs.Job `json:"job"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil || !doc.Applied || !doc.Ready || doc.Job.State != jobs.Succeeded {
		t.Fatalf("code %d, out %q", code, out)
	}
	types := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(errOut))
	for sc.Scan() {
		var line struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("stderr line %q: %v", sc.Text(), err)
		}
		types[line.Event] = true
	}
	for _, want := range []string{"job.queued", "step.started", "step.succeeded", "task.status", "job.succeeded"} {
		if !types[want] {
			t.Errorf("no %s event on stderr", want)
		}
	}
}

func TestRunPlanFile(t *testing.T) {
	a, rds, _ := jobApp(t, testConfig)
	dir := t.TempDir()
	for _, name := range []string{"sales.bak", "hr.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(dir, "import.yaml")
	if err := os.WriteFile(plan, []byte(`apiVersion: v1
kind: Import
target: dev-sql
items:
  - { file: sales.bak, database: sales }
  - { file: hr.bak, database: hr }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(a, "run", plan, "--apply", "--interval", "1ms"); code != 0 || rds.states["hr"] != "ONLINE" {
		t.Fatalf("code %d, out %q", code, out)
	}
	if code, _, errOut := run(a, "run", plan, "-t", "prod"); code != exitFailure || !strings.Contains(errOut, "the plan is for target dev-sql") {
		t.Fatalf("other target: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "run", filepath.Join(dir, "missing.yaml")); code != exitFailure || !strings.Contains(errOut, "no such file") {
		t.Fatalf("missing plan: code %d, stderr %q", code, errOut)
	}
}

func TestFollowJobCancelsOnCtrlC(t *testing.T) {
	a, _, s3 := jobApp(t, testConfig)
	started := make(chan struct{})
	var once sync.Once
	s3.OnUploadPart = func(ctx context.Context, _ int32, _ int) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	cfg, _, _ := a.loadConfig()
	name, tgt, _ := cfg.Target("")
	env := pipeline.Env{Name: name, Target: tgt, S3: transfer.Client(s3),
		OpenOps: func(ctx context.Context) (*ops.Session, func(), error) { return a.openOps(ctx, name, tgt, "") }}
	p, err := pipeline.Build(context.Background(), env, pipeline.Request{Kind: jobs.Import,
		Import: []pipeline.ImportItem{{Files: []string{localFile(t, "sales.bak", "x")}, Database: "sales"}}})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel() // Ctrl-C
	}()
	var seen []jobs.EventType
	final, err := followJob(ctx, m, job.ID, func(ev jobs.Event) { seen = append(seen, ev.Type) })
	if err != nil || final.State != jobs.Cancelled || s3.Uploads() != 0 || s3.Calls("AbortMultipartUpload") != 1 {
		t.Fatalf("job %s, %v, open uploads %d", final.State, err, s3.Uploads())
	}
	if seen[len(seen)-1] != jobs.EventJobCancelled {
		t.Fatalf("last event %s", seen[len(seen)-1])
	}
	if jobExit(final) != exitFailure {
		t.Fatalf("exit %d", jobExit(final))
	}
}
