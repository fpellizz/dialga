package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// probe counts what runs at once and logs the order of starts and ends.
type probe struct {
	mu  sync.Mutex
	cur map[string]int
	max map[string]int
	log []string
}

func newProbe() *probe { return &probe{cur: map[string]int{}, max: map[string]int{}} }

func (p *probe) enter(counter, entry string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur[counter]++
	p.max[counter] = max(p.max[counter], p.cur[counter])
	p.log = append(p.log, "+"+entry)
}

func (p *probe) leave(counter, entry string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur[counter]--
	p.log = append(p.log, "-"+entry)
}

func (p *probe) current(counter string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur[counter]
}

func (p *probe) maxOf(counter string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max[counter]
}

// index is the position of entry in the log, -1 if absent.
func (p *probe) index(entry string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Index(p.log, entry)
}

func (p *probe) entries(prefix string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, e := range p.log {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// eventually polls cond for up to 5 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// timed is a step that holds its counter for d.
func timed(p *probe, id string, res Resource, counter string, d time.Duration) Step {
	return NewStep(id, "Step "+id, res, func(ctx context.Context, r Reporter) (string, error) {
		e := r.Item() + "/" + id
		p.enter(counter, e)
		defer p.leave(counter, e)
		select {
		case <-time.After(d):
			return id + " done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
}

// together is a step that waits, up to a second, for n steps of its
// counter to be inside: a limit below n shows up as a timeout, one above n
// as a higher maximum.
func together(p *probe, id string, res Resource, counter string, n int) Step {
	return NewStep(id, "Step "+id, res, func(_ context.Context, r Reporter) (string, error) {
		e := r.Item() + "/" + id
		p.enter(counter, e)
		defer p.leave(counter, e)
		deadline := time.Now().Add(time.Second)
		for p.current(counter) < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(2 * time.Millisecond)
		return "", nil
	})
}

// gated is a step that runs until its gate is closed (or ctx ends).
func gated(p *probe, id string, res Resource, gate <-chan struct{}) Step {
	return NewStep(id, "Step "+id, res, func(ctx context.Context, r Reporter) (string, error) {
		e := r.Item() + "/" + id
		p.enter(id, e)
		defer p.leave(id, e)
		select {
		case <-gate:
			return id + " done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
}

func items(names []string, steps func(name string) []Step) []ItemSpec {
	var out []ItemSpec
	for _, n := range names {
		out = append(out, ItemSpec{Name: n, Steps: steps(n)})
	}
	return out
}

func waitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := m.Wait(ctx, id)
	if err != nil {
		t.Fatalf("wait %s: %v", id, err)
	}
	return job
}

func start(t *testing.T, m *Manager, spec Spec) Job {
	t.Helper()
	job, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func stepStatus(t *testing.T, m *Manager, id, item, step string) StepStatus {
	t.Helper()
	job, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range job.Items {
		if it.Name != item {
			continue
		}
		for _, st := range it.Steps {
			if st.ID == step {
				return st.Status
			}
		}
	}
	t.Fatalf("no step %s/%s", item, step)
	return ""
}

func eventTypes(job Job) []EventType {
	var out []EventType
	for _, ev := range job.Events {
		out = append(out, ev.Type)
	}
	return out
}

func TestTransfersAreBounded(t *testing.T) {
	m := New(Options{Limits: map[Resource]int{ResourceTransfer: 2}})
	p := newProbe()
	// Uploads go through in pairs; a third never joins them.
	step := func(string) []Step { return []Step{together(p, "upload", ResourceTransfer, "transfer", 2)} }
	a := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "i1", Items: items([]string{"a1", "a2", "a3"}, step)})
	b := start(t, m, Spec{Kind: Import, Target: "other", Instance: "i2", Items: items([]string{"b1", "b2", "b3"}, step)})
	for _, id := range []string{a.ID, b.ID} {
		if job := waitJob(t, m, id); job.State != Succeeded {
			t.Fatalf("job %s: %s", id, job.State)
		}
	}
	if got := p.maxOf("transfer"); got != 2 {
		t.Fatalf("transfers at once: %d, want 2 (the limit, all jobs together)", got)
	}
}

func TestRDSTasksPerInstanceAcrossJobs(t *testing.T) {
	m := New(Options{})
	p := newProbe()
	// The slot is held from the submit (restore) to the end of the task
	// (wait): count that whole span.
	steps := func(counter string) func(string) []Step {
		return func(string) []Step {
			return []Step{
				NewStep("restore", "Restore", ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
					p.enter(counter, r.Item())
					time.Sleep(3 * time.Millisecond)
					return "", nil
				}),
				NewStep("wait", "Wait", ResourceRDSTask, func(_ context.Context, r Reporter) (string, error) {
					time.Sleep(3 * time.Millisecond)
					p.leave(counter, r.Item())
					return "", nil
				}),
			}
		}
	}
	a := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 2, Items: items([]string{"a1", "a2", "a3", "a4"}, steps("db1"))})
	b := start(t, m, Spec{Kind: Import, Target: "dev-other-login", Instance: "db1", MaxTasks: 2, Items: items([]string{"b1", "b2", "b3"}, steps("db1"))})
	c := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 1, Items: items([]string{"c1", "c2"}, steps("db1"))})
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if job := waitJob(t, m, id); job.State != Succeeded {
			t.Fatalf("job %s: %s", id, job.State)
		}
	}
	if got := p.maxOf("db1"); got < 1 || got > 2 {
		t.Fatalf("native tasks at once on db1: %d, want at most 2", got)
	}
}

