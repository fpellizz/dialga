package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

const testBucket = "dev-sqlserver-backups"

var testTarget = config.Target{
	AccountID: "123456789012", Region: "eu-west-1", AWSProfile: "default", Instance: "dev-sqlserver",
	Bucket: testBucket, Prefixes: config.Prefixes{Import: "import", Export: "export"}, MaxTasks: 2,
	SQL: config.SQL{User: "admin", Password: "prompt"}, Connect: config.Connect{Mode: config.ModeExternal, LocalPort: 11433},
}

var arnPattern = regexp.MustCompile(`arn:aws:s3:::[^/']+/([^']+)`)

// fakeRDS is an RDS instance in memory: databases and native tasks that end
// on their second read. A restore creates its database when it ends, a
// backup puts its file in the bucket.
type fakeRDS struct {
	mu     sync.Mutex
	s3     *transfertest.S3
	states map[string]string
	tasks  map[int]*mssql.Task
	reads  map[int]int
	// end is how the task of a database ends (default SUCCESS); hold makes
	// its reads wait for the context to end.
	end  map[string]mssql.Lifecycle
	hold map[string]bool
	// report answers DatabaseReport (default: online, accessible).
	report     func(db string) (mssql.DatabaseReport, error)
	statements []string
	stmts      map[int]string // by task
	running    int            // native tasks not ended
	maxRunning int
	opens      int // sessions opened
}

func newFakeRDS(s3 *transfertest.S3) *fakeRDS {
	return &fakeRDS{s3: s3, states: map[string]string{}, tasks: map[int]*mssql.Task{}, reads: map[int]int{},
		end: map[string]mssql.Lifecycle{}, hold: map[string]bool{}, stmts: map[int]string{}}
}

func (f *fakeRDS) DatabaseState(_ context.Context, db string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[db], nil
}

func (f *fakeRDS) ActiveTasks(context.Context, string) (int, error) { return 0, nil }

func (f *fakeRDS) StartTask(_ context.Context, stmt, db string) (mssql.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	typ := "RESTORE_DB"
	if strings.Contains(stmt, "rds_backup_database") {
		typ = "BACKUP_DB"
	}
	t := &mssql.Task{ID: len(f.tasks) + 1, Type: typ, Database: db, Lifecycle: mssql.Created, Info: "[2026-10-16 12:00:00] Task created."}
	f.tasks[t.ID] = t
	f.statements = append(f.statements, stmt)
	f.stmts[t.ID] = stmt
	f.running++
	f.maxRunning = max(f.maxRunning, f.running)
	return *t, nil
}

func (f *fakeRDS) Exec(context.Context, string) error { return nil }

func (f *fakeRDS) DatabaseReport(_ context.Context, db string) (mssql.DatabaseReport, error) {
	if f.report != nil {
		return f.report(db)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states[db] == "" {
		return mssql.DatabaseReport{}, mssql.ErrNoDatabase
	}
	return mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: db, State: f.states[db], CompatibilityLevel: 140}, Access: true}, nil
}

func (f *fakeRDS) List(context.Context, mssql.TaskFilter) ([]mssql.Task, error) { return nil, nil }

func (f *fakeRDS) Get(ctx context.Context, id int) (mssql.Task, error) {
	f.mu.Lock()
	t := f.tasks[id]
	if t == nil {
		f.mu.Unlock()
		return mssql.Task{}, mssql.ErrTaskNotFound
	}
	if f.hold[t.Database] {
		f.mu.Unlock()
		<-ctx.Done()
		return mssql.Task{}, ctx.Err()
	}
	defer f.mu.Unlock()
	f.reads[id]++
	switch {
	case t.Lifecycle.Done():
	case f.reads[id] == 1:
		t.Lifecycle, t.Percent = mssql.InProgress, 50
	default:
		t.Lifecycle, t.Percent = mssql.Success, 100
		if end, ok := f.end[t.Database]; ok {
			t.Lifecycle = end
		}
		f.running--
		if t.Lifecycle == mssql.Success {
			f.complete(t)
		}
	}
	return *t, nil
}

