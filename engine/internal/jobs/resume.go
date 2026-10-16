package jobs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
)

var (
	// ErrNotInterrupted means the job is not interrupted: only those resume.
	ErrNotInterrupted = errors.New("only an interrupted job can be resumed")
	// ErrNotResumable means a step the job still has to run cannot be
	// rebuilt (no kind recorded, no factory for it, or the factory refused).
	ErrNotResumable = errors.New("the job cannot be resumed")
)

// ResumeOptions is what the caller of Resume knows about the target now.
type ResumeOptions struct {
	// Env goes to every factory (Rebuild.Env): how the steps reach the target.
	Env any
	// InstanceKey identifies the instance for the RDS slots, like
	// Spec.InstanceKey (default: the job's instance).
	InstanceKey string
}

// Resume runs an interrupted job again, under the same id: the history of
// the job goes on (a job.resumed event, then the events of the rest).
//
// The items that ended (succeeded, failed, cancelled) are kept as they are,
// and so are the steps that succeeded. Every other step is rebuilt by the
// factory registered for its kind (Options.Registry), which gets what the
// journal kept: the step's params and task id, the item's multipart upload
// and native tasks. A factory that returns no step marks it succeeded (its
// work was done before the interrupt). If any step cannot be rebuilt the
// job stays interrupted and nothing changes.
//
// Confirmation (the target name of a protected target) is the caller's,
// as for Start.
func (m *Manager) Resume(id string, opts ResumeOptions) (Job, error) {
	// Held to the end: two resumes of one job cannot both start it, and
	// factories only build steps.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Job{}, ErrClosed
	}
	old, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	old.mu.Lock()
	before, seq := old.job.clone(true), old.seq
	old.mu.Unlock()
	if before.State != Interrupted {
		return Job{}, fmt.Errorf("%w: job %s is %s", ErrNotInterrupted, id, before.State)
	}

	job := before.clone(true)
	steps := make([][]Step, len(job.Items))
	resumed := 0
	for i, it := range before.Items {
		if it.State != Interrupted {
			continue
		}
		list := make([]Step, len(it.Steps))
		for s, st := range it.Steps {
			if st.Status == StepSucceeded {
				continue
			}
			step, err := m.rebuildStep(Rebuild{Job: before, Item: it, Step: st, Env: opts.Env})
			if err != nil {
				return Job{}, fmt.Errorf("%w: database %s, step %s: %v", ErrNotResumable, it.Name, st.ID, err)
			}
			list[s] = step
		}
		steps[i] = list
		resumed++
	}
	if resumed == 0 {
		return Job{}, fmt.Errorf("%w: no database left to finish", ErrNotResumable)
	}

	now := m.opts.Now()
	for i, list := range steps {
		if list == nil {
			continue
		}
		it := &job.Items[i]
		it.State, it.Error, it.Percent, it.Transfer, it.TaskLeftRunning = Queued, "", 0, nil, false
		for s := range it.Steps {
			st := &it.Steps[s]
			switch {
			case st.Status == StepSucceeded:
			case list[s] == nil:
				st.Status, st.EndedAt, st.Detail = StepSucceeded, &now, "Done before the interrupt."
			default:
				st.Status, st.StartedAt, st.EndedAt, st.Detail = StepPending, nil, nil, ""
			}
		}
	}
	job.State, job.EndedAt, job.CancelRequested = Queued, nil, false

	r := &run{
		m: m, key: cmp.Or(opts.InstanceKey, job.Instance, job.Target), maxTasks: cmp.Or(job.MaxTasks, 2),
		steps: steps, job: job, seq: seq,
		done: make(chan struct{}), subs: map[*Subscription]struct{}{}, rates: make([]rate, len(job.Items)),
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	r.cancel = cancel
	m.seq++
	r.prio = m.seq
	r.mu.Lock()
	r.emit(Event{Type: EventJobResumed, Message: fmt.Sprintf("Resumed: %d database(s) to finish, %d kept as they were.",
		resumed, len(job.Items)-resumed)}, true)
	r.save(true)
	snap := r.job.clone(true)
	r.mu.Unlock()
	m.jobs[id] = r
	m.wg.Add(1)
	go r.execute(ctx)
	return snap, nil
}

// rebuildStep calls the factory of the step's kind; a panic is its error.
func (m *Manager) rebuildStep(rb Rebuild) (step Step, err error) {
	if rb.Step.Kind == "" {
		return nil, errors.New("it recorded no kind (not a resumable step)")
	}
	f, ok := m.opts.Registry.lookup(rb.Step.Kind)
	if !ok {
		return nil, fmt.Errorf("no factory rebuilds steps of kind %q", rb.Step.Kind)
	}
	defer func() {
		if p := recover(); p != nil {
			step, err = nil, fmt.Errorf("the %s factory panicked: %v", rb.Step.Kind, p)
		}
	}()
	step, err = f(rb)
	switch {
	case err != nil:
		return nil, err
	case step != nil && step.ID() != rb.Step.ID:
		return nil, fmt.Errorf("the %s factory built step %q", rb.Step.Kind, step.ID())
	}
	return step, nil
}