func TestInstancesDoNotShareSlots(t *testing.T) {
	m := New(Options{})
	p := newProbe()
	gate := make(chan struct{})
	full := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 2,
		Items: items([]string{"a1", "a2", "a3"}, func(string) []Step { return []Step{gated(p, "restore", ResourceRDSTask, gate)} })})
	eventually(t, "two tasks on db1", func() bool { return p.current("restore") == 2 })
	other := start(t, m, Spec{Kind: Import, Target: "prod", Instance: "db2", MaxTasks: 2,
		Items: items([]string{"x1", "x2"}, func(string) []Step { return []Step{together(p, "restore2", ResourceRDSTask, "db2", 2)} })})
	if job := waitJob(t, m, other.ID); job.State != Succeeded || p.maxOf("db2") != 2 {
		t.Fatalf("db2 job %s, %d at once: it must not wait for db1", job.State, p.maxOf("db2"))
	}
	if st := stepStatus(t, m, full.ID, "a3", "restore"); st != StepWaiting {
		t.Fatalf("a3 on the full instance: %s, want waiting", st)
	}
	close(gate)
	if job := waitJob(t, m, full.ID); job.State != Succeeded {
		t.Fatalf("db1 job: %s", job.State)
	}
}

func TestSlotHeldFromRestoreToWait(t *testing.T) {
	m := New(Options{})
	p := newProbe()
	gate := make(chan struct{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", MaxTasks: 1,
		Items: items([]string{"db-a", "db-b"}, func(name string) []Step {
			wait := timed(p, "wait", ResourceRDSTask, "rds", time.Millisecond)
			if name == "db-a" {
				wait = gated(p, "wait", ResourceRDSTask, gate)
			}
			return []Step{timed(p, "restore", ResourceRDSTask, "rds", time.Millisecond), wait}
		})})
	eventually(t, "db-a waiting for its task", func() bool { return p.index("+db-a/wait") >= 0 })
	eventually(t, "db-b waiting for the slot", func() bool { return stepStatus(t, m, job.ID, "db-b", "restore") == StepWaiting })
	if p.index("+db-b/restore") >= 0 {
		t.Fatal("db-b submitted a task between the restore and the wait of db-a")
	}
	close(gate)
	waitJob(t, m, job.ID)
	if p.index("+db-b/restore") < p.index("-db-a/wait") {
		t.Fatalf("db-b restored before the task of db-a ended: %v", p.log)
	}
}

