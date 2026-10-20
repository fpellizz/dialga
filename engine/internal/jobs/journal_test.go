package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

func openT(t *testing.T, dir string, opts Options) *Manager {
	t.Helper()
	m, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func closeT(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func readJob(t *testing.T, dir, id string) Job {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func writeJob(t *testing.T, dir string, job Job) {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, job.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestJournalRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "dialga", "jobs")
	m := openT(t, dir, Options{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{{Name: "sales", Steps: []Step{
		NewStep("upload", "Upload", ResourceTransfer, func(_ context.Context, r Reporter) (string, error) {
			r.Upload(UploadState{Bucket: "b", Key: "import/sales.bak", UploadID: "up-1", Size: 10, PartSize: 5, PartsDone: []int{1, 2}})
			r.Transfer(Upload, 10, 10)
			return "10 bytes", nil
		}),
		NewStep("restore", "Restore", ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
			r.Task(TaskStatus{ID: 3, Type: "RESTORE_DB", Lifecycle: mssql.Success, Percent: 100})
			r.Result("restored as sales")
			return "task 3", nil
		}),
	}}}})
	done := waitJob(t, m, job.ID)
	closeT(t, m)

	info, err := os.Stat(dir)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) { // no POSIX modes on Windows
		t.Fatalf("journal dir: %v %v", info.Mode(), err)
	}
	file := filepath.Join(dir, job.ID+".json")
	if info, err := os.Stat(file); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("job file: %v", err)
	}
	if tmps, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(tmps) != 0 {
		t.Fatalf("temporary files left: %v", tmps)
	}

	again := openT(t, dir, Options{})
	defer closeT(t, again)
	loaded, err := again.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, loaded) != asJSON(t, done) {
		t.Fatalf("reloaded job differs\nbefore %s\nafter  %s", asJSON(t, done), asJSON(t, loaded))
	}
	it := loaded.Items[0]
	if loaded.State != Succeeded || it.Upload.UploadID != "up-1" || !slices.Equal(it.TaskIDs, []int{3}) || it.Result != "restored as sales" {
		t.Fatalf("loaded %+v", loaded)
	}
	if _, err := again.Cancel(job.ID); !errors.Is(err, ErrEnded) {
		t.Fatalf("cancel a loaded job: %v", err)
	}
}

