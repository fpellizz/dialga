package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

var factories = map[string]jobs.Factory{
	KindUpload: rebuildUpload, KindTask: rebuildTask, KindWait: rebuildWait, KindPost: rebuildPost, KindDownload: rebuildDownload,
}

// rebuildOf is what Manager.Resume hands to a factory for the step of an item.
func rebuildOf(t *testing.T, env any, st jobs.StepInfo, params any) jobs.Rebuild {
	t.Helper()
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		st.Params = data
	}
	return jobs.Rebuild{Step: st, Item: jobs.Item{Name: "sales"}, Env: env}
}

func TestFactoriesRebuildTheSteps(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	tests := []struct {
		kind   string
		step   jobs.StepInfo
		params any
		res    jobs.Resource
	}{
		{KindUpload, jobs.StepInfo{ID: "upload", Title: "Upload sales.bak"}, UploadParams{Bucket: testBucket, Files: []UploadFile{{Path: "/x/sales.bak", Key: "import/sales.bak"}}}, jobs.ResourceTransfer},
		{KindTask, jobs.StepInfo{ID: "restore", Title: "Restore sales"}, TaskParams{Statement: "exec msdb.dbo.rds_restore_database", Database: "sales"}, jobs.ResourceRDSTask},
		{KindTask, jobs.StepInfo{ID: "export", Title: "Export sales"}, TaskParams{Statement: "exec msdb.dbo.rds_backup_database", Database: "sales"}, jobs.ResourceRDSTask},
		{KindWait, jobs.StepInfo{ID: "wait", Title: "Wait for the restore task"}, WaitParams{What: "restore"}, jobs.ResourceRDSTask},
		{KindPost, jobs.StepInfo{ID: "post", Title: "Post-restore report of sales (read-only)"}, PostParams{Database: "sales"}, jobs.NoResource},
		{KindDownload, jobs.StepInfo{ID: "download", Title: "Download sales.bak"}, DownloadParams{Database: "sales", Bucket: testBucket, Key: "export/sales.bak", Path: "/x/sales.bak"}, jobs.ResourceTransfer},
	}
	for _, tc := range tests {
		t.Run(tc.step.ID, func(t *testing.T) {
			f := factories[tc.kind]
			step, err := f(rebuildOf(t, env, tc.step, tc.params))
			if err != nil || step == nil {
				t.Fatalf("step %v, error %v", step, err)
			}
			rs, ok := step.(jobs.Resumable)
			if !ok || rs.Kind() != tc.kind || step.ID() != tc.step.ID || step.Title() != tc.step.Title || step.Resource() != tc.res {
				t.Fatalf("rebuilt %s %q (%s), resumable %v", step.ID(), step.Title(), step.Resource(), ok)
			}
			if got, want := mustJSON(t, rs.Params()), mustJSON(t, tc.params); got != want {
				t.Fatalf("params %s, want %s", got, want)
			}
			// The same factory refuses what it cannot build from.
			if _, err := f(rebuildOf(t, "not an env", tc.step, tc.params)); err == nil || !strings.Contains(err.Error(), "not a pipeline.Env") {
				t.Fatalf("wrong environment: %v", err)
			}
			if _, err := f(rebuildOf(t, env, tc.step, nil)); err == nil || !strings.Contains(err.Error(), "recorded no params") {
				t.Fatalf("no params: %v", err)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestoreQueuedBeforeTheInterruptIsNotQueuedAgain(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	f := rebuildTask
	// Not even the params are needed: the task is on the step.
	step, err := f(jobs.Rebuild{Step: jobs.StepInfo{ID: "restore", TaskID: 7}, Env: env})
	if step != nil || err != nil {
		t.Fatalf("step %v, error %v: want nothing left to do", step, err)
	}
	// Without a task it queues again, but not with an empty statement.
	_, err = f(rebuildOf(t, env, jobs.StepInfo{ID: "restore"}, TaskParams{Database: "sales"}))
	if err == nil || !strings.Contains(err.Error(), "no statement") {
		t.Fatalf("empty statement: %v", err)
	}
}

func TestResumedUploadSkipsTheFilesDone(t *testing.T) {
	env, _, s3 := testEnv(t, testTarget)
	done := backup(t, "a.bak", "already in s3")
	todo := backup(t, "b.bak", "still to upload")
	params := UploadParams{Bucket: testBucket, Files: []UploadFile{
		{Path: done, Key: "import/a.bak", Size: 13, Done: true},
		{Path: todo, Key: "import/b.bak"},
	}}
	f := rebuildUpload
	step, err := f(rebuildOf(t, env, jobs.StepInfo{ID: "upload", Title: "Upload"}, params))
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(jobs.Options{})
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	job, err := m.Start(jobs.Spec{Kind: jobs.Import, Target: "dev-sql", Instance: "dev-sqlserver",
		Items: []jobs.ItemSpec{{Name: "sales", Steps: []jobs.Step{step}}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err = m.Wait(context.Background(), job.ID)
	if err != nil || job.State != jobs.Succeeded {
		t.Fatalf("job %s, %v: %+v", job.State, err, job.Items)
	}
	if s3.Get(testBucket, "import/a.bak") != nil || string(s3.Get(testBucket, "import/b.bak").Data) != "still to upload" ||
		s3.Calls("CreateMultipartUpload") != 1 {
		t.Fatalf("the files done were uploaded again (%d multipart uploads)", s3.Calls("CreateMultipartUpload"))
	}
	if !strings.Contains(job.Items[0].Steps[0].Detail, "a.bak was uploaded before the interrupt") {
		t.Fatalf("detail %q", job.Items[0].Steps[0].Detail)
	}
}

// restartOn stops the engine of m (a Close, as on Ctrl-C) and opens the
// journal again: the interrupted job is what a resume finds.
func restartOn(t *testing.T, dir string, m *jobs.Manager) *jobs.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m2, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m2.Close(context.Background()) })
	return m2
}

func resumeAndWait(t *testing.T, m *jobs.Manager, id string, env Env) jobs.Job {
	t.Helper()
	if _, err := m.Resume(id, jobs.ResumeOptions{Env: env}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitJob(t *testing.T, what string, m *jobs.Manager, id string, cond func(jobs.Job) bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if job, err := m.Get(id); err == nil && cond(job) {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestResumeAfterTheUploadWasInterrupted(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	started := heldUpload(s3)
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "sales backup")}, Database: "sales"}))
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
	m = restartOn(t, dir, m)
	if j, _ := m.Get(job.ID); j.State != jobs.Interrupted || s3.Uploads() != 1 {
		t.Fatalf("job %s, open uploads %d", j.State, s3.Uploads())
	}
	s3.OnUploadPart = nil
	end := resumeAndWait(t, m, job.ID, env)
	if end.State != jobs.Succeeded || stepStatuses(end.Items[0]) != "upload:succeeded,restore:succeeded,wait:succeeded,post:succeeded" {
		t.Fatalf("job %s: %+v", end.State, end.Items)
	}
	// It went on with the multipart upload of the first run.
	if s3.Calls("CreateMultipartUpload") != 1 || s3.Calls("ListParts") != 1 || len(rds.statements) != 1 ||
		string(s3.Get(testBucket, "import/sales.bak").Data) != "sales backup" || rds.states["sales"] != "ONLINE" {
		t.Fatalf("multipart uploads %d, ListParts %d, restores %d", s3.Calls("CreateMultipartUpload"), s3.Calls("ListParts"), len(rds.statements))
	}
}

func TestResumeWhileWaitingFollowsTheSameTask(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.hold["sales"] = true
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "s")}, Database: "sales"}))
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
	waitJob(t, "the wait", m, job.ID, func(j jobs.Job) bool {
		return len(j.Items[0].Steps) == 4 && j.Items[0].Steps[2].Status == jobs.StepRunning
	})
	m = restartOn(t, dir, m)
	rds.mu.Lock()
	rds.hold["sales"] = false
	rds.mu.Unlock()
	end := resumeAndWait(t, m, job.ID, env)
	if end.State != jobs.Succeeded || stepStatuses(end.Items[0]) != "upload:succeeded,restore:succeeded,wait:succeeded,post:succeeded" ||
		len(rds.statements) != 1 || len(end.Items[0].TaskIDs) != 1 || end.Items[0].Task.Lifecycle != mssql.Success {
		t.Fatalf("job %s, %d restore(s), tasks %v: %+v", end.State, len(rds.statements), end.Items[0].TaskIDs, end.Items)
	}
}