func TestPipeliningInItemOrder(t *testing.T) {
	m := New(Options{Limits: map[Resource]int{ResourceTransfer: 1}})
	p := newProbe()
	release := make(chan struct{})
	names := []string{"db1", "db2", "db3"}
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "rds1", MaxTasks: 1,
		Items: items(names, func(name string) []Step {
			restore := timed(p, "restore", ResourceRDSTask, "rds", time.Millisecond)
			if name == "db1" {
				restore = gated(p, "restore", ResourceRDSTask, release)
			}
			return []Step{timed(p, "upload", ResourceTransfer, "transfer", 2*time.Millisecond), restore}
		})})
	// While db1 restores, db2 uploads (and then db3).
	eventually(t, "db2 uploading while db1 restores", func() bool { return p.index("+db2/upload") >= 0 })
	if p.index("-db1/restore") >= 0 {
		t.Fatal("db1 restore ended before the gate opened")
	}
	close(release)
	if got := waitJob(t, m, job.ID); got.State != Succeeded {
		t.Fatalf("job %s", got.State)
	}
	// Each resource takes the databases in the order of the job.
	for _, step := range []string{"upload", "restore"} {
		var order []string
		for _, e := range p.entries("+db") {
			if name, ok := strings.CutSuffix(e, "/"+step); ok {
				order = append(order, strings.TrimPrefix(name, "+"))
			}
		}
		if !slices.Equal(order, names) {
			t.Fatalf("%s order %v, want %v", step, order, names)
		}
	}
	if p.maxOf("transfer") != 1 || p.maxOf("rds") > 1 {
		t.Fatalf("limits: %d transfers, %d tasks", p.maxOf("transfer"), p.maxOf("rds"))
	}
}

func TestFailedItemDoesNotStopTheOthers(t *testing.T) {
	m := New(Options{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1",
		Items: items([]string{"ok1", "bad", "ok2"}, func(name string) []Step {
			restore := NewStep("restore", "Restore", ResourceRDSTask, func(context.Context, Reporter) (string, error) {
				if name == "bad" {
					return "", errors.New("RESTORE_DB ended in ERROR")
				}
				return "task 1 submitted", nil
			})
			post := NewStep("post", "Post", NoResource, func(_ context.Context, r Reporter) (string, error) {
				r.Result("restored " + r.Item())
				return "", nil
			})
			return []Step{restore, post}
		})})
	got := waitJob(t, m, job.ID)
	if got.State != Failed {
		t.Fatalf("job %s, want failed", got.State)
	}
	for _, it := range got.Items {
		switch it.Name {
		case "bad":
			if it.State != Failed || it.Error != "RESTORE_DB ended in ERROR" || it.Steps[1].Status != StepSkipped || it.Step != "restore" {
				t.Fatalf("bad item %+v", it)
			}
		default:
			if it.State != Succeeded || it.Result != "restored "+it.Name || it.Steps[0].Detail != "task 1 submitted" || it.Percent != 100 {
				t.Fatalf("item %+v", it)
			}
		}
	}
	if s := got.Summary(); s.Succeeded != 2 || s.Failed != 1 || !slices.Equal(s.Databases, []string{"ok1", "bad", "ok2"}) {
		t.Fatalf("summary %+v", s)
	}
	types := eventTypes(got)
	for _, want := range []EventType{EventJobQueued, EventJobStarted, EventStepFailed, EventStepSkipped, EventJobFailed} {
		if !slices.Contains(types, want) {
			t.Fatalf("events %v: no %s", types, want)
		}
	}
	if types[len(types)-1] != EventJobFailed {
		t.Fatalf("last event %s", types[len(types)-1])
	}
}