// complete does what RDS does when a task succeeds (f.mu held).
func (f *fakeRDS) complete(t *mssql.Task) {
	switch {
	case t.Type == "BACKUP_DB":
		key := arnPattern.FindStringSubmatch(f.stmts[t.ID])[1]
		f.s3.Put(testBucket, key, []byte("backup of "+t.Database), types.ChecksumAlgorithmCrc64nvme)
	case strings.Contains(f.stmts[t.ID], "@with_norecovery = 1"):
		f.states[t.Database] = "RESTORING"
	default:
		f.states[t.Database] = "ONLINE"
	}
}

// bucket is the ops view of the fake bucket.
type bucket struct{ s3 *transfertest.S3 }

func (b bucket) Size(_ context.Context, key string) (int64, error) {
	if o := b.s3.Get(testBucket, key); o != nil {
		return int64(len(o.Data)), nil
	}
	return 0, awsx.ErrObjectNotFound
}

func (b bucket) Keys(context.Context, string) ([]string, error) { return nil, nil }

func testEnv(t *testing.T, target config.Target) (Env, *fakeRDS, *transfertest.S3) {
	t.Helper()
	s3 := transfertest.New()
	rds := newFakeRDS(s3)
	env := Env{
		Name: "dev-sql", Target: target, S3: s3, TaskInterval: time.Millisecond,
		OpenOps: func(context.Context) (*ops.Session, func(), error) {
			rds.mu.Lock()
			rds.opens++
			rds.mu.Unlock()
			return &ops.Session{Name: "dev-sql", Target: target, SQL: rds, Bucket: bucket{s3},
				Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }}, func() {}, nil
		},
	}
	return env, rds, s3
}

