package jobs

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decisyon/dialga/engine/internal/transfer"
)

var (
	// ErrCancelled is the context cause a step sees when the user cancels.
	ErrCancelled = errors.New("job cancelled")
	// ErrInterrupted is the context cause a step sees when the engine stops.
	// It is a transfer.ErrPaused too, so an upload it stops keeps its
	// multipart upload for a resume instead of aborting it.
	ErrInterrupted error = interruptedError{}

	ErrNotFound = errors.New("job not found")
	ErrEnded    = errors.New("job already ended")
	ErrClosed   = errors.New("job manager closed")
)

type interruptedError struct{}

func (interruptedError) Error() string { return "the engine stopped" }

func (interruptedError) Is(target error) bool { return target == transfer.ErrPaused }

// Defaults of Options.
const (
	DefaultTransfers = 3
	DefaultKeep      = 200
	DefaultMaxAge    = 30 * 24 * time.Hour
	defaultSaveEvery = 2 * time.Second
)

const (
	maxEvents     = 5000                   // logged events kept per job
	subBuffer     = 256                    // live events a subscriber may lag
	progressEvery = 250 * time.Millisecond // upload.progress events per item
	rateWindow    = 500 * time.Millisecond // shortest sample of a transfer speed
)

// Options configures a Manager.
type Options struct {
	// Limits are the slots of the global resources, all jobs together
	// (default: ResourceTransfer 3). A resource not listed gets 1.
	// ResourceRDSTask is per instance: its limit is Spec.MaxTasks.
	Limits map[Resource]int
	// Keep and MaxAge bound the history: an ended job is removed when it
	// is older than MaxAge or not among the Keep most recent (defaults 200
	// and 30 days). Jobs that have not ended are never removed.
	Keep   int
	MaxAge time.Duration
	// SaveEvery throttles the journal writes of progress-only changes
	// (default 2s); state changes are written at once.
	SaveEvery time.Duration
	Log       *slog.Logger
	// Now is the clock of the event times (default time.Now).
	Now func() time.Time
}

// Spec describes a job to start.
type Spec struct {
	Kind   Kind
	Target string
	// Instance is the RDS instance identifier, shown to the user.
	Instance string
	// InstanceKey identifies the instance for the RDS slots across
	// targets (e.g. account/region/instance); default Instance.
	InstanceKey string
	// MaxTasks is the native tasks at once on the instance: 1 or 2
	// (default 2, the RDS maximum).
	MaxTasks int
	Items    []ItemSpec
}

// ItemSpec is one database of a job and the steps it goes through.
type ItemSpec struct {
	Name  string
	Steps []Step
}

func (s Spec) validate() error {
	switch {
	case s.Kind == "":
		return errors.New("job: kind missing")
	case s.Target == "":
		return errors.New("job: target missing")
	case s.MaxTasks < 0 || s.MaxTasks > 2:
		return fmt.Errorf("job: max tasks %d, must be 1 or 2", s.MaxTasks)
	case len(s.Items) == 0:
		return errors.New("job: no databases")
	}
	names := map[string]bool{}
	for _, it := range s.Items {
		if it.Name == "" || names[it.Name] {
			return fmt.Errorf("job: database name %q empty or repeated", it.Name)
		}
		names[it.Name] = true
		if len(it.Steps) == 0 {
			return fmt.Errorf("job: database %s has no steps", it.Name)
		}
		ids := map[string]bool{}
		for _, st := range it.Steps {
			if st == nil || st.ID() == "" || ids[st.ID()] {
				return fmt.Errorf("job: database %s: step id empty or repeated", it.Name)
			}
			ids[st.ID()] = true
		}
	}
	return nil
}

// Manager runs the jobs and keeps their history.
type Manager struct {
	opts    Options
	log     *slog.Logger
	lim     *limiter
	journal *journal // nil: memory only
	// journalOff stops the writes once Close released the journal.
	journalOff atomic.Bool

	mu     sync.Mutex
	jobs   map[string]*run
	seq    int64
	closed bool
	wg     sync.WaitGroup
}