func TestCancel(t *testing.T) {
	m := New(Options{Limits: map[Resource]int{ResourceTransfer: 1}})
	p := newProbe()
	causes := make(chan error, 3)
	blocking := func(id string, res Resource, task bool) Step {
		return NewStep(id, id, res, func(ctx context.Context, r Reporter) (string, error) {
			p.enter(id, r.Item())
			if task {
				r.Task(TaskStatus{ID: 42, Type: "RESTORE_DB", Lifecycle: mssql.InProgress, Percent: 30})
			}
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return "", ctx.Err()
		})
	}
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{
		{Name: "restoring", Steps: []Step{blocking("restore", ResourceRDSTask, true), timed(p, "post", NoResource, "post", 0)}},
		{Name: "uploading", Steps: []Step{blocking("upload", ResourceTransfer, false)}},
		{Name: "queued", Steps: []Step{blocking("upload", ResourceTransfer, false)}},
	}})
	eventually(t, "restore and upload running, the third waiting", func() bool {
		return p.current("restore") == 1 && p.current("upload") == 1 &&
			stepStatus(t, m, job.ID, "queued", "upload") == StepWaiting
	})
	s, err := m.Cancel(job.ID)
	if err != nil || !s.CancelRequested {
		t.Fatalf("cancel: %+v, %v", s, err)
	}
	got := waitJob(t, m, job.ID)
	if got.State != Cancelled {
		t.Fatalf("job %s, want cancelled", got.State)
	}
	for range 2 {
		if c := <-causes; !errors.Is(c, ErrCancelled) {
			t.Fatalf("cause seen by a step: %v", c)
		}
	}
	for _, it := range got.Items {
		if it.State != Cancelled {
			t.Fatalf("item %s: %s", it.Name, it.State)
		}
		for _, st := range it.Steps {
			if st.Status != StepCancelled {
				t.Fatalf("step %s/%s: %s", it.Name, st.ID, st.Status)
			}
		}
	}
	restoring := got.Items[0]
	if !restoring.TaskLeftRunning || !slices.Equal(restoring.TaskIDs, []int{42}) {
		t.Fatalf("restoring item %+v: the running task must be recorded", restoring)
	}
	var warned bool
	for _, ev := range got.Events {
		if ev.Type == EventLog && ev.Level == Warn && strings.Contains(ev.Message, "Native task 42") {
			warned = true
		}
	}
	if !warned || !slices.Contains(eventTypes(got), EventJobCancelling) {
		t.Fatalf("events %v", eventTypes(got))
	}
	if _, err := m.Cancel(job.ID); !errors.Is(err, ErrEnded) {
		t.Fatalf("cancel after the end: %v", err)
	}
	if _, err := m.Cancel("20260101T000000Z-00000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel unknown: %v", err)
	}
}

func TestPanickingStepFailsItsItem(t *testing.T) {
	m := New(Options{})
	job := start(t, m, Spec{Kind: Export, Target: "dev", Instance: "db1", Items: []ItemSpec{
		{Name: "a", Steps: []Step{NewStep("backup", "Backup", ResourceRDSTask, func(context.Context, Reporter) (string, error) {
			panic("boom")
		})}},
		{Name: "b", Steps: []Step{NewStep("backup", "Backup", ResourceRDSTask, func(context.Context, Reporter) (string, error) { return "", nil })}},
	}})
	got := waitJob(t, m, job.ID)
	if got.State != Failed || got.Items[0].State != Failed || !strings.Contains(got.Items[0].Error, "panicked: boom") || got.Items[1].State != Succeeded {
		t.Fatalf("job %s, items %+v", got.State, got.Items)
	}
}