func TestResumeAfterTheRestoreWasQueuedDoesNotRestoreAgain(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	// The sessions are opened by the restore, then by the wait: the engine
	// stops before the wait read anything.
	open := env.OpenOps
	env.OpenOps = func(ctx context.Context) (*ops.Session, func(), error) {
		rds.mu.Lock()
		queued := len(rds.statements) > 0
		rds.mu.Unlock()
		if queued {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return open(ctx)
	}
	p, err := Build(context.Background(), env, importOf(ImportItem{Files: []string{backup(t, "sales.bak", "s")}, Database: "sales"}))
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
	waitJob(t, "the wait", m, job.ID, func(j jobs.Job) bool { return j.Items[0].Steps[2].Status == jobs.StepRunning })
	m = restartOn(t, dir, m)
	interrupted, _ := m.Get(job.ID)
	if st := interrupted.Items[0].Steps[1]; st.Status != jobs.StepSucceeded || len(rds.statements) != 1 {
		t.Fatalf("restore %s, %d statement(s)", st.Status, len(rds.statements))
	}
	env.OpenOps = open
	end := resumeAndWait(t, m, job.ID, env)
	if end.State != jobs.Succeeded || len(rds.statements) != 1 || rds.states["sales"] != "ONLINE" {
		t.Fatalf("job %s, %d restore(s): %+v", end.State, len(rds.statements), end.Items)
	}
}
