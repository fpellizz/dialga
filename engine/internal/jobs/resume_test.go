package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// blocking runs until ctx ends, after before (which reports what a resume
// will need).
func blocking(id string, res Resource, before func(Reporter)) func(context.Context, Reporter) (string, error) {
	return func(ctx context.Context, r Reporter) (string, error) {
		if before != nil {
			before(r)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
}

func quick(detail string) func(context.Context, Reporter) (string, error) {
	return func(context.Context, Reporter) (string, error) { return detail, nil }
}

type uploadParams struct {
	File string `json:"file"`
	Key  string `json:"key"`
}

// interruptedJournal runs a job until it is mid-way and closes the engine:
//   - done: upload and restore succeeded;
//   - bad: its upload failed;
//   - sales: interrupted mid-upload, with the multipart upload recorded;
//   - hr: upload and restore done (task 42 queued), interrupted mid-wait;
//   - crm: interrupted inside restore, after its task 50 was queued.
func interruptedJournal(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	m := openT(t, dir, Options{})
	up := func(name string, run func(context.Context, Reporter) (string, error)) Step {
		return WithKind(NewStep("upload", "Upload "+name, ResourceTransfer, run), "upload",
			uploadParams{File: "/backups/" + name + ".bak", Key: "import/" + name + ".bak"})
	}
	restore := func(run func(context.Context, Reporter) (string, error)) Step {
		return WithKind(NewStep("restore", "Restore", ResourceRDSTask, run), "restore", map[string]string{"op": "restore"})
	}
	wait := func(run func(context.Context, Reporter) (string, error)) Step {
		return WithKind(NewStep("wait", "Wait", ResourceRDSTask, run), "wait", map[string]string{"what": "restore"})
	}
	task := func(id int) func(Reporter) {
		return func(r Reporter) {
			r.Task(TaskStatus{ID: id, Type: "RESTORE_DB", Lifecycle: mssql.InProgress, Percent: 10})
		}
	}
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 2, Items: []ItemSpec{
		{Name: "done", Steps: []Step{up("done", quick("uploaded")), restore(quick("task 1 created"))}},
		{Name: "bad", Steps: []Step{up("bad", func(context.Context, Reporter) (string, error) { return "", errors.New("Access Denied") }), restore(quick(""))}},
		{Name: "sales", Steps: []Step{
			up("sales", blocking("upload", ResourceTransfer, func(r Reporter) {
				r.Upload(UploadState{Bucket: "b", Key: "import/sales.bak", UploadID: "up-1", Size: 30, PartSize: 10, PartsDone: []int{1, 2}})
			})),
			restore(quick("")), wait(quick("")),
		}},
		{Name: "hr", Steps: []Step{
			up("hr", quick("uploaded")),
			restore(func(_ context.Context, r Reporter) (string, error) { task(42)(r); return "task 42 created", nil }),
			wait(blocking("wait", ResourceRDSTask, task(42))),
		}},
		{Name: "crm", Steps: []Step{
			up("crm", quick("uploaded")),
			restore(blocking("restore", ResourceRDSTask, task(50))),
			wait(quick("")),
		}},
	}})
	eventually(t, "every database at its point", func() bool {
		j, _ := m.Get(job.ID)
		st := func(i, s int) StepStatus { return j.Items[i].Steps[s].Status }
		return j.Items[0].State == Succeeded && j.Items[1].State == Failed &&
			st(2, 0) == StepRunning && j.Items[2].Upload != nil &&
			st(3, 2) == StepRunning && j.Items[3].Task != nil && j.Items[3].Task.ID == 42 &&
			st(4, 1) == StepRunning && j.Items[4].Steps[1].TaskID == 50
	})
	closeT(t, m)
	return dir, job.ID
}

// fakeFactories records what they were given and rebuild steps that succeed.
type fakeFactories struct {
	mu    sync.Mutex
	calls []Rebuild
}