func TestReporter(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	m := New(Options{Now: clock})
	gate, subscribed := make(chan struct{}), make(chan struct{})
	var late Reporter
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{{Name: "sales", Steps: []Step{
		NewStep("upload", "Upload", ResourceTransfer, func(_ context.Context, r Reporter) (string, error) {
			<-subscribed
			const total = 1000 << 20
			r.Upload(UploadState{Bucket: "b", Key: "import/sales.bak", UploadID: "up-1", Size: total, PartSize: 100 << 20, PartsDone: []int{1}})
			r.Transfer(Upload, 0, total)
			advance(time.Second)
			r.Transfer(Upload, 100<<20, total) // 100 MiB/s
			r.Task(TaskStatus{ID: 7, Lifecycle: mssql.Created})
			r.Task(TaskStatus{ID: 7, Lifecycle: mssql.InProgress, Percent: 10})
			r.Task(TaskStatus{ID: 7, Lifecycle: mssql.InProgress, Percent: 150})
			r.Log(Info, "parts 1 of 10 done")
			late = r
			<-gate
			return "uploaded", nil
		}),
	}}}})
	sub, err := m.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	close(subscribed)
	var live []Event
	for ev := range sub.C {
		live = append(live, ev)
		if ev.Type == EventLog {
			break
		}
	}
	snap, _ := m.Get(job.ID)
	it := snap.Items[0]
	if it.Transfer == nil || it.Transfer.BytesPerSecond != 100<<20 || it.Transfer.ETASeconds != 9 || it.Transfer.Direction != Upload {
		t.Fatalf("transfer %+v", it.Transfer)
	}
	if it.Task == nil || it.Task.Percent != 100 || it.Percent != 100 || !slices.Equal(it.TaskIDs, []int{7}) {
		t.Fatalf("task %+v, percent %d, ids %v", it.Task, it.Percent, it.TaskIDs)
	}
	if it.Upload == nil || it.Upload.UploadID != "up-1" || !slices.Equal(it.Upload.PartsDone, []int{1}) {
		t.Fatalf("upload %+v", it.Upload)
	}
	// Progress goes to the live subscribers; the log keeps the task when
	// it appears or changes lifecycle, not the percentages.
	count := func(evs []Event, typ EventType) int {
		n := 0
		for _, ev := range evs {
			if ev.Type == typ {
				n++
			}
		}
		return n
	}
	if count(live, EventUploadProgress) != 2 || count(live, EventTaskStatus) != 3 {
		t.Fatalf("live events %v", live)
	}
	if count(snap.Events, EventUploadProgress) != 0 || count(snap.Events, EventTaskStatus) != 2 {
		t.Fatalf("logged events %v", eventTypes(snap))
	}
	for i := 1; i < len(live); i++ {
		if live[i].Seq != live[i-1].Seq+1 {
			t.Fatalf("seq not contiguous: %d then %d", live[i-1].Seq, live[i].Seq)
		}
	}
	close(gate)
	waitJob(t, m, job.ID)
	late.Log(Error, "after the end")
	late.Result("ignored")
	final, _ := m.Get(job.ID)
	if final.Items[0].Result != "" || final.Items[0].Steps[0].Detail != "uploaded" || final.Events[len(final.Events)-1].Type != EventJobSucceeded {
		t.Fatalf("a reporter used after its step returned changed the job: %+v", final.Items[0])
	}
}

func TestSubscribeReplayAndLag(t *testing.T) {
	m := New(Options{})
	gate := make(chan struct{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{{Name: "a", Steps: []Step{
		NewStep("noisy", "Noisy", NoResource, func(_ context.Context, r Reporter) (string, error) {
			r.Log(Info, "first")
			<-gate
			for i := range subBuffer + 10 {
				r.Log(Info, fmt.Sprint("line ", i))
			}
			return "", nil
		}),
	}}}})
	eventually(t, "the first log line", func() bool {
		j, _ := m.Get(job.ID)
		return slices.Contains(eventTypes(j), EventLog)
	})
	snap, _ := m.Get(job.ID)
	after := snap.Events[0].Seq // job.queued
	sub, err := m.Subscribe(job.ID, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Replay) != len(snap.Events)-1 || sub.Replay[0].Seq != after+1 {
		t.Fatalf("replay %v", sub.Replay)
	}
	// Nobody reads: the subscriber falls behind and is dropped.
	close(gate)
	waitJob(t, m, job.ID)
	n := 0
	for range sub.C {
		n++
	}
	if !sub.Lagged() || n != subBuffer {
		t.Fatalf("lagged %v after %d events", sub.Lagged(), n)
	}
	// Subscribing to an ended job: the whole log, and C closed at once.
	ended, _ := m.Subscribe(job.ID, 0)
	if _, open := <-ended.C; open || ended.Replay[len(ended.Replay)-1].Type != EventJobSucceeded || ended.Lagged() {
		t.Fatal("subscription to an ended job")
	}
	if _, err := m.Subscribe("nope", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestEventLogIsCapped(t *testing.T) {
	m := New(Options{})
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{{Name: "a", Steps: []Step{
		NewStep("chatty", "Chatty", NoResource, func(_ context.Context, r Reporter) (string, error) {
			for range maxEvents + 100 {
				r.Log(Info, "x")
			}
			return "", nil
		}),
	}}}})
	got := waitJob(t, m, job.ID)
	if len(got.Events) > maxEvents || got.Events[len(got.Events)-1].Type != EventJobSucceeded {
		t.Fatalf("%d events logged", len(got.Events))
	}
}

