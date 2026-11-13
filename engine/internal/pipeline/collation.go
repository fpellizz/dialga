package pipeline

import (
	"context"
	"errors"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// CollationParams are one step of a collation operation (set-collation,
// convert-columns): its parameters and the statement the plan showed.
type CollationParams struct {
	ops.CollationStep
	Statement string `json:"statement"`
}

// Step runs the step on a session of its own.
func (p CollationParams) Step(env Env, id, title string) jobs.Step {
	return jobs.WithKind(jobs.NewStep(id, title, jobs.NoResource, func(ctx context.Context, _ jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		return sess.RunCollationStep(ctx, p.CollationStep)
	}), KindCollation, p)
}

// rebuildCollation rebuilds a step of a collation operation: the names are
// checked again and the statement must be the one its parameters build. The
// step reads the database before it acts, so a step the instance already did
// is a no-op.
func rebuildCollation(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p CollationParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if err := p.Validate(env.Target); err != nil {
		return nil, err
	}
	want, err := p.CollationStep.Statement()
	if err != nil {
		return nil, err
	}
	if p.Statement != want {
		return nil, errors.New("the statement the step recorded is not the one its parameters build")
	}
	return p.Step(env, rb.Step.ID, rb.Step.Title), nil
}
