package jobs

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Rebuild is what a Factory gets to rebuild one step of an interrupted job.
type Rebuild struct {
	// Job is the interrupted job (target, instance, every item).
	Job Job
	// Item is the database of the step: Upload holds the multipart upload
	// (upload id, parts done), Task and TaskIDs its native tasks.
	Item Item
	// Step is the step as the journal kept it: Kind, Params, Status
	// (pending, or interrupted if it had started) and TaskID.
	Step StepInfo
	// Env is ResumeOptions.Env: how the steps reach the target (the
	// pipeline's environment), passed through as is.
	Env any
}

// Params decodes the params the step recorded into v.
func (rb Rebuild) Params(v any) error {
	if len(rb.Step.Params) == 0 {
		return fmt.Errorf("step %s of %s recorded no params", rb.Step.ID, rb.Item.Name)
	}
	return json.Unmarshal(rb.Step.Params, v)
}

// LastTaskID is the native task to follow: the step's own, else the last
// one the item started (0 if none).
func (rb Rebuild) LastTaskID() int {
	if rb.Step.TaskID != 0 {
		return rb.Step.TaskID
	}
	if n := len(rb.Item.TaskIDs); n > 0 {
		return rb.Item.TaskIDs[n-1]
	}
	return 0
}

// Factory rebuilds a step from the journal. It returns a nil Step when the
// step has nothing left to do (a restore whose native task was already
// queued: the wait follows it): the step is then marked succeeded. Factories
// only build: no I/O, the step does it when it runs.
type Factory func(Rebuild) (Step, error)

// Registry maps step kinds to their factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty registry (tests; the engine uses DefaultRegistry).
func NewRegistry() *Registry { return &Registry{factories: map[string]Factory{}} }

// DefaultRegistry is where the step packages register from init().
var DefaultRegistry = NewRegistry()

// Register adds the factory of kind to DefaultRegistry.
//
//	func init() { jobs.Register("upload", rebuildUpload) }
func Register(kind string, f Factory) { DefaultRegistry.Register(kind, f) }

// Register adds the factory of kind; a kind registered twice is a bug.
func (r *Registry) Register(kind string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "" || f == nil {
		panic("jobs: Register with an empty kind or a nil factory")
	}
	if _, dup := r.factories[kind]; dup {
		panic("jobs: step kind registered twice: " + kind)
	}
	r.factories[kind] = f
}

func (r *Registry) lookup(kind string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[kind]
	return f, ok
}