func backup(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func importOf(items ...ImportItem) Request { return Request{Kind: jobs.Import, Import: items} }

func actionIDs(it ItemPlan) string {
	var ids []string
	for _, a := range it.Actions {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ",")
}

func checkSteps(it ItemPlan) string {
	var s []string
	for _, c := range it.Checks {
		s = append(s, c.Step+":"+string(c.Status))
	}
	return strings.Join(s, ",")
}

func TestBuildImport(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	req := importOf(
		ImportItem{Files: []string{backup(t, "sales.bak", "sales backup")}, Database: "sales"},
		ImportItem{Files: []string{backup(t, "hr.bak", "hr backup")}, Database: "hr", Key: "restore/hr.bak", NoRecovery: true},
	)
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready || p.Confirmation != "" || len(p.Items) != 2 || len(p.Failed()) != 0 {
		t.Fatalf("plan %+v", p)
	}
	sales, hr := p.Items[0], p.Items[1]
	if actionIDs(sales) != "upload,restore,wait,post" || actionIDs(hr) != "upload,restore,wait" {
		t.Fatalf("actions %s / %s", actionIDs(sales), actionIDs(hr))
	}
	// The source check of the restore is left out: the upload puts it there.
	if checkSteps(sales) != "upload:ok,database:ok,tasks:ok" || sales.Key != "import/sales.bak" || hr.Key != "restore/hr.bak" {
		t.Fatalf("checks %s, keys %s %s", checkSteps(sales), sales.Key, hr.Key)
	}
	for _, want := range []string{
		"Dry run: import of 2 database(s) on dev-sql (instance dev-sqlserver). Nothing is changed.",
		"PUT s3://dev-sqlserver-backups/import/sales.bak · multipart, 1 part, CRC64NVME",
		"@s3_arn_to_restore_from = N'arn:aws:s3:::dev-sqlserver-backups/restore/hr.bak',",
		"@with_norecovery = 1;",
		"4. Post-restore report of sales (read-only)",
	} {
		if !strings.Contains(p.Preview, want) {
			t.Errorf("preview misses %q:\n%s", want, p.Preview)
		}
	}
	if ids := p.Core().Actions; len(ids) != 7 || ids[0].ID != "sales/upload" || ids[6].ID != "hr/wait" {
		t.Fatalf("job actions %+v", ids)
	}
	again, _ := Build(context.Background(), env, req)
	if again.Digest != p.Digest {
		t.Fatal("the digest of the same plan changed")
	}
	if rds.opens != 2 || s3.Calls("CreateMultipartUpload") != 0 || len(rds.statements) != 0 {
		t.Fatal("the pre-flight changed something")
	}
}

func TestBuildRefusesOrSkipsFailingItems(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["hr"] = "ONLINE"
	req := importOf(
		ImportItem{Files: []string{backup(t, "sales.bak", "x")}, Database: "sales"},
		ImportItem{Files: []string{backup(t, "hr.bak", "y")}, Database: "hr"},
		ImportItem{Files: []string{filepath.Join(t.TempDir(), "missing.bak")}, Database: "ops"},
	)
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	failed := strings.Join(p.Failed(), "\n")
	if p.Ready || !strings.Contains(failed, "hr: Target database: hr already exists (ONLINE)") ||
		!strings.Contains(failed, "ops: Upload to S3: cannot read it") {
		t.Fatalf("ready %v, failed:\n%s", p.Ready, failed)
	}
	if _, err := p.Start(jobs.New(jobs.Options{}), ""); !errors.Is(err, ErrNotReady) {
		t.Fatalf("start: %v", err)
	}

	req.SkipFailing = true
	p, err = Build(context.Background(), env, req)
	if err != nil || !p.Ready || p.Items[0].Skipped || !p.Items[1].Skipped || !p.Items[2].Skipped ||
		!strings.Contains(p.Preview, "[skipped: a pre-flight check failed]") {
		t.Fatalf("skip failing: %+v, %v", p, err)
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, "")
	if err != nil {
		t.Fatal(err)
	}
	job, _ = m.Wait(context.Background(), job.ID)
	if job.State != jobs.Succeeded || len(job.Items) != 1 || job.Items[0].Name != "sales" {
		t.Fatalf("job %+v", job.Summary())
	}
}

func TestRequestValidation(t *testing.T) {
	abs := backup(t, "sales.bak", "x")
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{"no kind", Request{}, "kind must be import or export"},
		{"no items", Request{Kind: jobs.Import}, "no databases"},
		{"relative file", importOf(ImportItem{Files: []string{"sales.bak"}, Database: "sales"}), "absolute path"},
		{"bad database", importOf(ImportItem{Files: []string{abs}, Database: "x'y"}), "invalid database name"},
		{"system database", importOf(ImportItem{Files: []string{abs}, Database: "msdb"}), "system database"},
		{"twice", importOf(ImportItem{Files: []string{abs}, Database: "Sales"}, ImportItem{Files: []string{abs}, Database: "sales"}), "twice"},
		{"export items in an import", Request{Kind: jobs.Import, Export: []ExportItem{{Database: "a"}}}, "import items only"},
		{"relative destination", Request{Kind: jobs.Export, Export: []ExportItem{{Database: "a", Destination: "out"}}}, "absolute path"},
		{"export key with *", Request{Kind: jobs.Export, Export: []ExportItem{{Database: "a", Destination: "/tmp", Key: "export/a*.bak"}}}, "no * allowed"},
		{"upload key with a quote", importOf(ImportItem{Files: []string{abs}, Database: "sales", Key: "import/it's.bak"}), "no spaces or quotes"},
	}
	env, _, _ := testEnv(t, testTarget)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(context.Background(), env, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	many := Request{Kind: jobs.Export}
	for i := range MaxItems + 1 {
		many.Export = append(many.Export, ExportItem{Database: fmt.Sprintf("db%d", i), Destination: "/tmp"})
	}
	if err := many.Validate(testTarget); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("%d items: %v", MaxItems+1, err)
	}
}