// New returns a manager that keeps its jobs in memory only.
func New(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Keep <= 0 {
		opts.Keep = DefaultKeep
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = DefaultMaxAge
	}
	if opts.SaveEvery <= 0 {
		opts.SaveEvery = defaultSaveEvery
	}
	limits := map[Resource]int{ResourceTransfer: DefaultTransfers}
	for res, n := range opts.Limits {
		limits[res] = n
	}
	opts.Limits = limits
	return &Manager{opts: opts, log: opts.Log, lim: newLimiter(), jobs: map[string]*run{}}
}

// Open returns a manager with its journal in dir (DefaultDir in the
// engine): it takes the directory lock (ErrLocked if another engine holds
// it), loads the jobs, marks those that were running as interrupted and
// applies the retention.
func Open(dir string, opts Options) (*Manager, error) {
	j, err := openJournal(dir)
	if err != nil {
		return nil, err
	}
	m := New(opts)
	m.journal = j
	loaded, errs := j.load()
	for _, err := range errs {
		m.log.Warn("job journal: file skipped", "err", err)
	}
	for _, job := range loaded {
		r := &run{m: m, job: job, cancel: func(error) {}, done: make(chan struct{}), subs: map[*Subscription]struct{}{}}
		if n := len(job.Events); n > 0 {
			r.seq = job.Events[n-1].Seq
		}
		r.mu.Lock()
		if job.State.Ended() {
			r.ended = true
			close(r.done)
		} else {
			r.interrupt("The engine stopped while the job was running.")
		}
		r.mu.Unlock()
		m.jobs[job.ID] = r
	}
	m.prune()
	return m, nil
}

// Start runs a job in the background and returns its first snapshot.
func (m *Manager) Start(spec Spec) (Job, error) {
	if err := spec.validate(); err != nil {
		return Job{}, err
	}
	if spec.MaxTasks == 0 {
		spec.MaxTasks = 2
	}
	key := cmp.Or(spec.InstanceKey, spec.Instance, spec.Target)
	now := m.opts.Now()
	r := &run{
		m: m, key: key, maxTasks: spec.MaxTasks,
		done: make(chan struct{}), subs: map[*Subscription]struct{}{},
		rates: make([]rate, len(spec.Items)),
		job: Job{
			Kind: spec.Kind, Target: spec.Target, Instance: spec.Instance, MaxTasks: spec.MaxTasks,
			State: Queued, CreatedAt: now, UpdatedAt: now, Items: []Item{}, Events: []Event{},
		},
	}
	for _, it := range spec.Items {
		item := Item{Name: it.Name, State: Queued, Steps: []StepInfo{}}
		for _, st := range it.Steps {
			item.Steps = append(item.Steps, StepInfo{ID: st.ID(), Title: st.Title(), Resource: st.Resource(), Status: StepPending})
		}
		r.job.Items = append(r.job.Items, item)
		r.steps = append(r.steps, slices.Clone(it.Steps))
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	r.cancel = cancel

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(ErrClosed)
		return Job{}, ErrClosed
	}
	for {
		r.job.ID = newID(now)
		if _, dup := m.jobs[r.job.ID]; !dup {
			break
		}
	}
	m.seq++
	r.prio = m.seq
	m.jobs[r.job.ID] = r
	m.wg.Add(1)
	m.mu.Unlock()

	r.mu.Lock()
	r.emit(Event{Type: EventJobQueued, Message: fmt.Sprintf("%s of %d database(s) on %s", spec.Kind, len(spec.Items), spec.Target)}, true)
	r.save(true)
	snap := r.job.clone(true)
	r.mu.Unlock()
	go r.execute(ctx)
	return snap, nil
}

func newID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b) // crypto/rand never fails on supported systems
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

