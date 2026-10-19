package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// OperationItem names the one item of the job of a single operation: its
// database, or the native task of a cancel.
func OperationItem(p ops.Params) string {
	if p.Database == "" && p.TaskID > 0 {
		return fmt.Sprintf("task %d", p.TaskID)
	}
	return p.Database
}

// OperationSpec turns a prepared operation (ready, confirmed by the caller)
// into a job of kind jobs.Operation: the actions of its plan are the steps,
// with the same titles and statements, and the native task a restore or an
// export starts is followed by a wait step unless noWait. The steps record
// what a resume needs, so an interrupted operation resumes like any job.
//
// env.OnTask, if set, gets the followed task in full at every change.
func OperationSpec(env Env, prep *ops.Prepared, noWait bool) (jobs.Spec, error) {
	plan := prep.Plan
	if !prep.Ready() {
		return jobs.Spec{}, ErrNotReady
	}
	if prep.Replace != nil && noWait {
		return jobs.Spec{}, ops.ErrReplaceNoWait
	}
	p := prep.Params
	var steps []jobs.Step
	switch plan.Operation {
	case ops.OpRestore:
		if prep.Replace != nil {
			for _, st := range prep.Replace.Steps {
				steps = append(steps, replaceStep(env, prep.Replace, st))
			}
			break
		}
		a := plan.Actions[0]
		steps = append(steps, TaskParams{Statement: a.Statement, Database: p.Database, Key: p.Key,
			Differential: p.Restore.Differential, NoRecovery: p.Restore.NoRecovery}.Step(env, a.ID, a.Title))
		if !noWait {
			steps = append(steps, WaitParams{What: "restore"}.Step(env))
		}
	case ops.OpExport:
		a := plan.Actions[0]
		steps = append(steps, TaskParams{Statement: a.Statement, Database: p.Database, Key: p.Key}.Step(env, a.ID, a.Title))
		if !noWait {
			steps = append(steps, WaitParams{What: "backup"}.Step(env))
		}
	case ops.OpFinish:
		a := plan.Actions[0]
		steps = append(steps, SQLParams{Op: ops.StepFinish, Statement: a.Statement, Database: p.Database,
			Result: p.Database + " brought online"}.Step(env, a.ID, a.Title))
	case ops.OpCancel:
		a := plan.Actions[0]
		steps = append(steps, SQLParams{Op: ops.StepCancel, Statement: a.Statement, TaskID: p.TaskID,
			Result: fmt.Sprintf("cancel of task %d requested", p.TaskID)}.Step(env, a.ID, a.Title))
	case ops.OpDrop:
		a := plan.Actions[0]
		steps = append(steps, SQLParams{Op: ops.StepDrop, Statement: a.Statement, Database: p.Database,
			Result: p.Database + " dropped"}.Step(env, a.ID, a.Title))
	case ops.OpGrant, ops.OpRevoke, ops.OpFixOrphan:
		if prep.NeedsLoginPassword() {
			if err := ops.CheckLoginPassword(env.LoginPassword); err != nil {
				return jobs.Spec{}, err
			}
		}
		// The ids of the steps of a job are unique; the plan repeats add-role
		// and drop-role, so the repeats get a "#n" (OperationEvents drops it).
		seen := map[string]int{}
		for i, st := range prep.AccessSteps() {
			a := plan.Actions[i]
			id := a.ID
			if seen[id]++; seen[id] > 1 {
				id = fmt.Sprintf("%s#%d", id, seen[id])
			}
			steps = append(steps, AccessParams{AccessStep: st, Statement: a.Statement}.Step(env, id, a.Title))
		}
	default:
		return jobs.Spec{}, fmt.Errorf("operation %q cannot run as a job", plan.Operation)
	}
	t := env.Target
	return jobs.Spec{
		Kind: jobs.Operation, Operation: plan.Operation, Target: env.Name, Instance: t.Instance,
		InstanceKey: strings.Join([]string{t.AccountID, t.Region, t.Instance}, "/"), MaxTasks: t.MaxTasks,
		Items: []jobs.ItemSpec{{Name: OperationItem(p), Steps: steps}},
	}, nil
}

// repeatedID is the "#n" the steps of an access plan get when an id repeats.
var repeatedID = regexp.MustCompile(`#[0-9]+$`)

// OperationEvents turns the events of an operation job into what the plan
// execution reported: a core.Event per step that starts, succeeds, fails or
// is skipped. titles maps a step id to its title (jobs.StepInfo). ok is false
// for the events that are not about a step.
func OperationEvents(titles map[string]string) func(jobs.Event) (core.Event, bool) {
	return func(ev jobs.Event) (core.Event, bool) {
		out := core.Event{Step: repeatedID.ReplaceAllString(ev.Step, ""), Label: titles[ev.Step]}
		switch ev.Type {
		case jobs.EventStepStarted:
			out.Status = core.Running
		case jobs.EventStepSucceeded:
			out.Status, out.Detail = core.OK, ev.Message
		case jobs.EventStepFailed:
			out.Status, out.Detail = core.Failed, ev.Message
		case jobs.EventStepSkipped:
			out.Status = core.Skipped
		case jobs.EventStepCancelled, jobs.EventStepInterrupted:
			out.Status, out.Detail = core.Failed, ev.Message
			if out.Detail == "" {
				out.Detail = "cancelled"
			}
		default:
			return core.Event{}, false
		}
		return out, true
	}
}

// OperationTask is the native task of an operation job as the last
// task.status event or the item says it, with the database of the item.
func OperationTask(item jobs.Item) *mssql.Task {
	if item.Task == nil {
		return nil
	}
	t := item.Task
	return &mssql.Task{ID: t.ID, Type: t.Type, Database: item.Name, Percent: t.Percent, Lifecycle: t.Lifecycle}
}