func TestConfirmationOncePerJob(t *testing.T) {
	prod := testTarget
	prod.Protected = true
	env, _, _ := testEnv(t, prod)
	req := importOf(ImportItem{Files: []string{backup(t, "a.bak", "a")}, Database: "a"}, ImportItem{Files: []string{backup(t, "b.bak", "b")}, Database: "b"})
	p, err := Build(context.Background(), env, req)
	if err != nil || p.Confirmation != "dev-sql" {
		t.Fatalf("protected: %q, %v", p.Confirmation, err)
	}
	if _, err := p.Start(jobs.New(jobs.Options{}), "prod"); !errors.Is(err, core.ErrNotConfirmed) {
		t.Fatalf("wrong confirmation: %v", err)
	}

	env, _, s3 := testEnv(t, testTarget)
	s3.Put(testBucket, "import/a.bak", []byte("old"), "")
	req.Import[0].Overwrite = true
	if p, _ := Build(context.Background(), env, req); p.Confirmation != "import/a.bak" || p.Items[0].Actions[0].Risk != core.Destructive {
		t.Fatalf("one object replaced: %q", p.Confirmation)
	}
	s3.Put(testBucket, "import/b.bak", []byte("old"), "")
	req.Import[1].Overwrite = true
	if p, _ := Build(context.Background(), env, req); p.Confirmation != "replace 2 files" {
		t.Fatalf("two objects replaced: %q", p.Confirmation)
	}
	req.Import[1].Overwrite = false
	if p, _ := Build(context.Background(), env, req); p.Ready || !strings.Contains(strings.Join(p.Failed(), ""), "choose overwrite") {
		t.Fatalf("existing object without overwrite: %v", p.Failed())
	}
}

// runJob starts the plan of req and waits for its job to end.
func runJob(t *testing.T, env Env, req Request) jobs.Job {
	t.Helper()
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready {
		t.Fatalf("not ready: %v", p.Failed())
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, p.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err = m.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func stepStatuses(it jobs.Item) string {
	var s []string
	for _, st := range it.Steps {
		s = append(s, st.ID+":"+string(st.Status))
	}
	return strings.Join(s, ",")
}

func TestImportJob(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	job := runJob(t, env, importOf(
		ImportItem{Files: []string{backup(t, "sales.bak", "sales backup")}, Database: "sales"},
		ImportItem{Files: []string{backup(t, "hr.bak", "hr backup")}, Database: "hr", NoRecovery: true},
	))
	if job.State != jobs.Succeeded {
		t.Fatalf("job %s: %+v", job.State, job.Items)
	}
	sales, hr := job.Items[0], job.Items[1]
	if stepStatuses(sales) != "upload:succeeded,restore:succeeded,wait:succeeded,post:succeeded" ||
		stepStatuses(hr) != "upload:succeeded,restore:succeeded,wait:succeeded" {
		t.Fatalf("steps %s / %s", stepStatuses(sales), stepStatuses(hr))
	}
	if sales.Result != "sales restored: ONLINE, compatibility level 140" || !strings.Contains(hr.Result, "hr stays RESTORING") {
		t.Fatalf("results %q / %q", sales.Result, hr.Result)
	}
	if !strings.Contains(sales.Steps[0].Detail, "CRC64NVME verified") || sales.Upload == nil || sales.Upload.UploadID == "" ||
		!slices.Equal(sales.Upload.PartsDone, []int{1}) {
		t.Fatalf("upload %+v, transfer %+v, detail %q", sales.Upload, sales.Transfer, sales.Steps[0].Detail)
	}
	if sales.Task == nil || sales.Task.Lifecycle != mssql.Success || len(sales.TaskIDs) != 1 {
		t.Fatalf("task %+v", sales.Task)
	}
	if string(s3.Get(testBucket, "import/sales.bak").Data) != "sales backup" || rds.states["sales"] != "ONLINE" || rds.states["hr"] != "RESTORING" {
		t.Fatal("the instance is not as imported")
	}
}

func TestNativeTasksStayWithinTheLimit(t *testing.T) {
	for _, maxTasks := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_tasks %d", maxTasks), func(t *testing.T) {
			target := testTarget
			target.MaxTasks = maxTasks
			env, rds, _ := testEnv(t, target)
			var items []ImportItem
			for i := range 5 {
				name := fmt.Sprintf("db%d", i)
				items = append(items, ImportItem{Files: []string{backup(t, name+".bak", name)}, Database: name})
			}
			if job := runJob(t, env, importOf(items...)); job.State != jobs.Succeeded {
				t.Fatalf("job %s", job.State)
			}
			// restore + wait hold one RDS slot from the submit to the end of the task.
			if rds.maxRunning > maxTasks || len(rds.statements) != 5 {
				t.Fatalf("%d tasks at once, limit %d", rds.maxRunning, maxTasks)
			}
		})
	}
}