func (m *Manager) run(id string) (*run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

// Get returns a snapshot of the job, with its events.
func (m *Manager) Get(id string) (Job, error) {
	r, err := m.run(id)
	if err != nil {
		return Job{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job.clone(true), nil
}

// List returns the jobs, newest first.
func (m *Manager) List() []Summary {
	m.mu.Lock()
	runs := make([]*run, 0, len(m.jobs))
	for _, r := range m.jobs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	list := make([]Summary, 0, len(runs))
	for _, r := range runs {
		r.mu.Lock()
		list = append(list, r.job.Summary())
		r.mu.Unlock()
	}
	slices.SortFunc(list, func(a, b Summary) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return list
}

// Cancel asks a job to stop: queued and waiting steps do not start, running
// steps see their context cancelled (cause ErrCancelled). The job ends when
// its steps have returned.
func (m *Manager) Cancel(id string) (Summary, error) {
	r, err := m.run(id)
	if err != nil {
		return Summary{}, err
	}
	r.mu.Lock()
	if r.ended {
		s := r.job.Summary()
		r.mu.Unlock()
		return s, ErrEnded
	}
	if !r.job.CancelRequested {
		r.job.CancelRequested = true
		r.emit(Event{Type: EventJobCancelling, Message: "Cancel requested: running native tasks are not stopped."}, true)
		r.save(true)
	}
	s := r.job.Summary()
	r.mu.Unlock()
	r.cancel(ErrCancelled)
	return s, nil
}

// Wait blocks until the job ends (or ctx does) and returns its last
// snapshot.
func (m *Manager) Wait(ctx context.Context, id string) (Job, error) {
	r, err := m.run(id)
	if err != nil {
		return Job{}, err
	}
	select {
	case <-r.done:
		return m.Get(id)
	case <-ctx.Done():
		return Job{}, context.Cause(ctx)
	}
}

// Subscription is a live view of the events of a job.
type Subscription struct {
	// Replay are the logged events after the sequence number asked for.
	Replay []Event
	// C delivers the events that follow Replay. It is closed after the
	// final job.* event, or early when the subscriber fell behind (Lagged):
	// it then subscribes again after the last Seq it received.
	C <-chan Event

	r      *run
	ch     chan Event
	lagged bool
}

// Lagged reports whether C was closed because the subscriber fell behind.
// Read it after C is closed.
func (s *Subscription) Lagged() bool { return s.lagged }

// Close ends the subscription.
func (s *Subscription) Close() {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if _, ok := s.r.subs[s]; ok {
		delete(s.r.subs, s)
		close(s.ch)
	}
}

// Subscribe returns the logged events after the sequence number after,
// then the live ones until the job ends.
func (m *Manager) Subscribe(id string, after int64) (*Subscription, error) {
	r, err := m.run(id)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan Event, subBuffer)
	s := &Subscription{r: r, ch: ch, C: ch}
	for _, ev := range r.job.Events {
		if ev.Seq > after {
			s.Replay = append(s.Replay, ev)
		}
	}
	if r.ended {
		close(ch)
	} else {
		r.subs[s] = struct{}{}
	}
	return s, nil
}

// Close stops the manager: no new jobs, the running ones are interrupted
// (their steps see the cause ErrInterrupted) and written to the journal.
// Steps that have not returned when ctx ends are left behind and their jobs
// are written as interrupted anyway.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runs := make([]*run, 0, len(m.jobs))
	for _, r := range m.jobs {
		runs = append(runs, r)
	}
	m.mu.Unlock()

	for _, r := range runs {
		r.cancel(ErrInterrupted)
	}
	stopped := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(stopped)
	}()
	var err error
	select {
	case <-stopped:
	case <-ctx.Done():
		err = fmt.Errorf("jobs: steps still running at shutdown: %w", context.Cause(ctx))
		for _, r := range runs {
			r.mu.Lock()
			if !r.ended {
				r.interrupt("The engine stopped before the job's steps returned.")
			}
			r.mu.Unlock()
		}
	}
	if m.journal != nil {
		m.journalOff.Store(true)
		err = errors.Join(err, m.journal.close())
	}
	return err
}

// prune applies the retention to the ended jobs.
func (m *Manager) prune() {
	type ended struct {
		id string
		at time.Time
	}
	now := m.opts.Now()
	m.mu.Lock()
	var list []ended
	for id, r := range m.jobs {
		r.mu.Lock()
		if r.ended {
			at := r.job.UpdatedAt
			if r.job.EndedAt != nil {
				at = *r.job.EndedAt
			}
			list = append(list, ended{id, at})
		}
		r.mu.Unlock()
	}
	slices.SortFunc(list, func(a, b ended) int { return cmp.Or(b.at.Compare(a.at), cmp.Compare(b.id, a.id)) })
	var drop []string
	for i, e := range list {
		if i >= m.opts.Keep || now.Sub(e.at) > m.opts.MaxAge {
			drop = append(drop, e.id)
			delete(m.jobs, e.id)
		}
	}
	m.mu.Unlock()
	if m.journal == nil || m.journalOff.Load() {
		return
	}
	for _, id := range drop {
		if err := m.journal.remove(id); err != nil {
			m.log.Warn("job journal: cannot remove an old job", "job", id, "err", err)
		}
	}
}

// run is one job in the manager.
type run struct {
	m        *Manager
	key      string // the instance, for the RDS slots
	maxTasks int
	prio     int64
	steps    [][]Step // per item; nil for a job loaded from the journal

	mu       sync.Mutex
	job      Job
	seq      int64
	ended    bool
	cancel   context.CancelCauseFunc
	done     chan struct{} // closed when the job ended
	subs     map[*Subscription]struct{}
	rates    []rate // per item: the speed of its transfer
	lastSave time.Time
	dirty    bool
	flush    *time.Timer
}

type rate struct {
	bytes    int64
	at       time.Time
	perSec   float64
	lastEmit time.Time
}

func (r *run) execute(ctx context.Context) {
	defer r.m.wg.Done()
	// The first slot of every item is queued here, in item order.
	first := make([]*ticket, len(r.steps))
	for i, steps := range r.steps {
		if res := steps[0].Resource(); res != NoResource {
			first[i] = r.m.lim.enqueue(r.poolKey(res), r.limit(res), priority{r.prio, i})
		}
	}
	var wg sync.WaitGroup
	for i := range r.steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.runItem(ctx, i, first[i])
		}()
	}
	wg.Wait()
	r.finish()
	r.m.prune()
}