func (f *fakeFactories) record(rb Rebuild) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rb)
}

func (f *fakeFactories) registry() *Registry {
	reg := NewRegistry()
	reg.Register("upload", func(rb Rebuild) (Step, error) {
		f.record(rb)
		var p uploadParams
		if err := rb.Params(&p); err != nil {
			return nil, err
		}
		detail := "uploaded " + p.File
		if u := rb.Item.Upload; u != nil {
			detail = fmt.Sprintf("resumed %s from %d parts", u.UploadID, len(u.PartsDone))
		}
		return NewStep(rb.Step.ID, rb.Step.Title, ResourceTransfer, quick(detail)), nil
	})
	reg.Register("restore", func(rb Rebuild) (Step, error) {
		f.record(rb)
		if rb.Step.TaskID != 0 {
			return nil, nil // queued before the interrupt: the wait follows it
		}
		return NewStep(rb.Step.ID, rb.Step.Title, ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
			r.Task(TaskStatus{ID: 77, Type: "RESTORE_DB", Lifecycle: mssql.Created})
			return "task 77 created", nil
		}), nil
	})
	reg.Register("wait", func(rb Rebuild) (Step, error) {
		f.record(rb)
		return NewStep(rb.Step.ID, rb.Step.Title, ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
			// At run time: a rebuilt restore before it may queue a new task.
			id := r.LastTaskID()
			r.Task(TaskStatus{ID: id, Type: "RESTORE_DB", Lifecycle: mssql.Success, Percent: 100})
			return fmt.Sprintf("followed task %d", id), nil
		}), nil
	})
	return reg
}

func (f *fakeFactories) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, rb := range f.calls {
		out = append(out, rb.Item.Name+"/"+rb.Step.ID)
	}
	return out
}

func (f *fakeFactories) call(item, step string) Rebuild {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rb := range f.calls {
		if rb.Item.Name == item && rb.Step.ID == step {
			return rb
		}
	}
	return Rebuild{}
}