func TestFailedItemsDoNotStopTheOthers(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.end["sales"] = mssql.Error
	rds.report = func(db string) (mssql.DatabaseReport, error) {
		return mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: db, State: "ONLINE"}, Access: db != "hr"}, nil
	}
	job := runJob(t, env, importOf(
		ImportItem{Files: []string{backup(t, "sales.bak", "s")}, Database: "sales"},
		ImportItem{Files: []string{backup(t, "hr.bak", "h")}, Database: "hr"},
		ImportItem{Files: []string{backup(t, "ops.bak", "o")}, Database: "ops"},
	))
	sales, hr, opsItem := job.Items[0], job.Items[1], job.Items[2]
	if job.State != jobs.Failed || sales.State != jobs.Failed || !regexp.MustCompile(`^task \d ended in ERROR: \[2026-10-16 12:00:00\] Task created\.$`).MatchString(sales.Error) ||
		stepStatuses(sales) != "upload:succeeded,restore:succeeded,wait:failed,post:skipped" {
		t.Fatalf("sales %s %q", stepStatuses(sales), sales.Error)
	}
	if hr.State != jobs.Failed || !strings.Contains(hr.Error, "Login access: admin cannot enter hr") || opsItem.State != jobs.Succeeded {
		t.Fatalf("hr %s %q, ops %s", hr.State, hr.Error, opsItem.State)
	}
}

func TestExportJob(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	rds.states["hr"] = "ONLINE"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hr.bak"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := Request{Kind: jobs.Export, Export: []ExportItem{
		{Database: "sales", Destination: dir},
		{Database: "hr", Key: "export/hr.bak", Destination: dir, Overwrite: true},
	}}
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Key != "export/sales_20261016-120000.bak" || actionIDs(p.Items[0]) != "export,wait,download" ||
		p.Items[1].File != filepath.Join(dir, "hr.bak") || p.Confirmation != "hr.bak" {
		t.Fatalf("plan items %+v, confirmation %q", p.Items, p.Confirmation)
	}
	job := runJob(t, env, req)
	if job.State != jobs.Succeeded || stepStatuses(job.Items[0]) != "export:succeeded,wait:succeeded,download:succeeded" {
		t.Fatalf("job %s %s", job.State, stepStatuses(job.Items[0]))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "sales_20261016-120000.bak"))
	replaced, _ := os.ReadFile(filepath.Join(dir, "hr.bak"))
	if string(got) != "backup of sales" || string(replaced) != "backup of hr" || s3.Get(testBucket, "export/hr.bak") == nil {
		t.Fatalf("downloads %q %q", got, replaced)
	}
	if job.Items[1].Transfer == nil || job.Items[1].Transfer.Direction != jobs.Download {
		t.Fatalf("transfer %+v", job.Items[1].Transfer)
	}

	// Without overwrite, an existing local file fails the pre-flight.
	req.Export[1].Overwrite = false
	if p, _ := Build(context.Background(), env, req); p.Ready || !strings.Contains(strings.Join(p.Failed(), ""), "hr.bak already exists") {
		t.Fatalf("existing file: %v", p.Failed())
	}
}

// heldUpload is an S3 whose parts hang until their context ends.
func heldUpload(s3 *transfertest.S3) chan struct{} {
	started := make(chan struct{})
	var once sync.Once
	s3.OnUploadPart = func(ctx context.Context, _ int32, _ int) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}
	return started
}

func startHeld(t *testing.T) (*jobs.Manager, jobs.Job, *transfertest.S3) {
	t.Helper()
	env, _, s3 := testEnv(t, testTarget)
	started := heldUpload(s3)
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "sales")}, Database: "sales"}))
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(jobs.Options{})
	job, err := p.Start(m, "")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	return m, job, s3
}