// runItem runs the steps of item i; first is the queued request for the
// slot of its first step (nil for NoResource).
func (r *run) runItem(ctx context.Context, i int, first *ticket) {
	var (
		held    Resource
		release func()
	)
	defer func() {
		if release != nil {
			release()
		}
	}()
	for s, step := range r.steps[i] {
		if res := step.Resource(); res != held {
			// Give back the slot before waiting for another: an item never
			// holds one resource while it waits for the next.
			if release != nil {
				release()
				release = nil
			}
			held = NoResource
			if res != NoResource {
				t := first
				if s > 0 || t == nil {
					t = r.m.lim.enqueue(r.poolKey(res), r.limit(res), priority{r.prio, i})
				}
				rel, err := t.wait(ctx, func() { r.stepWaiting(i, s) })
				if err != nil {
					r.stop(ctx, i, s)
					return
				}
				release, held = rel, res
			}
		}
		if ctx.Err() != nil {
			r.stop(ctx, i, s)
			return
		}
		r.stepStarted(i, s)
		detail, err := runStep(ctx, step, &reporter{r: r, item: i, step: s})
		switch {
		case err != nil && ctx.Err() != nil:
			r.stop(ctx, i, s)
			return
		case err != nil:
			r.stepFailed(i, s, err)
			return
		}
		r.stepSucceeded(i, s, detail)
	}
	r.itemSucceeded(i)
}