func TestInterruptedOnRestart(t *testing.T) {
	dir := t.TempDir()
	created := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)
	crashed := Job{
		ID: "20261016T090000Z-0000abcd", Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 2,
		State: Running, CreatedAt: created, UpdatedAt: started,
		Items: []Item{
			{Name: "uploading", State: Running, Step: "upload", Percent: 40,
				Upload: &UploadState{Bucket: "b", Key: "import/a.bak", UploadID: "up-9", Size: 100, PartSize: 10, PartsDone: []int{1, 2, 3, 4}},
				Steps: []StepInfo{
					{ID: "upload", Title: "Upload", Resource: ResourceTransfer, Status: StepRunning, StartedAt: &started},
					{ID: "restore", Title: "Restore", Resource: ResourceRDSTask, Status: StepPending},
				}},
			{Name: "restoring", State: Running, Step: "wait",
				Task: &TaskStatus{ID: 12, Type: "RESTORE_DB", Lifecycle: mssql.InProgress, Percent: 60}, TaskIDs: []int{12},
				Steps: []StepInfo{
					{ID: "upload", Title: "Upload", Status: StepSucceeded},
					{ID: "wait", Title: "Wait", Resource: ResourceRDSTask, Status: StepRunning, StartedAt: &started},
				}},
			{Name: "done", State: Succeeded, Steps: []StepInfo{{ID: "upload", Title: "Upload", Status: StepSucceeded}}},
			{Name: "queued", State: Queued, Step: "upload", Steps: []StepInfo{{ID: "upload", Title: "Upload", Status: StepWaiting}}},
		},
		Events: []Event{{Seq: 1, Time: created, Type: EventJobQueued}, {Seq: 2, Time: started, Type: EventJobStarted}},
	}
	writeJob(t, dir, crashed)

	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	m := openT(t, dir, Options{Now: func() time.Time { return now }})
	defer closeT(t, m)
	got, err := m.Get(crashed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Interrupted || got.EndedAt == nil || !got.EndedAt.Equal(now) {
		t.Fatalf("state %s, ended %v", got.State, got.EndedAt)
	}
	up, rs, dn, qu := got.Items[0], got.Items[1], got.Items[2], got.Items[3]
	if up.State != Interrupted || up.Step != "upload" || up.Steps[0].Status != StepInterrupted || up.Steps[1].Status != StepPending ||
		up.Upload == nil || up.Upload.UploadID != "up-9" || !slices.Equal(up.Upload.PartsDone, []int{1, 2, 3, 4}) {
		t.Fatalf("uploading item: %+v", up)
	}
	if rs.State != Interrupted || !rs.TaskLeftRunning || !slices.Equal(rs.TaskIDs, []int{12}) || rs.Steps[1].Status != StepInterrupted {
		t.Fatalf("restoring item: %+v", rs)
	}
	if dn.State != Succeeded || qu.State != Interrupted || qu.Steps[0].Status != StepInterrupted {
		t.Fatalf("done %+v, queued %+v", dn, qu)
	}
	last := got.Events[len(got.Events)-1]
	if last.Type != EventJobInterrupted || last.Seq != int64(len(got.Events)) || got.Events[2].Seq != 3 {
		t.Fatalf("events %v", eventTypes(got))
	}
	// Written back: a second restart does not interrupt it again.
	if onDisk := readJob(t, dir, crashed.ID); onDisk.State != Interrupted || len(onDisk.Events) != len(got.Events) {
		t.Fatalf("on disk: %s with %d events", onDisk.State, len(onDisk.Events))
	}
	if s := m.List(); len(s) != 1 || s[0].State != Interrupted {
		t.Fatalf("list %+v", s)
	}
}

func TestCloseInterruptsRunningJobs(t *testing.T) {
	dir := t.TempDir()
	m := openT(t, dir, Options{})
	cause := make(chan error, 1)
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{{Name: "sales", Steps: []Step{
		NewStep("upload", "Upload", ResourceTransfer, func(ctx context.Context, r Reporter) (string, error) {
			r.Upload(UploadState{Bucket: "b", Key: "k", UploadID: "up-1", PartsDone: []int{1}})
			<-ctx.Done()
			cause <- context.Cause(ctx)
			return "", ctx.Err()
		}),
		NewStep("restore", "Restore", ResourceRDSTask, func(context.Context, Reporter) (string, error) { return "", nil }),
	}}}})
	eventually(t, "upload running", func() bool { return stepStatus(t, m, job.ID, "sales", "upload") == StepRunning })
	closeT(t, m)
	if c := <-cause; !errors.Is(c, ErrInterrupted) {
		t.Fatalf("cause seen by the step: %v", c)
	}
	if _, err := m.Start(Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "x", Steps: []Step{NewStep("s", "S", NoResource, nil)}}}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after close: %v", err)
	}

	again := openT(t, dir, Options{})
	defer closeT(t, again)
	got, _ := again.Get(job.ID)
	it := got.Items[0]
	if got.State != Interrupted || it.State != Interrupted || it.Steps[0].Status != StepInterrupted ||
		it.Steps[1].Status != StepPending || it.Upload == nil || it.Upload.UploadID != "up-1" {
		t.Fatalf("after restart: %s %+v", got.State, it)
	}
	if n := len(got.Events); got.Events[n-1].Type != EventJobInterrupted {
		t.Fatalf("events %v", eventTypes(got))
	}
}