func TestCancelAbortsTheUpload(t *testing.T) {
	m, job, s3 := startHeld(t)
	if _, err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	job, _ = m.Wait(context.Background(), job.ID)
	if job.State != jobs.Cancelled || s3.Uploads() != 0 || s3.Calls("AbortMultipartUpload") != 1 {
		t.Fatalf("job %s, open uploads %d", job.State, s3.Uploads())
	}
}

func TestEngineStopKeepsTheUpload(t *testing.T) {
	m, job, s3 := startHeld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ = m.Get(job.ID)
	it := job.Items[0]
	// Interrupted, not aborted: the multipart upload waits for a resume.
	if job.State != jobs.Interrupted || s3.Uploads() != 1 || s3.Calls("AbortMultipartUpload") != 0 ||
		it.Upload == nil || it.Upload.UploadID != s3.UploadIDs()[0] || it.Steps[0].Status != jobs.StepInterrupted {
		t.Fatalf("job %s, open uploads %d, item %+v", job.State, s3.Uploads(), it)
	}
}

func TestCancelLeavesTheNativeTaskRunning(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.hold["sales"] = true
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "s")}, Database: "sales"}))
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(jobs.Options{})
	job, _ := p.Start(m, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, _ = m.Get(job.ID)
		if job.Items[0].Step == "wait" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	job, _ = m.Wait(context.Background(), job.ID)
	if it := job.Items[0]; job.State != jobs.Cancelled || !it.TaskLeftRunning || it.Task == nil || it.Task.ID != 1 {
		t.Fatalf("job %s, item %+v", job.State, it)
	}
}

func TestParseFile(t *testing.T) {
	base := t.TempDir()
	f, err := ParseFile([]byte(`apiVersion: v1
kind: Import
target: dev-sql
skipFailing: true
items:
  - { file: backups/sales.bak, database: sales }
  - { file: /data/hr.bak, database: hr, key: import/hr.bak, noRecovery: true, overwrite: true }
`), base)
	if err != nil {
		t.Fatal(err)
	}
	req := f.Request()
	if f.Target != "dev-sql" || req.Kind != jobs.Import || !req.SkipFailing || len(req.Import) != 2 ||
		!slices.Equal(req.Import[0].Files, []string{filepath.Join(base, "backups", "sales.bak")}) ||
		!reflect.DeepEqual(req.Import[1], ImportItem{Files: []string{"/data/hr.bak"}, Database: "hr", Key: "import/hr.bak", NoRecovery: true, Overwrite: true}) {
		t.Fatalf("file %+v, request %+v", f, req)
	}
	exp, err := ParseFile([]byte("apiVersion: v1\nkind: Export\nitems:\n  - { database: sales, destination: out }\n"), base)
	if err != nil || exp.Request().Export[0] != (ExportItem{Database: "sales", Destination: filepath.Join(base, "out")}) {
		t.Fatalf("export %+v, %v", exp, err)
	}
	for doc, want := range map[string]string{
		"apiVersion: v2\nkind: Import\n":                                                       `apiVersion is "v2"`,
		"apiVersion: v1\nkind: import\n":                                                       "kind must be Import or Export",
		"apiVersion: v1\nkind: Import\nitems:\n  - { database: x }\n":                          "needs file",
		"apiVersion: v1\nkind: Export\nitems:\n  - { database: x, destination: d, file: f }\n": "are for import items",
		"apiVersion: v1\nkind: Import\nitem: []\n":                                             "field item not found",
	} {
		if _, err := ParseFile([]byte(doc), base); err == nil || !strings.Contains(err.Error(), want) || !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%q: err %v, want %q", doc, err, want)
		}
	}
	path := filepath.Join(base, "plan.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Import\nitems:\n  - { file: x.bak, database: x }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := ReadFile(path); err != nil || f.Items[0].File != filepath.Join(base, "x.bak") {
		t.Fatalf("read file: %+v, %v", f, err)
	}
}

