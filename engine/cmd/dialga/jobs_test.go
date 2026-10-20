package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

var (
	jobCreated = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	jobEnded   = jobCreated.Add(42 * time.Minute)
)

// sampleJobs: an interrupted import with what a resume needs, and an older
// export that failed on one database. Placeholders only.
func sampleJobs() []jobs.Job {
	started := jobCreated.Add(time.Minute)
	interrupted := jobs.Job{
		ID: "20261016T090000Z-0000abcd", Kind: jobs.Import, Target: "dev-sql", Instance: "dev-sqlserver", MaxTasks: 2,
		State: jobs.Interrupted, CreatedAt: jobCreated, UpdatedAt: jobEnded, EndedAt: &jobEnded,
		Items: []jobs.Item{
			{Name: "sales", State: jobs.Interrupted, Step: "upload", Percent: 40,
				Transfer: &jobs.TransferProgress{Direction: jobs.Upload, Bytes: 40 << 20, Total: 100 << 20},
				Upload:   &jobs.UploadState{Bucket: "dev-sqlserver-backups", Key: "import/sales.bak", UploadID: "up-9", Size: 100 << 20, PartSize: 10 << 20, PartsDone: []int{1, 2, 3, 4}},
				Steps: []jobs.StepInfo{
					{ID: "upload", Title: "Upload sales.bak", Resource: jobs.ResourceTransfer, Status: jobs.StepInterrupted, StartedAt: &started},
					{ID: "restore", Title: "Restore sales", Resource: jobs.ResourceRDSTask, Status: jobs.StepPending},
				}},
			{Name: "hr", State: jobs.Interrupted, Step: "wait", TaskIDs: []int{12}, TaskLeftRunning: true,
				Task: &jobs.TaskStatus{ID: 12, Type: "RESTORE_DB", Lifecycle: mssql.InProgress, Percent: 60},
				Steps: []jobs.StepInfo{
					{ID: "restore", Title: "Restore hr", Status: jobs.StepSucceeded, Detail: "task 12"},
					{ID: "wait", Title: "Wait for the restore", Status: jobs.StepInterrupted},
				}},
		},
	}
	for i, typ := range []jobs.EventType{jobs.EventJobQueued, jobs.EventJobStarted, jobs.EventStepStarted, jobs.EventLog, jobs.EventJobInterrupted} {
		ev := jobs.Event{Seq: int64(i + 1), Time: jobCreated.Add(time.Duration(i) * time.Minute), Type: typ}
		switch typ {
		case jobs.EventStepStarted:
			ev.Item, ev.Step, ev.Message = "sales", "upload", "Upload sales.bak"
		case jobs.EventLog:
			ev.Item, ev.Level, ev.Message = "hr", jobs.Warn, "Native task 12 is still running"
		case jobs.EventJobInterrupted:
			ev.Message = "The engine stopped while the job was running."
		}
		interrupted.Events = append(interrupted.Events, ev)
	}
	older := jobCreated.Add(-24 * time.Hour)
	olderEnd := older.Add(time.Hour)
	failed := jobs.Job{
		ID: "20261015T090000Z-00000001", Kind: jobs.Export, Target: "dev-sql", Instance: "dev-sqlserver", MaxTasks: 1,
		State: jobs.Failed, CreatedAt: older, UpdatedAt: olderEnd, EndedAt: &olderEnd,
		Items: []jobs.Item{
			{Name: "a", State: jobs.Succeeded, Result: "exported", Steps: []jobs.StepInfo{{ID: "backup", Title: "Backup a", Status: jobs.StepSucceeded}}},
			{Name: "b", State: jobs.Failed, Step: "backup", Error: "BACKUP_DB ended in ERROR", Steps: []jobs.StepInfo{{ID: "backup", Title: "Backup b", Status: jobs.StepFailed}}},
		},
		Events: []jobs.Event{},
	}
	return []jobs.Job{failed, interrupted}
}

func journalDir(t *testing.T, list []jobs.Job) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, j := range list {
		data, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, j.ID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestJobsList(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	dir := journalDir(t, sampleJobs())
	code, out, errOut := run(a, "jobs", "--dir", dir)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "Jobs in "+dir+", newest first") ||
		!strings.Contains(lines[2], "20261016T090000Z-0000abcd") || !strings.Contains(lines[3], "20261015T090000Z-00000001") {
		t.Fatalf("not newest first:\n%s", out)
	}
	for _, want := range []string{"interrupted", "import", "0/2 ok", "export", "1/2 ok, 1 failed",
		localTime(&jobCreated, time.DateTime[:16]), localTime(&jobEnded, time.DateTime[:16]), "dialga jobs show <id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "were interrupted if no Dialga engine") {
		t.Error("hint about unfinished jobs without any")
	}
}