func TestResumeRebuildsWhatIsLeft(t *testing.T) {
	dir, id := interruptedJournal(t)
	f := &fakeFactories{}
	m := openT(t, dir, Options{Registry: f.registry()})
	defer closeT(t, m)
	before, _ := m.Get(id)
	if before.State != Interrupted {
		t.Fatalf("setup: %s", before.State)
	}
	lastSeq := before.Events[len(before.Events)-1].Seq

	snap, err := m.Resume(id, ResumeOptions{Env: "the env", InstanceKey: "123456789012/eu-west-1/db1"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.ID != id || snap.State != Queued || snap.EndedAt != nil {
		t.Fatalf("snapshot %s %v", snap.State, snap.EndedAt)
	}
	if ev := snap.Events[len(snap.Events)-1]; ev.Type != EventJobResumed || ev.Seq != lastSeq+1 ||
		ev.Message != "Resumed: 3 database(s) to finish, 2 kept as they were." {
		t.Fatalf("resume event %+v", ev)
	}
	// Only the steps left: done and bad untouched, succeeded steps not rebuilt.
	if got, want := f.seen(), []string{"sales/upload", "sales/restore", "sales/wait", "hr/wait", "crm/restore", "crm/wait"}; !slices.Equal(got, want) {
		t.Fatalf("factories called for %v, want %v", got, want)
	}
	up := f.call("sales", "upload")
	if up.Env != "the env" || up.Job.ID != id || up.Item.Upload == nil || up.Item.Upload.UploadID != "up-1" ||
		!slices.Equal(up.Item.Upload.PartsDone, []int{1, 2}) || up.Step.Status != StepInterrupted || up.Step.Kind != "upload" {
		t.Fatalf("upload rebuild %+v", up)
	}
	if rb := f.call("hr", "wait"); rb.LastTaskID() != 42 {
		t.Fatalf("hr wait follows task %d", rb.LastTaskID())
	}
	if rb := f.call("crm", "restore"); rb.Step.TaskID != 50 {
		t.Fatalf("crm restore task %d", rb.Step.TaskID)
	}

	got := waitJob(t, m, id)
	if got.State != Failed { // bad failed before the interrupt and stays failed
		t.Fatalf("job %s", got.State)
	}
	byName := map[string]Item{}
	for _, it := range got.Items {
		byName[it.Name] = it
	}
	if !equalJSON(t, byName["done"], before.Items[0]) || !equalJSON(t, byName["bad"], before.Items[1]) {
		t.Fatal("an item that had ended changed")
	}
	sales, hr, crm := byName["sales"], byName["hr"], byName["crm"]
	if sales.State != Succeeded || sales.Steps[0].Detail != "resumed up-1 from 2 parts" || sales.Steps[1].TaskID != 77 ||
		sales.Steps[2].Detail != "followed task 77" {
		t.Fatalf("sales %+v", sales)
	}
	if hr.State != Succeeded || !equalJSON(t, hr.Steps[:2], before.Items[3].Steps[:2]) || hr.Steps[2].Detail != "followed task 42" || hr.TaskLeftRunning {
		t.Fatalf("hr %+v", hr)
	}
	if crm.State != Succeeded || crm.Steps[1].Status != StepSucceeded || crm.Steps[1].Detail != "Done before the interrupt." ||
		crm.Steps[2].Detail != "followed task 50" || !slices.Equal(crm.TaskIDs, []int{50}) {
		t.Fatalf("crm %+v", crm)
	}
	// The journal has the end, and the job cannot resume twice.
	if onDisk := readJob(t, dir, id); onDisk.State != Failed || onDisk.Events[len(onDisk.Events)-1].Type != EventJobFailed {
		t.Fatalf("on disk: %s", onDisk.State)
	}
	if _, err := m.Resume(id, ResumeOptions{}); !errors.Is(err, ErrNotInterrupted) {
		t.Fatalf("second resume: %v", err)
	}
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	return asJSON(t, a) == asJSON(t, b)
}

func TestResumeRefusesWhatItCannotRebuild(t *testing.T) {
	dir, id := interruptedJournal(t)
	file := filepath.Join(dir, id+".json")
	original, _ := os.ReadFile(file)

	f := &fakeFactories{}
	partial := NewRegistry()
	partial.Register("upload", f.registry().factories["upload"])
	partial.Register("restore", f.registry().factories["restore"])
	cases := map[string]struct {
		reg  *Registry
		want string
	}{
		"unknown kind":     {partial, `database sales, step wait: no factory rebuilds steps of kind "wait"`},
		"factory error":    {regWith("upload", func(Rebuild) (Step, error) { return nil, errors.New("file gone") }), "database sales, step upload: file gone"},
		"factory panic":    {regWith("upload", func(Rebuild) (Step, error) { panic("boom") }), "the upload factory panicked: boom"},
		"wrong step built": {regWith("upload", func(Rebuild) (Step, error) { return NewStep("other", "x", NoResource, quick("")), nil }), `built step "other"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := openT(t, dir, Options{Registry: tc.reg})
			defer closeT(t, m)
			_, err := m.Resume(id, ResumeOptions{})
			if !errors.Is(err, ErrNotResumable) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
			if j, _ := m.Get(id); j.State != Interrupted {
				t.Fatalf("job %s after a refused resume", j.State)
			}
		})
	}
	if now, _ := os.ReadFile(file); string(now) != string(original) {
		t.Fatal("a refused resume changed the journal")
	}
}

func regWith(kind string, f Factory) *Registry {
	reg := NewRegistry()
	reg.Register(kind, f)
	return reg
}

func TestResumeNeedsAResumableStep(t *testing.T) {
	m := New(Options{Registry: NewRegistry()})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{
		NewStep("plain", "No kind", NoResource, blocking("plain", NoResource, nil)),
	}}}})
	eventually(t, "running", func() bool { return stepStatus(t, m, job.ID, "a", "plain") == StepRunning })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resume(job.ID, ResumeOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("resume on a closed manager: %v", err)
	}
	// A manager in memory keeps the interrupted job until it closes; here
	// it is closed: check the refusal on a fresh one with the same job.
	m2 := New(Options{Registry: NewRegistry()})
	j, _ := m.Get(job.ID)
	r := &run{m: m2, job: j, cancel: func(error) {}, done: make(chan struct{}), subs: map[*Subscription]struct{}{}, ended: true}
	close(r.done)
	m2.jobs[j.ID] = r
	if _, err := m2.Resume(j.ID, ResumeOptions{}); !errors.Is(err, ErrNotResumable) || !strings.Contains(err.Error(), "recorded no kind") {
		t.Fatalf("plain step: %v", err)
	}
	if _, err := m2.Resume("20200101T000000Z-00000000", ResumeOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestResumeOnlyInterrupted(t *testing.T) {
	m := New(Options{Registry: NewRegistry()})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{
		WithKind(NewStep("s", "S", NoResource, quick("")), "s", nil),
	}}}})
	waitJob(t, m, job.ID)
	if _, err := m.Resume(job.ID, ResumeOptions{}); !errors.Is(err, ErrNotInterrupted) || !strings.Contains(err.Error(), "is succeeded") {
		t.Fatalf("resume a succeeded job: %v", err)
	}
}

func TestStartRecordsKindParamsAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	m := openT(t, dir, Options{})
	defer closeT(t, m)
	gate := make(chan struct{})
	job := start(t, m, Spec{Kind: Export, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{
		WithKind(NewStep("backup", "Backup", ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
			if err := r.Checkpoint(map[string]string{"key": "export/a-20261016.bak"}); err != nil {
				return "", err
			}
			if err := r.Checkpoint(func() {}); err == nil {
				return "", errors.New("a func checkpointed")
			}
			<-gate
			return "", nil
		}), "backup", map[string]string{"key": ""}),
	}}}})
	if got := string(job.Items[0].Steps[0].Params); job.Items[0].Steps[0].Kind != "backup" || got != `{"key":""}` {
		t.Fatalf("recorded %q %s", job.Items[0].Steps[0].Kind, got)
	}
	eventually(t, "the checkpoint on disk", func() bool {
		return string(readJob(t, dir, job.ID).Items[0].Steps[0].Params) == `{"key":"export/a-20261016.bak"}`
	})
	close(gate)
	waitJob(t, m, job.ID)

	bad := WithKind(NewStep("x", "X", NoResource, quick("")), "x", func() {})
	if _, err := m.Start(Spec{Kind: Export, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{bad}}}}); err == nil ||
		!strings.Contains(err.Error(), "step x: params") {
		t.Fatalf("unserializable params: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	f := func(Rebuild) (Step, error) { return nil, nil }
	reg.Register("a", f)
	if _, ok := reg.lookup("a"); !ok {
		t.Fatal("registered kind not found")
	}
	for name, register := range map[string]func(){
		"twice":   func() { reg.Register("a", f) },
		"empty":   func() { reg.Register("", f) },
		"nil":     func() { reg.Register("b", nil) },
		"default": func() { Register("", f) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			register()
		}()
	}
	var p struct{ Key string }
	if err := (Rebuild{Step: StepInfo{ID: "s"}, Item: Item{Name: "a"}}).Params(&p); err == nil {
		t.Fatal("params of a step without params")
	}
	if err := (Rebuild{Step: StepInfo{Params: json.RawMessage(`{"Key":"k"}`)}}).Params(&p); err != nil || p.Key != "k" {
		t.Fatalf("params %+v %v", p, err)
	}
	if id := (Rebuild{Item: Item{TaskIDs: []int{3, 9}}}).LastTaskID(); id != 9 {
		t.Fatalf("last task %d", id)
	}
}