// runStep turns a panic of a step into its error, so one bad step cannot
// take the engine down.
func runStep(ctx context.Context, step Step, rep Reporter) (detail string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("step %s panicked: %v", step.ID(), p)
		}
	}()
	return step.Run(ctx, rep)
}

func (r *run) poolKey(res Resource) string {
	if res == ResourceRDSTask {
		return string(res) + "/" + r.key
	}
	return string(res)
}

func (r *run) limit(res Resource) int {
	if res == ResourceRDSTask {
		return r.maxTasks
	}
	if n, ok := r.m.opts.Limits[res]; ok {
		return n
	}
	return 1
}

// The state changes below hold r.mu and do nothing once the job ended
// (Close may have written it as interrupted while a step still ran).

func (r *run) stepWaiting(i, s int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	it, st := r.at(i, s)
	st.Status = StepWaiting
	it.Step = st.ID
	r.emit(Event{Type: EventStepWaiting, Item: it.Name, Step: st.ID, Message: "Waiting for a free " + string(st.Resource) + " slot."}, true)
	r.save(false)
}

func (r *run) stepStarted(i, s int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	it, st := r.at(i, s)
	now := r.m.opts.Now()
	st.Status, st.StartedAt = StepRunning, &now
	it.Step, it.Percent, it.Transfer = st.ID, 0, nil
	r.rates[i] = rate{}
	it.State = Running
	if r.job.State == Queued {
		r.job.State = Running
		r.emit(Event{Type: EventJobStarted}, true)
	}
	r.emit(Event{Type: EventStepStarted, Item: it.Name, Step: st.ID, Message: st.Title}, true)
	r.save(true)
}

func (r *run) stepSucceeded(i, s int, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	it, st := r.at(i, s)
	now := r.m.opts.Now()
	st.Status, st.EndedAt, st.Detail = StepSucceeded, &now, detail
	it.Percent = 100
	r.emit(Event{Type: EventStepSucceeded, Item: it.Name, Step: st.ID, Message: detail}, true)
	r.save(true)
}

func (r *run) stepFailed(i, s int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	it, st := r.at(i, s)
	now := r.m.opts.Now()
	st.Status, st.EndedAt, st.Detail = StepFailed, &now, err.Error()
	it.State, it.Error = Failed, err.Error()
	r.emit(Event{Type: EventStepFailed, Item: it.Name, Step: st.ID, Message: err.Error()}, true)
	for k := s + 1; k < len(it.Steps); k++ {
		it.Steps[k].Status = StepSkipped
		r.emit(Event{Type: EventStepSkipped, Item: it.Name, Step: it.Steps[k].ID, Message: "An earlier step failed."}, true)
	}
	r.save(true)
}

// stop ends item i at step s because ctx ended: cancelled by the user, or
// interrupted by the engine stopping (the steps not reached stay pending,
// for a resume).
func (r *run) stop(ctx context.Context, i, s int) {
	interrupted := errors.Is(context.Cause(ctx), ErrInterrupted)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	it, _ := r.at(i, s)
	now := r.m.opts.Now()
	for k := s; k < len(it.Steps); k++ {
		st := &it.Steps[k]
		switch {
		case interrupted && (st.Status == StepRunning || st.Status == StepWaiting):
			st.Status, st.EndedAt = StepInterrupted, &now
			r.emit(Event{Type: EventStepInterrupted, Item: it.Name, Step: st.ID, Message: "The engine stopped."}, true)
		case !interrupted:
			st.Status = StepCancelled
			if k == s {
				st.EndedAt = &now
			}
			r.emit(Event{Type: EventStepCancelled, Item: it.Name, Step: st.ID}, true)
		}
	}
	if interrupted {
		it.State = Interrupted
	} else {
		it.State = Cancelled
	}
	r.leftRunning(it)
	r.save(true)
}