func TestJobsListJSONAndUnfinished(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	list := sampleJobs()
	list[1].State, list[1].EndedAt, list[1].CancelRequested = jobs.Running, nil, true
	dir := journalDir(t, list)
	code, out, _ := run(a, "jobs", "--dir", dir)
	if code != 0 || !strings.Contains(out, "running (cancelling)") || !strings.Contains(out, "were interrupted if their process") {
		t.Fatalf("code %d:\n%s", code, out)
	}
	code, out, _ = run(a, "-o", "json", "jobs", "--dir", dir)
	var got struct {
		Journal string         `json:"journal"`
		Jobs    []jobs.Summary `json:"jobs"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("code %d, out %q", code, out)
	}
	if got.Journal != dir || len(got.Jobs) != 2 || got.Jobs[0].ID != list[1].ID || got.Jobs[1].Failed != 1 || got.Jobs[1].Databases[1] != "b" {
		t.Fatalf("json %+v", got)
	}
}

func TestJobsEmptyAndDefaultDir(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	isolateUserDirs(t)
	dir, err := jobs.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(a, "jobs")
	want := "No jobs in " + dir + " yet"
	if code != 0 || !strings.HasPrefix(out, want) {
		t.Fatalf("code %d, out %q, want prefix %q", code, out, want)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Fatal("dialga jobs created the journal directory")
	}
	code, out, _ = run(a, "-o", "json", "jobs")
	if code != 0 || !strings.Contains(out, `"jobs":[]`) {
		t.Fatalf("json: code %d, out %q", code, out)
	}
}

func TestJobsShow(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	dir := journalDir(t, sampleJobs())
	code, out, _ := run(a, "jobs", "show", "20261016T090000Z-0000abcd", "--dir", dir)
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{
		"Job 20261016T090000Z-0000abcd: import on dev-sql (instance dev-sqlserver)",
		"interrupted (the engine stopped while it ran: dialga jobs resume 20261016T090000Z-0000abcd continues it)",
		"Created  " + localTime(&jobCreated, time.DateTime),
		"Ended    " + localTime(&jobEnded, time.DateTime),
		"sales  interrupted at step upload (40%)",
		"1. upload", "Upload sales.bak", "2. restore",
		"Multipart upload: s3://dev-sqlserver-backups/import/sales.bak, upload id up-9, 4 of 10 parts done",
		"hr  interrupted at step wait",
		"Native tasks: 12; last 12 RESTORE_DB IN_PROGRESS 60%",
		"Task 12 was still running on the instance when the job stopped: dialga wait 12 -t dev-sql",
		"Events (5):", "job.queued", "sales/upload", "WARN: Native task 12 is still running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Transfer:") {
		t.Error("an ended item shows its transfer")
	}
	code, out, _ = run(a, "jobs", "show", "20261016T090000Z-0000abcd", "--dir", dir, "--events", "2")
	if code != 0 || !strings.Contains(out, "Last 2 of 5 events:") || strings.Contains(out, "job.queued") || !strings.Contains(out, "job.interrupted") {
		t.Fatalf("--events 2:\n%s", out)
	}
	code, out, _ = run(a, "jobs", "show", "20261015T090000Z-00000001", "--dir", dir)
	if code != 0 || !strings.Contains(out, "b  failed at step backup") || !strings.Contains(out, "Error: BACKUP_DB ended in ERROR") ||
		!strings.Contains(out, "Result: exported") || strings.Contains(out, "Events") {
		t.Fatalf("failed job:\n%s", out)
	}
}

func TestJobsShowJSONAndErrors(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	dir := journalDir(t, sampleJobs())
	code, out, _ := run(a, "-o", "json", "jobs", "show", "20261016T090000Z-0000abcd", "--dir", dir)
	var job jobs.Job
	if code != 0 || json.Unmarshal([]byte(out), &job) != nil || len(job.Events) != 5 || job.Items[0].Upload.UploadID != "up-9" {
		t.Fatalf("json: code %d, %q", code, out)
	}
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"jobs", "show", "../etc/passwd", "--dir", dir}, "invalid job id"},
		{[]string{"jobs", "show", "20201010T000000Z-00000000", "--dir", dir}, "not found in " + dir},
		{[]string{"jobs", "show", "20261016T090000Z-0000abcd", "--dir", dir, "--events", "-1"}, "--events must be"},
		{[]string{"jobs", "show", "--dir", dir}, "accepts 1 arg"},
	}
	for _, tc := range tests {
		if code, _, errOut := run(a, tc.args...); code != exitFailure || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: code %d, stderr %q", tc.args, code, errOut)
		}
	}
}

func TestJobsSkipsBadFiles(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	dir := journalDir(t, sampleJobs()[:1])
	if err := os.WriteFile(filepath.Join(dir, "20261016T100000Z-00000002.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(a, "jobs", "--dir", dir)
	if code != 0 || !strings.Contains(out, "20261015T090000Z-00000001") || !strings.Contains(errOut, "journal file skipped") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
}