func TestPreviewShowsChecks(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	p, _ := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "s")}, Database: "sales"}))
	var want bytes.Buffer
	want.WriteString("  FAIL  Target database      sales already exists (ONLINE): choose another name or drop it first\n")
	if !strings.Contains(p.Preview, want.String()) || !strings.HasSuffix(p.Preview, "Some pre-flight checks failed: the job cannot start.\n") {
		t.Fatalf("preview:\n%s", p.Preview)
	}
	if c := p.Items[0].Checks[1]; c.Status != check.Failed {
		t.Fatalf("check %+v", c)
	}
}

func TestStripePattern(t *testing.T) {
	tests := []struct {
		keys []string
		want string
	}{
		{[]string{"import/crm_1.bak", "import/crm_2.bak"}, "import/crm_*.bak"},
		{[]string{"import/crm_01.bak", "import/crm_02.bak", "import/crm_10.bak"}, "import/crm_*.bak"},
		{[]string{"import/a.bak", "import/b.bak"}, "import/*.bak"},
		// The common start and end never overlap.
		{[]string{"import/ab.bak", "import/abb.bak"}, "import/ab*.bak"},
		{[]string{"import/x1", "import/x12"}, "import/x1*"},
	}
	for _, tc := range tests {
		got := stripePattern(tc.keys)
		pre, suf, _ := strings.Cut(got, "*")
		for _, k := range tc.keys {
			if !strings.HasPrefix(k, pre) || !strings.HasSuffix(k, suf) || len(k) < len(pre)+len(suf) {
				t.Errorf("%s does not match %s", k, got)
			}
		}
		if got != tc.want {
			t.Errorf("stripePattern(%v) = %q, want %q", tc.keys, got, tc.want)
		}
	}
}

// listing is a bucket that lists its keys too.
type listing struct{ bucket }

