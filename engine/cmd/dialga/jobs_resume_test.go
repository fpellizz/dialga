package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// interruptedImport leaves in a journal an import of sales on dev-sql that
// the engine stopped mid-upload, with its restore still to do.
func interruptedImport(t *testing.T) (dir, id string) {
	t.Helper()
	dir = t.TempDir()
	m, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	upload := jobs.NewStep("upload", "Upload sales.bak", jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
		r.Upload(jobs.UploadState{Bucket: "b", Key: "import/sales.bak", UploadID: "up-1", Size: 100 << 20, PartSize: 10 << 20, PartsDone: []int{1}})
		<-ctx.Done()
		return "", ctx.Err()
	})
	restore := jobs.NewStep("restore", "Restore sales", jobs.ResourceRDSTask, func(context.Context, jobs.Reporter) (string, error) { return "", nil })
	job, err := m.Start(jobs.Spec{Kind: jobs.Import, Target: "dev-sql", Instance: "dev-sqlserver", Items: []jobs.ItemSpec{{
		Name: "sales", Steps: []jobs.Step{
			jobs.WithKind(upload, "upload", map[string]string{"file": "/backups/sales.bak"}),
			jobs.WithKind(restore, "restore", map[string]string{"db": "sales"}),
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, _ := m.Get(job.ID)
		if j.Items[0].Upload != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload did not start")
		}
		time.Sleep(2 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return dir, job.ID
}

// resumable gives a the pipeline it needs: factories that rebuild the
// upload (from its upload id) and the restore, and an env that records
// what it was built with.
func resumable(a *app, restoreErr error) *string {
	built := new(string)
	a.jobEnv = func(_ context.Context, name string, _ config.Target, password string) (any, func(), error) {
		*built = name + " with " + password
		return "env", func() {}, nil
	}
	reg := jobs.NewRegistry()
	reg.Register("upload", func(rb jobs.Rebuild) (jobs.Step, error) {
		up := rb.Item.Upload
		return jobs.NewStep(rb.Step.ID, rb.Step.Title, jobs.ResourceTransfer, func(_ context.Context, r jobs.Reporter) (string, error) {
			for _, done := range []int64{10, 100} { // the engine sends at most 4 a second
				r.Transfer(jobs.Upload, done<<20, 100<<20)
			}
			return "resumed " + up.UploadID, nil
		}), nil
	})
	reg.Register("restore", func(rb jobs.Rebuild) (jobs.Step, error) {
		return jobs.NewStep(rb.Step.ID, rb.Step.Title, jobs.ResourceRDSTask, func(_ context.Context, r jobs.Reporter) (string, error) {
			r.Task(jobs.TaskStatus{ID: 9, Type: "RESTORE_DB", Lifecycle: mssql.Success, Percent: 100})
			return "task 9 completed", restoreErr
		}), nil
	})
	a.jobRegistry = reg
	return built
}

func TestJobsResume(t *testing.T) {
	dir, id := interruptedImport(t)
	a, _, _ := testApp(t, testConfig)
	built := resumable(a, nil)
	code, out, errOut := run(a, "jobs", "resume", id, "--dir", dir)
	if code != 0 {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if *built != "dev-sql with s3cret" {
		t.Fatalf("env built %q", *built)
	}
	// Progress events are best effort (never replayed, skipped for a late or
	// slow subscriber): the printer has its own test, so none is required here.
	for _, want := range []string{
		"job.resumed", "Resumed: 1 database(s) to finish, 0 kept as they were.",
		"step.started", "sales/upload",
		"resumed up-1", "task 9 SUCCESS 100%", "job.succeeded",
		"Job " + id + " succeeded. Detail: dialga jobs show " + id,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	// The journal is free again and has the end.
	code, out, _ = run(a, "-o", "json", "jobs", "show", id, "--dir", dir)
	var job jobs.Job
	if code != 0 || json.Unmarshal([]byte(out), &job) != nil || job.State != jobs.Succeeded {
		t.Fatalf("after resume: %d %s", code, out)
	}
	if code, _, errOut := run(a, "jobs", "resume", id, "--dir", dir); code != exitFailure || !strings.Contains(errOut, "is succeeded: only an interrupted job") {
		t.Fatalf("resume twice: %d %q", code, errOut)
	}
}

func TestJobsResumeJSONAndFailure(t *testing.T) {
	dir, id := interruptedImport(t)
	a, _, _ := testApp(t, testConfig)
	resumable(a, errors.New("RESTORE_DB ended in ERROR"))
	code, out, _ := run(a, "-o", "json", "jobs", "resume", id, "--dir", dir)
	var job jobs.Job
	if code != exitTaskFailed || json.Unmarshal([]byte(out), &job) != nil || job.State != jobs.Failed || job.Items[0].Error != "RESTORE_DB ended in ERROR" {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestJobsResumeProtected(t *testing.T) {
	dir, id := interruptedImport(t)
	doc := strings.Replace(testConfig, "    bucket:", "    protected: true\n    bucket:", 1)
	a, _, _ := testApp(t, doc)
	resumable(a, nil)
	a.readConfirm = func(string) (string, error) { return "", errors.New("stdin is not a terminal") }
	if code, _, errOut := run(a, "jobs", "resume", id, "--dir", dir); code != exitFailure || !strings.Contains(errOut, "pass --confirm dev-sql") {
		t.Fatalf("no terminal: %d %q", code, errOut)
	}
	if code, _, errOut := run(a, "jobs", "resume", id, "--dir", dir, "--confirm", "dev"); code != exitFailure || !strings.Contains(errOut, "not confirmed") {
		t.Fatalf("wrong name: %d %q", code, errOut)
	}
	var prompt string
	a.readConfirm = func(p string) (string, error) { prompt = p; return "dev-sql", nil }
	if code, _, errOut := run(a, "jobs", "resume", id, "--dir", dir); code != 0 || prompt != `Retype "dev-sql" to resume the import on dev-sql: ` {
		t.Fatalf("retyped: %d %q, prompt %q", code, errOut, prompt)
	}
}

func TestJobsResumeRefusals(t *testing.T) {
	dir, id := interruptedImport(t)
	other := strings.Replace(testConfig, "instance: dev-sqlserver", "instance: other-sqlserver", 1)
	tests := []struct {
		name  string
		doc   string
		setup func(a *app)
		args  []string
		want  string
	}{
		{"no pipeline", testConfig, func(*app) {}, []string{id}, "needs the import/export pipeline"},
		{"not resumable", testConfig, func(a *app) { resumable(a, nil); a.jobRegistry = jobs.NewRegistry() }, []string{id}, `the job cannot be resumed: database sales, step upload: no factory rebuilds steps of kind "upload"`},
		{"other instance", other, func(a *app) { resumable(a, nil) }, []string{id}, "now points at instance other-sqlserver"},
		{"unknown job", testConfig, func(a *app) { resumable(a, nil) }, []string{"20200101T000000Z-00000000"}, "not found in " + dir},
		{"invalid id", testConfig, func(a *app) { resumable(a, nil) }, []string{"../x"}, "invalid job id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := testApp(t, tc.doc)
			tc.setup(a)
			args := append(append([]string{"jobs", "resume"}, tc.args...), "--dir", dir)
			if code, _, errOut := run(a, args...); code != exitFailure || !strings.Contains(errOut, tc.want) {
				t.Fatalf("code %d, stderr %q, want %q", code, errOut, tc.want)
			}
		})
	}
}

func TestEventPrinterPrintsEveryTenPercent(t *testing.T) {
	var out strings.Builder
	p := &eventPrinter{w: &out, deciles: map[string]int64{}}
	progress := func(item string, done int64) jobs.Event {
		return jobs.Event{Type: jobs.EventUploadProgress, Item: item, Step: "upload",
			Transfer: &jobs.TransferProgress{Direction: jobs.Upload, Bytes: done << 20, Total: 100 << 20}}
	}
	for _, ev := range []jobs.Event{progress("a", 3), progress("a", 8), progress("b", 5), progress("a", 15),
		progress("a", 19), progress("a", 47), progress("a", 100), {Type: jobs.EventLog, Item: "a", Level: jobs.Warn, Message: "slow"}} {
		p.print(ev)
	}
	got := out.String()
	for _, want := range []string{"3.0 MiB", "5.0 MiB", "15.0 MiB", "47.0 MiB", "100.0 MiB", "warn: slow"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"8.0 MiB", "19.0 MiB"} {
		if strings.Contains(got, not) {
			t.Errorf("%q printed within the same 10%%:\n%s", not, got)
		}
	}
}

// An import interrupted mid-upload resumes from the multipart upload the
// first run left, with the pipeline's own steps.
func TestJobsResumeImportWithThePipeline(t *testing.T) {
	a, rds, s3 := jobApp(t, testConfig)
	started := make(chan struct{})
	var once sync.Once
	s3.OnUploadPart = func(ctx context.Context, _ int32, _ int) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	cfg, _, _ := a.loadConfig()
	name, tgt, _ := cfg.Target("")
	env := pipeline.Env{Name: name, Target: tgt, S3: transfer.Client(s3), TaskInterval: time.Millisecond,
		OpenOps: func(ctx context.Context) (*ops.Session, func(), error) { return a.openOps(ctx, name, tgt, "") }}
	p, err := pipeline.Build(context.Background(), env, pipeline.Request{Kind: jobs.Import,
		Import: []pipeline.ImportItem{{Files: []string{localFile(t, "sales.bak", "sales backup")}, Database: "sales"}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := p.Start(m, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s3.OnUploadPart = nil

	a.jobEnv, a.jobInterval = a.pipelineJobEnv, time.Millisecond
	code, out, errOut := run(a, "jobs", "resume", job.ID, "--dir", dir)
	if code != 0 {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{"job.resumed", "task 1 SUCCESS", "job.succeeded", "Job " + job.ID + " succeeded."} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if s3.Calls("CreateMultipartUpload") != 1 || s3.Calls("ListParts") != 1 || len(rds.tasks) != 1 ||
		string(s3.Get("dev-sqlserver-backups", "import/sales.bak").Data) != "sales backup" || rds.states["sales"] != "ONLINE" {
		t.Fatalf("multipart uploads %d, ListParts %d, tasks %d", s3.Calls("CreateMultipartUpload"), s3.Calls("ListParts"), len(rds.tasks))
	}
}