// leftRunning records a native task the item leaves running on the
// instance: the engine never cancels it on its own.
func (r *run) leftRunning(it *Item) {
	if it.Task == nil || it.Task.Lifecycle.Done() {
		return
	}
	it.TaskLeftRunning = true
	r.emit(Event{Type: EventLog, Level: Warn, Item: it.Name, Message: fmt.Sprintf(
		"Native task %d (%s) is still running on the instance: Dialga does not stop it. Follow it with \"dialga wait %d\".",
		it.Task.ID, it.Task.Lifecycle, it.Task.ID)}, true)
}

func (r *run) itemSucceeded(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.job.Items[i].State = Succeeded
	r.save(true)
}

// finish ends the job once every item ended.
func (r *run) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	var interrupted, cancelled, failed int
	for _, it := range r.job.Items {
		switch it.State {
		case Interrupted:
			interrupted++
		case Cancelled:
			cancelled++
		case Failed:
			failed++
		}
	}
	n := len(r.job.Items)
	switch {
	case interrupted > 0:
		r.end(Interrupted, "The engine stopped while the job was running.")
	case cancelled > 0:
		r.end(Cancelled, fmt.Sprintf("Cancelled: %d of %d database(s) not done.", cancelled+failed, n))
	case failed > 0:
		r.end(Failed, fmt.Sprintf("%d of %d database(s) failed.", failed, n))
	default:
		r.end(Succeeded, fmt.Sprintf("%d database(s) done.", n))
	}
}

// interrupt ends a job whose steps will not report any more: loaded from
// the journal while running, or left behind by Close.
func (r *run) interrupt(msg string) {
	now := r.m.opts.Now()
	for i := range r.job.Items {
		it := &r.job.Items[i]
		if it.State.Ended() {
			continue
		}
		for k := range it.Steps {
			st := &it.Steps[k]
			if st.Status == StepRunning || st.Status == StepWaiting {
				st.Status, st.EndedAt = StepInterrupted, &now
				r.emit(Event{Type: EventStepInterrupted, Item: it.Name, Step: st.ID, Message: "The engine stopped."}, true)
			}
		}
		it.State = Interrupted
		r.leftRunning(it)
	}
	r.end(Interrupted, msg)
}

func (r *run) end(state State, msg string) {
	now := r.m.opts.Now()
	r.job.State, r.job.EndedAt = state, &now
	r.emit(Event{Type: EventType("job." + string(state)), Message: msg}, true)
	r.ended = true
	r.save(true)
	for s := range r.subs {
		delete(r.subs, s)
		close(s.ch)
	}
	close(r.done)
}

func (r *run) at(i, s int) (*Item, *StepInfo) {
	it := &r.job.Items[i]
	return it, &it.Steps[s]
}

// emit numbers an event, logs it if durable and sends it to the
// subscribers (r.mu held). A subscriber too slow for a logged event is
// dropped, so it subscribes again from its last Seq; a progress event is
// just skipped for it.
func (r *run) emit(ev Event, durable bool) {
	r.seq++
	ev.Seq = r.seq
	ev.Time = r.m.opts.Now()
	r.job.UpdatedAt = ev.Time
	if durable {
		r.job.Events = append(r.job.Events, ev)
		if len(r.job.Events) > maxEvents {
			n := copy(r.job.Events, r.job.Events[len(r.job.Events)-maxEvents*9/10:])
			r.job.Events = r.job.Events[:n]
		}
	}
	for s := range r.subs {
		select {
		case s.ch <- ev:
		default:
			if durable {
				s.lagged = true
				delete(r.subs, s)
				close(s.ch)
			}
		}
	}
}

// save writes the job to the journal (r.mu held): at once when force,
// otherwise at most every Options.SaveEvery, with a timer for the last
// change.
func (r *run) save(force bool) {
	if r.m.journal == nil {
		return
	}
	every := r.m.opts.SaveEvery
	if since := time.Since(r.lastSave); !force && since < every {
		r.dirty = true
		if r.flush == nil {
			r.flush = time.AfterFunc(every-since, r.flushDirty)
		}
		return
	}
	r.write()
}

func (r *run) flushDirty() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flush = nil
	if r.dirty {
		r.write()
	}
}