func (l listing) Keys(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, k := range []string{"import/crm_1.bak", "import/crm_2.bak", "import/crm_old.bak"} {
		if strings.HasPrefix(k, prefix) && l.s3.Get(testBucket, k) != nil {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestImportStripedBackup(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	open := env.OpenOps
	env.OpenOps = func(ctx context.Context) (*ops.Session, func(), error) {
		sess, closeOps, err := open(ctx)
		sess.Bucket = listing{bucket{s3}}
		return sess, closeOps, err
	}
	dir := t.TempDir()
	var files []string
	for i, part := range []string{"first stripe", "second stripe"} {
		path := filepath.Join(dir, fmt.Sprintf("crm_%d.bak", i+1))
		if err := os.WriteFile(path, []byte(part), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	req := importOf(ImportItem{Files: files, Database: "crm"})
	p, err := Build(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	it := p.Items[0]
	if !p.Ready || it.Key != "import/crm_*.bak" || len(it.Uploads) != 2 || actionIDs(it) != "upload,restore,wait,post" ||
		!strings.Contains(it.Actions[0].Title, "Upload 2 files of one backup") ||
		!strings.Contains(it.Actions[1].Statement, "N'arn:aws:s3:::dev-sqlserver-backups/import/crm_*.bak'") ||
		checkSteps(it) != "upload:ok,stripes:ok,database:ok,tasks:ok" {
		t.Fatalf("plan %+v", it)
	}
	job := runJob(t, env, req)
	if job.State != jobs.Succeeded || !strings.Contains(job.Items[0].Steps[0].Detail, "2 files") ||
		string(s3.Get(testBucket, "import/crm_2.bak").Data) != "second stripe" || rds.states["crm"] != "ONLINE" {
		t.Fatalf("job %s %+v", job.State, job.Items[0].Steps)
	}

	// Another object matching the pattern would be restored with the backup.
	s3.Put(testBucket, "import/crm_old.bak", []byte("old"), "")
	env2, _, _ := testEnv(t, testTarget)
	env2.S3 = transfertest.New()
	env2.OpenOps = env.OpenOps
	p, err = Build(context.Background(), env2, importOf(ImportItem{Files: files, Database: "crm2"}))
	if err != nil || p.Ready || !strings.Contains(strings.Join(p.Failed(), ""), "also matches import/crm_old.bak") {
		t.Fatalf("pattern clash: %v, %v", p.Failed(), err)
	}
}

func TestImportDifferentialAndPrefix(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	prefix := "restore/2026-10"
	req := Request{Kind: jobs.Import, Prefix: &prefix, Import: []ImportItem{
		{Files: []string{backup(t, "crm_diff.bak", "diff")}, Database: "crm", Differential: true},
	}}
	// A differential restore needs the database RESTORING.
	p, err := Build(context.Background(), env, req)
	if err != nil || p.Ready || !strings.Contains(strings.Join(p.Failed(), ""), "needs it RESTORING") {
		t.Fatalf("no database: %v, %v", p.Failed(), err)
	}
	rds.states["crm"] = "RESTORING"
	p, err = Build(context.Background(), env, req)
	if err != nil || !p.Ready || p.Items[0].Key != "restore/2026-10/crm_diff.bak" {
		t.Fatalf("plan %+v, %v", p, err)
	}
	if job := runJob(t, env, req); job.State != jobs.Succeeded || s3.Get(testBucket, "restore/2026-10/crm_diff.bak") == nil {
		t.Fatalf("job %s", job.State)
	}
	root := ""
	req.Prefix = &root
	rds.states["crm"] = "RESTORING"
	if p, _ := Build(context.Background(), env, req); p.Items[0].Key != "crm_diff.bak" {
		t.Fatalf("bucket root: %q", p.Items[0].Key)
	}
}

func TestExportWithoutDownload(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	req := Request{Kind: jobs.Export, Export: []ExportItem{{Database: "sales", Key: "export/sales.bak"}}}
	p, err := Build(context.Background(), env, req)
	if err != nil || actionIDs(p.Items[0]) != "export,wait" || p.Items[0].File != "" {
		t.Fatalf("plan %+v, %v", p, err)
	}
	job := runJob(t, env, req)
	if job.State != jobs.Succeeded || job.Items[0].Result != "sales exported to s3://dev-sqlserver-backups/export/sales.bak" ||
		s3.Get(testBucket, "export/sales.bak") == nil {
		t.Fatalf("job %s, result %q", job.State, job.Items[0].Result)
	}
}

func TestImportPlanCarriesTheUploads(t *testing.T) {
	env, _, s3 := testEnv(t, testTarget)
	s3.Put(testBucket, "import/sales.bak", []byte("old"), "")
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "new")}, Database: "sales"}))
	if err != nil {
		t.Fatal(err)
	}
	it := p.Items[0]
	if len(it.Uploads) != 0 || len(it.Rejected) != 1 || !strings.Contains(it.Rejected[0].Reason, "already in s3://") || it.Key != "import/sales.bak" {
		t.Fatalf("uploads %+v, rejected %+v", it.Uploads, it.Rejected)
	}
}

func TestParseFileCapsTheSize(t *testing.T) {
	big := "apiVersion: v1\nkind: Export\n# " + strings.Repeat("x", MaxFileSize) + "\n"
	if _, err := ParseFile([]byte(big), t.TempDir()); err == nil || !strings.Contains(err.Error(), "larger than 1024 KiB") {
		t.Fatalf("ParseFile: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plan.yaml")
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), "larger than 1024 KiB") {
		t.Fatalf("ReadFile: %v", err)
	}
}

func TestValidateChecksThePrefixUpFront(t *testing.T) {
	for _, prefix := range []string{"a b", "../x", "/abs", "it's"} {
		req := Request{Kind: jobs.Import, Prefix: &prefix, Import: []ImportItem{{Files: []string{"/a.bak"}, Database: "a"}}}
		if err := req.Validate(testTarget); err == nil || !strings.Contains(err.Error(), "invalid prefix") {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
	}
	ok := "imports/2026"
	req := Request{Kind: jobs.Import, Prefix: &ok, Import: []ImportItem{{Files: []string{"/a.bak"}, Database: "a"}}}
	if err := req.Validate(testTarget); err != nil {
		t.Fatal(err)
	}
}