func TestCloseLeavesStuckStepsBehind(t *testing.T) {
	dir := t.TempDir()
	m := openT(t, dir, Options{})
	stuck, returned := make(chan struct{}), make(chan struct{})
	var rep Reporter
	job := start(t, m, Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{
		NewStep("deaf", "Ignores its context", NoResource, func(_ context.Context, r Reporter) (string, error) {
			rep = r
			<-stuck
			defer close(returned)
			return "", nil
		}),
	}}}})
	eventually(t, "step running", func() bool { return stepStatus(t, m, job.ID, "a", "deaf") == StepRunning })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); err == nil {
		t.Fatal("Close returned no error with a step still running")
	}
	if onDisk := readJob(t, dir, job.ID); onDisk.State != Interrupted || onDisk.Items[0].Steps[0].Status != StepInterrupted {
		t.Fatalf("on disk: %s", onDisk.State)
	}
	close(stuck)
	<-returned
	rep.Log(Info, "too late")
	if got, _ := m.Get(job.ID); got.State != Interrupted || got.Items[0].Steps[0].Status != StepInterrupted {
		t.Fatalf("the late step changed the job: %s", got.State)
	}
}

func TestThrottledSavesAreFlushed(t *testing.T) {
	dir := t.TempDir()
	m := openT(t, dir, Options{SaveEvery: 30 * time.Millisecond})
	defer closeT(t, m)
	gate := make(chan struct{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{
		NewStep("upload", "Upload", ResourceTransfer, func(_ context.Context, r Reporter) (string, error) {
			r.Transfer(Upload, 30, 100)
			r.Transfer(Upload, 70, 100) // throttled: written by the timer
			<-gate
			return "", nil
		}),
	}}}})
	eventually(t, "the last progress on disk", func() bool {
		j := readJob(t, dir, job.ID)
		return j.Items[0].Transfer != nil && j.Items[0].Transfer.Bytes == 70
	})
	close(gate)
	waitJob(t, m, job.ID)
}

func TestRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ended := func(i int, at time.Time) Job {
		return Job{ID: fmt.Sprintf("20260101T000000Z-%08x", i), Kind: Import, Target: "dev", State: Succeeded,
			CreatedAt: at.Add(-time.Minute), UpdatedAt: at, EndedAt: &at, Items: []Item{}, Events: []Event{}}
	}
	var ids []string
	for i := range 205 { // one every hour: the 200 newest stay
		j := ended(i, now.Add(-time.Duration(i)*time.Hour))
		writeJob(t, dir, j)
		ids = append(ids, j.ID)
	}
	old := ended(900, now.Add(-31*24*time.Hour))
	writeJob(t, dir, old)
	// Running when the engine stopped, long ago: interrupted now, so kept.
	stale := ended(901, now.Add(-40*24*time.Hour))
	stale.State, stale.EndedAt = Running, nil
	writeJob(t, dir, stale)

	m := openT(t, dir, Options{Now: func() time.Time { return now }})
	if got := m.List(); len(got) != 200 {
		t.Fatalf("%d jobs kept, want 200", len(got))
	}
	// The interrupted job ended now: it is the newest of the 200.
	kept := append(slices.Clone(ids[:199]), stale.ID)
	dropped := append(slices.Clone(ids[199:]), old.ID)
	for _, id := range kept {
		if _, err := m.Get(id); err != nil {
			t.Fatalf("job %s should be kept: %v", id, err)
		}
	}
	for _, id := range dropped {
		_, err := m.Get(id)
		_, ferr := os.Stat(filepath.Join(dir, id+".json"))
		if !errors.Is(err, ErrNotFound) || !errors.Is(ferr, os.ErrNotExist) {
			t.Fatalf("job %s should be gone: %v, %v", id, err, ferr)
		}
	}
	closeT(t, m)

	// Keep and MaxAge are options.
	small := openT(t, dir, Options{Now: func() time.Time { return now }, Keep: 3, MaxAge: 90 * time.Minute})
	defer closeT(t, small)
	if got := small.List(); len(got) != 3 { // the interrupted job and the two finished less than 90 minutes ago
		t.Fatalf("%d jobs with Keep 3 and MaxAge 90m: %+v", len(got), got)
	}
}

func TestJournalSkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	good := Job{ID: "20261016T000000Z-00000001", Kind: Export, Target: "dev", State: Failed,
		CreatedAt: at, UpdatedAt: at, EndedAt: &at, Items: []Item{}, Events: []Event{}}
	writeJob(t, dir, good)
	files := map[string]string{
		"20261016T000000Z-00000002.json":          "{not json",
		"20261016T000000Z-00000003.json":          `{"id":"20261016T000000Z-00000009"}`,
		"notes.json":                              "{}",
		".20261016T000000Z-00000001.json.123.tmp": "half",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A leftover of a crash is old; a younger one may be another process
	// writing right now.
	old := time.Now().Add(-time.Hour)
	for name := range files {
		if name[0] == '.' {
			if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	young := filepath.Join(dir, ".20261016T000000Z-00000001.json.456.tmp")
	if err := os.WriteFile(young, []byte("being written"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := openT(t, dir, Options{})
	defer closeT(t, m)
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("a recent temporary file belongs to another process: %v", err)
	}
	if got := m.List(); len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("loaded %+v", got)
	}
	for name := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if name[0] == '.' {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale temporary file %s not removed", name)
			}
		} else if err != nil {
			t.Fatalf("%s must be left for a person to look at: %v", name, err)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	config := func() (string, error) { return "/cfg", nil }
	env := func(v string) func(string) string { return func(string) string { return v } }
	tests := []struct {
		goos, xdg, want string
	}{
		{"linux", "/xdg/state", "/xdg/state/dialga/jobs"},
		{"linux", "", "/home/u/.local/state/dialga/jobs"},
		{"linux", "relative/state", "/home/u/.local/state/dialga/jobs"},
		{"darwin", "/xdg/state", "/cfg/dialga/jobs"},
		{"windows", "", "/cfg/dialga/jobs"},
	}
	for _, tc := range tests {
		if tc.goos == "linux" && runtime.GOOS == "windows" {
			continue // "/xdg/state" is not absolute on Windows
		}
		got, err := defaultDir(tc.goos, env(tc.xdg), home, config)
		if err != nil || got != filepath.FromSlash(tc.want) {
			t.Errorf("%s XDG_STATE_HOME=%q: %q, %v; want %q", tc.goos, tc.xdg, got, err, tc.want)
		}
	}
	fail := func() (string, error) { return "", errors.New("no home") }
	if _, err := defaultDir("linux", env(""), fail, config); err == nil {
		t.Error("no home: no error")
	}
	if _, err := defaultDir("darwin", env(""), home, fail); err == nil {
		t.Error("no config dir: no error")
	}
}

func TestReadJournalChangesNothing(t *testing.T) {
	if list, errs := ReadJournal(filepath.Join(t.TempDir(), "missing")); list != nil || errs != nil {
		t.Fatalf("missing dir: %v %v", list, errs)
	}
	dir := t.TempDir()
	running := Job{ID: "20261016T090000Z-0000abcd", Kind: Import, Target: "dev", State: Running,
		Items: []Item{{Name: "a", State: Running, Steps: []StepInfo{{ID: "upload", Status: StepRunning}}}}, Events: []Event{}}
	writeJob(t, dir, running)
	tmp := filepath.Join(dir, ".20261016T090000Z-0000abcd.json.1.tmp")
	if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, running.ID+".json"))

	// An engine holds the journal: reading still works.
	m := openT(t, dir, Options{})
	defer closeT(t, m)
	after, _ := os.ReadFile(filepath.Join(dir, running.ID+".json"))
	list, errs := ReadJournal(dir)
	if len(errs) != 0 || len(list) != 1 || list[0].State != Interrupted {
		t.Fatalf("read while an engine runs: %v %v", list, errs)
	}
	if string(before) == string(after) {
		t.Fatal("the engine did not mark the job (test setup)")
	}

	// Without an engine: the file stays as it is, temporary files too.
	dir2 := t.TempDir()
	writeJob(t, dir2, running)
	tmp2 := filepath.Join(dir2, ".x.tmp")
	if err := os.WriteFile(tmp2, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, errs = ReadJournal(dir2)
	if len(errs) != 0 || len(list) != 1 || list[0].State != Running {
		t.Fatalf("read: %v %v", list, errs)
	}
	if _, err := os.Stat(tmp2); err != nil {
		t.Fatal("ReadJournal removed a file")
	}
	if _, err := os.Stat(filepath.Join(dir2, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ReadJournal created the lock file")
	}
}