func TestStartValidates(t *testing.T) {
	m := New(Options{})
	ok := []Step{NewStep("s", "S", NoResource, func(context.Context, Reporter) (string, error) { return "", nil })}
	tests := map[string]Spec{
		"no kind":        {Target: "dev", Items: []ItemSpec{{Name: "a", Steps: ok}}},
		"no target":      {Kind: Import, Items: []ItemSpec{{Name: "a", Steps: ok}}},
		"no items":       {Kind: Import, Target: "dev"},
		"max tasks":      {Kind: Import, Target: "dev", MaxTasks: 3, Items: []ItemSpec{{Name: "a", Steps: ok}}},
		"repeated item":  {Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: ok}, {Name: "a", Steps: ok}}},
		"no steps":       {Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a"}}},
		"repeated steps": {Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: append(slices.Clone(ok), ok...)}}},
		"nil step":       {Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: []Step{nil}}}},
	}
	for name, spec := range tests {
		if _, err := m.Start(spec); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(m.List()) != 0 {
		t.Fatal("an invalid spec left a job behind")
	}
}

func TestListNewestFirstAndDefaults(t *testing.T) {
	m := New(Options{})
	noop := []Step{NewStep("s", "S", NoResource, func(context.Context, Reporter) (string, error) { return "", nil })}
	first := start(t, m, Spec{Kind: Import, Target: "dev", Items: []ItemSpec{{Name: "a", Steps: noop}}})
	time.Sleep(time.Millisecond)
	second := start(t, m, Spec{Kind: Export, Target: "dev", Items: []ItemSpec{{Name: "b", Steps: noop}}})
	waitJob(t, m, first.ID)
	waitJob(t, m, second.ID)
	list := m.List()
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list %+v", list)
	}
	if first.MaxTasks != 2 || first.State != Queued || !ValidID(first.ID) || first.Events[0].Type != EventJobQueued {
		t.Fatalf("first snapshot %+v", first)
	}
}

func TestFromAction(t *testing.T) {
	m := New(Options{})
	ran := false
	act := core.Action{ID: "restore", Title: "Restore sales", Kind: core.KindSQL, Risk: core.Change,
		Run: func(context.Context) (string, error) { ran = true; return "task 9", nil }}
	job := start(t, m, Spec{Kind: Import, Target: "dev", Instance: "db1", Items: []ItemSpec{
		{Name: "sales", Steps: []Step{FromAction(act, ResourceRDSTask)}},
		{Name: "norunner", Steps: []Step{FromAction(core.Action{ID: "x", Title: "X"}, NoResource)}},
	}})
	got := waitJob(t, m, job.ID)
	if !ran || got.Items[0].Steps[0].Detail != "task 9" || got.Items[0].Steps[0].Resource != ResourceRDSTask || got.Items[1].State != Failed {
		t.Fatalf("job %+v", got.Items)
	}
}

func TestLimiterCancelWhileWaiting(t *testing.T) {
	l := newLimiter()
	rel, err := l.acquire(context.Background(), "k", 1, priority{1, 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	waited := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		_, err := l.acquire(ctx, "k", 1, priority{1, 1}, func() { close(waited) })
		errc <- err
	}()
	<-waited
	cancel(ErrCancelled)
	if err := <-errc; !errors.Is(err, ErrCancelled) {
		t.Fatalf("acquire after cancel: %v", err)
	}
	rel()
	rel() // twice is harmless
	// The slot is free again, and the cancelled waiter left no trace.
	rel2, err := l.acquire(context.Background(), "k", 1, priority{2, 0}, func() { t.Error("had to wait") })
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	if p := l.pools["k"]; p.inUse != 0 || len(p.waiting) != 0 {
		t.Fatalf("pool %+v", p)
	}
}