func (r *run) write() {
	if r.flush != nil {
		r.flush.Stop()
		r.flush = nil
	}
	r.lastSave, r.dirty = time.Now(), false
	if r.m.journalOff.Load() {
		return
	}
	data, err := json.Marshal(r.job)
	if err == nil {
		err = r.m.journal.write(r.job.ID, data)
	}
	if err != nil {
		r.m.log.Warn("job journal: write failed", "job", r.job.ID, "err", err)
	}
}

// reporter is the Reporter of one step of one item.
type reporter struct {
	r          *run
	item, step int
}

// live returns the item while its step still runs (r.mu held).
func (p *reporter) live() (*Item, bool) {
	if p.r.ended {
		return nil, false
	}
	it, st := p.r.at(p.item, p.step)
	return it, st.Status == StepRunning
}

func (p *reporter) Item() string {
	p.r.mu.Lock()
	defer p.r.mu.Unlock()
	return p.r.job.Items[p.item].Name
}

func (p *reporter) Transfer(dir Direction, done, total int64) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := p.live()
	if !ok {
		return
	}
	now := r.m.opts.Now()
	rt := &r.rates[p.item]
	switch dt := now.Sub(rt.at); {
	case rt.at.IsZero() || done < rt.bytes:
		rt.bytes, rt.at = done, now // first sample, or the transfer restarted
	case dt >= rateWindow:
		inst := float64(done-rt.bytes) / dt.Seconds()
		if rt.perSec == 0 {
			rt.perSec = inst
		} else {
			rt.perSec = 0.3*inst + 0.7*rt.perSec
		}
		rt.bytes, rt.at = done, now
	}
	tp := &TransferProgress{Direction: dir, Bytes: done, Total: total, BytesPerSecond: int64(rt.perSec)}
	if rt.perSec > 0 && total > done {
		tp.ETASeconds = int64(math.Ceil(float64(total-done) / rt.perSec))
	}
	it.Transfer = tp
	if total > 0 {
		it.Percent = clampPercent(int(done * 100 / total))
	}
	r.job.UpdatedAt = now
	if rt.lastEmit.IsZero() || now.Sub(rt.lastEmit) >= progressEvery || done >= total {
		rt.lastEmit = now
		r.emit(Event{Type: EventUploadProgress, Item: it.Name, Step: it.Step, Transfer: tp}, false)
	}
	r.save(false)
}

func (p *reporter) Task(ts TaskStatus) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := p.live()
	if !ok {
		return
	}
	ts.Percent = clampPercent(ts.Percent)
	prev := it.Task
	it.Task, it.Percent = &ts, ts.Percent
	if ts.ID != 0 && !slices.Contains(it.TaskIDs, ts.ID) {
		it.TaskIDs = append(it.TaskIDs, ts.ID)
	}
	durable := prev == nil || prev.ID != ts.ID || prev.Lifecycle != ts.Lifecycle
	r.emit(Event{Type: EventTaskStatus, Item: it.Name, Step: it.Step, Task: &ts}, durable)
	r.save(durable)
}

func (p *reporter) Upload(u UploadState) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := p.live()
	if !ok {
		return
	}
	u.PartsDone = append([]int{}, u.PartsDone...)
	prev := it.Upload
	it.Upload = &u
	r.job.UpdatedAt = r.m.opts.Now()
	r.save(prev == nil || prev.UploadID != u.UploadID)
}

func (p *reporter) Log(level Level, msg string) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := p.live()
	if !ok {
		return
	}
	r.emit(Event{Type: EventLog, Level: level, Item: it.Name, Step: it.Step, Message: msg}, true)
	r.save(false)
}

func (p *reporter) Result(s string) {
	r := p.r
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := p.live()
	if !ok {
		return
	}
	it.Result = s
	r.job.UpdatedAt = r.m.opts.Now()
	r.save(false)
}

func clampPercent(p int) int { return min(max(p, 0), 100) }
