package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Two managers on one directory are the app engine and a CLI run.

func running(t *testing.T, m *Manager, name string) (Job, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	step := NewStep("work", "Work", NoResource, func(ctx context.Context, r Reporter) (string, error) {
		r.Log(Info, "working")
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	job, err := m.Start(Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: name, Steps: []Step{step}}}})
	if err != nil {
		t.Fatal(err)
	}
	return job, release
}

func TestOtherProcessJobIsListedFollowedAndLeftAlone(t *testing.T) {
	dir := t.TempDir()
	cli := openT(t, dir, Options{Origin: OriginCLI})
	job, release := running(t, cli, "sales")
	eventually(t, "the job runs", func() bool { j, _ := cli.Get(job.ID); return j.State == Running })

	// The app starts while the CLI job runs: it opens, and keeps the job as it is.
	app := openT(t, dir, Options{Origin: OriginApp})
	defer closeT(t, app)
	got, err := app.Get(job.ID)
	if err != nil || got.State.Ended() || got.Origin != OriginCLI {
		t.Fatalf("the CLI job in the app: %+v, %v", got, err)
	}
	if on := cli.List(); len(on) != 1 || on[0].State.Ended() {
		t.Fatalf("the CLI job was touched by the app's start: %+v", on)
	}
	if _, err := app.Cancel(job.ID); !errors.Is(err, ErrForeign) {
		t.Fatalf("cancel of a job of another process: %v", err)
	}
	if _, err := app.Resume(job.ID, ResumeOptions{}); !errors.Is(err, ErrNotInterrupted) {
		t.Fatalf("resume of a running job: %v", err)
	}

	// Its events reach a subscriber of the app, up to the end.
	sub, err := app.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	close(release)
	deadline := time.After(5 * time.Second)
	var last Event
	for open := true; open; {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				open = false
				break
			}
			last = ev
		case <-deadline:
			t.Fatalf("the app did not see the job end; last event %+v", last)
		}
	}
	all := append(append([]Event(nil), sub.Replay...), last)
	if last.Type != EventJobSucceeded || len(all) < 3 {
		t.Fatalf("events seen by the app: %+v", all)
	}
	if s := app.List(); len(s) != 1 || s[0].State != Succeeded || s[0].Origin != OriginCLI {
		t.Fatalf("list in the app: %+v", s)
	}
	closeT(t, cli)
}

func TestNewJobOfAnotherProcessAppearsInTheList(t *testing.T) {
	dir := t.TempDir()
	app := openT(t, dir, Options{})
	defer closeT(t, app)
	if len(app.List()) != 0 {
		t.Fatal("not empty")
	}
	cli := openT(t, dir, Options{Origin: OriginCLI})
	job, release := running(t, cli, "hr")
	defer func() { close(release); closeT(t, cli) }()
	eventually(t, "the job is listed while it runs", func() bool {
		s := app.List()
		return len(s) == 1 && s[0].ID == job.ID && !s[0].State.Ended()
	})
	if _, err := app.Get(job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanOfAnotherProcessIsInterrupted(t *testing.T) {
	dir := t.TempDir()
	app := openT(t, dir, Options{})
	defer closeT(t, app)
	at := time.Now()
	// A running job whose process was killed: nobody holds its lock.
	orphan := Job{ID: "20261016T010000Z-0000000a", Kind: Import, Origin: OriginCLI, Target: "dev", State: Running,
		CreatedAt: at, UpdatedAt: at, Items: []Item{{Name: "sales", State: Running, Steps: []StepInfo{{ID: "work", Status: StepRunning}}}}, Events: []Event{}}
	writeJob(t, dir, orphan)
	eventually(t, "the orphan is marked", func() bool {
		s := app.List()
		return len(s) == 1 && s[0].State == Interrupted
	})
	if got := readJob(t, dir, orphan.ID); got.State != Interrupted {
		t.Fatalf("file: %s", got.State)
	}
}

func TestResumeTakesTheLockOfTheJob(t *testing.T) {
	dir, id := interruptedJournal(t)
	a := openT(t, dir, Options{})
	defer closeT(t, a)
	hold, err := a.journal.lock(id) // another process resumes it
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resume(id, ResumeOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("resume while the job is locked: %v", err)
	}
	_ = hold()
	if _, err := os.Stat(filepath.Join(dir, "."+id+".lock")); err != nil {
		t.Fatalf("lock file: %v", err)
	}
}

func TestIDsAreUniqueAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	// The same clock: every id of a second is told apart by its random part and the lock only.
	fixed := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixed }
	a := openT(t, dir, Options{Now: now})
	b := openT(t, dir, Options{Now: now, Origin: OriginCLI})
	defer closeT(t, a)
	defer closeT(t, b)
	var wg sync.WaitGroup
	ids := make(chan string, 200)
	for _, m := range []*Manager{a, b} {
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				j, err := m.Record(Finished{Kind: Export, Target: "dev", State: Failed, Refusal: &Refusal{Status: 422, Reason: "no"}})
				if err != nil {
					t.Error(err)
					return
				}
				ids <- j.ID
			}()
		}
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("id %s given twice", id)
		}
		seen[id] = true
		if got := readJob(t, dir, id); got.ID != id {
			t.Fatalf("file of %s holds %s", id, got.ID)
		}
	}
	if len(seen) != 100 {
		t.Fatalf("%d jobs", len(seen))
	}
}

func TestRecordRefusedJob(t *testing.T) {
	dir := t.TempDir()
	m := openT(t, dir, Options{Origin: OriginCLI})
	defer closeT(t, m)
	j, err := m.Record(Finished{Kind: Operation, Operation: "drop", Target: "dev", Instance: "i-1", State: Failed,
		Items:   []FinishedItem{{Name: "sales", Error: "not confirmed"}},
		Refusal: &Refusal{Status: 422, Reason: "not confirmed"}})
	if err != nil {
		t.Fatal(err)
	}
	got := readJob(t, dir, j.ID)
	if got.State != Failed || got.Kind != Operation || got.Operation != "drop" || got.Origin != OriginCLI ||
		got.Refusal == nil || got.Refusal.Status != 422 || len(got.Items) != 1 || got.Items[0].State != Failed ||
		got.EndedAt == nil || len(got.Events) != 1 || got.Events[0].Type != EventJobFailed {
		t.Fatalf("recorded %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "."+j.ID+".lock")); err != nil {
		t.Fatalf("lock file left for the retention to remove: %v", err)
	}
	if _, err := m.Record(Finished{Kind: Operation, Target: "dev", State: Failed}); err == nil {
		t.Fatal("an operation job without operation")
	}
}

func TestRetentionRemovesLockFilesAcrossOrigins(t *testing.T) {
	dir := t.TempDir()
	cli := openT(t, dir, Options{Keep: 2, Origin: OriginCLI})
	defer closeT(t, cli)
	app := openT(t, dir, Options{Keep: 2})
	defer closeT(t, app)
	var ids []string
	for i, m := range []*Manager{cli, app, cli, app} {
		j, err := m.Record(Finished{Kind: Export, Target: "dev", State: Failed, Refusal: &Refusal{Status: 502, Reason: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		time.Sleep(2 * time.Millisecond)
		_ = i
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("%d job files kept of 4 with Keep 2: %v", len(files), files)
	}
	for _, id := range ids[:2] {
		if _, err := os.Stat(filepath.Join(dir, "."+id+".lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock file of the removed job %s: %v", id, err)
		}
	}
}
