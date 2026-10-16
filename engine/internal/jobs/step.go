package jobs

import (
	"context"
	"errors"

	"github.com/decisyon/dialga/engine/internal/core"
)

// Resource is what a step uses while it runs: the scheduler limits how many
// steps of each resource run at once.
type Resource string

const (
	// NoResource is local work (checks, post-processing): no limit.
	NoResource Resource = ""
	// ResourceTransfer is an S3 upload or download. Global limit, all jobs
	// together: Options.Limits[ResourceTransfer], default 3.
	ResourceTransfer Resource = "transfer"
	// ResourceRDSTask is a native backup/restore task. Limit per instance,
	// all jobs together: Spec.MaxTasks (RDS runs at most 2).
	ResourceRDSTask Resource = "rds-task"
)

// Step is one unit of work of an item: "upload", "restore", "wait",
// "post"... Steps of an item run in order; a slot of the step's resource is
// taken before the first of consecutive steps with the same resource and
// given back after the last one, so "restore" (submit the native task) and
// "wait" (follow it) hold one RDS slot from the submit to the end of the
// task.
//
// Run must return once ctx ends. context.Cause(ctx) then says why:
//   - ErrCancelled: the user cancelled the job; abort what can be aborted
//     (the multipart upload), but never a native task already running;
//   - ErrInterrupted: the engine is stopping; keep what a resume needs (the
//     uploaded parts, reported through Reporter.Upload).
//
// detail is the result line of the step (shown under it in the GUI).
type Step interface {
	ID() string
	Title() string
	Resource() Resource
	Run(ctx context.Context, r Reporter) (detail string, err error)
}

// Reporter is how a running step reports to its job. Calls after Run
// returned are ignored.
type Reporter interface {
	// Item is the name of the item (the database) the step works on.
	Item() string
	// Transfer reports the bytes moved so far out of total
	// (upload.progress; the speed and the ETA are computed here).
	Transfer(dir Direction, done, total int64)
	// Task reports the native RDS task the step started or follows
	// (task.status): its id is recorded on the item.
	Task(TaskStatus)
	// Upload records the multipart upload state a resume needs.
	Upload(UploadState)
	// Log adds a log event to the job.
	Log(level Level, msg string)
	// Result sets the result line of the item.
	Result(string)
}

// NewStep makes a Step out of a function.
func NewStep(id, title string, res Resource, run func(context.Context, Reporter) (string, error)) Step {
	return funcStep{id: id, title: title, res: res, run: run}
}

// FromAction runs an action of a core.Plan as a step. The plan's
// confirmation must be checked before the job starts: the step only runs.
func FromAction(a core.Action, res Resource) Step {
	return NewStep(a.ID, a.Title, res, func(ctx context.Context, _ Reporter) (string, error) {
		if a.Run == nil {
			return "", errors.New("this action has no runner")
		}
		return a.Run(ctx)
	})
}

type funcStep struct {
	id, title string
	res       Resource
	run       func(context.Context, Reporter) (string, error)
}

func (s funcStep) ID() string         { return s.id }
func (s funcStep) Title() string      { return s.title }
func (s funcStep) Resource() Resource { return s.res }

func (s funcStep) Run(ctx context.Context, r Reporter) (string, error) { return s.run(ctx, r) }
