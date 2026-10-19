package pipeline

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// The kinds the steps of the pipeline record in the journal (jobs.WithKind);
// the factories below rebuild a step of each from its params.
const (
	KindUpload   = "pipeline.upload"
	KindTask     = "pipeline.task"
	KindWait     = "pipeline.wait"
	KindPost     = "pipeline.post"
	KindDownload = "pipeline.download"
	KindSQL      = "pipeline.sql"
	KindAccess   = "pipeline.access"
)

func init() {
	jobs.Register(KindUpload, rebuildUpload)
	jobs.Register(KindTask, rebuildTask)
	jobs.Register(KindWait, rebuildWait)
	jobs.Register(KindPost, rebuildPost)
	jobs.Register(KindDownload, rebuildDownload)
	jobs.Register(KindSQL, rebuildSQL)
	jobs.Register(KindAccess, rebuildAccess)
}

// envOf is the Env a resume hands to the factories (ResumeOptions.Env).
func envOf(rb jobs.Rebuild) (Env, error) {
	env, ok := rb.Env.(Env)
	if !ok {
		return Env{}, fmt.Errorf("resume environment is %T, not a pipeline.Env", rb.Env)
	}
	return env, nil
}

// rebuildUpload goes on with the multipart upload the item recorded; the
// files the step checkpointed as done are skipped.
func rebuildUpload(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p UploadParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	// The journal is a file on disk: what a step rebuilt from it will touch
	// is checked again, as for a new request.
	if err := checkBucket(env, p.Bucket); err != nil {
		return nil, err
	}
	for _, f := range p.Files {
		if err := checkLocal(f.Path); err != nil {
			return nil, err
		}
		if err := validate.S3Key(f.Key); err != nil {
			return nil, err
		}
	}
	return p.step(env, rb.Step.Title, rb.Item.Upload), nil
}

// rebuildTask queues the statement again, unless the step had already
// queued its native task before the interrupt: then there is nothing left to
// do and the wait follows that task.
func rebuildTask(rb jobs.Rebuild) (jobs.Step, error) {
	if rb.Step.TaskID != 0 {
		return nil, nil
	}
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p TaskParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if p.Statement == "" {
		return nil, errors.New("the step recorded no statement")
	}
	if err := validate.DerivedDBName(p.Database); err != nil {
		return nil, err
	}
	if err := validate.S3Key(p.Key); err != nil {
		return nil, fmt.Errorf("%w (a job recorded by an older version cannot be resumed)", err)
	}
	want, err := p.expected(rb.Step.ID, env.Target.Bucket)
	if err != nil {
		return nil, err
	}
	if p.Statement != want {
		return nil, errors.New("the statement the step recorded is not the one its parameters and the target's bucket build")
	}
	return p.Step(env, rb.Step.ID, rb.Step.Title), nil
}

// rebuildSQL rebuilds a statement of a replace. The statement is built again
// from the parameters and compared with the one recorded, as for a task; the
// step then reads the names on the instance before it acts, so an interrupt
// between the two renames of a swap resumes where it stopped.
func rebuildSQL(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p SQLParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if p.Op == ops.StepCancel {
		if p.TaskID < 1 {
			return nil, fmt.Errorf("the cancel step recorded task %d", p.TaskID)
		}
	} else if err := validate.DerivedDBName(p.Database); err != nil {
		return nil, err
	}
	if p.Op == ops.StepRename {
		if err := validate.DerivedDBName(p.To); err != nil {
			return nil, err
		}
		if p.Fallback != "" {
			if err := validate.DerivedDBName(p.Fallback); err != nil {
				return nil, err
			}
		}
	} else if p.To != "" || p.Fallback != "" {
		return nil, fmt.Errorf("the %s step recorded a new name", p.Op)
	}
	want, err := p.expected()
	if err != nil {
		return nil, err
	}
	if p.Statement != want {
		return nil, errors.New("the statement the step recorded is not the one its parameters build")
	}
	return p.Step(env, rb.Step.ID, rb.Step.Title), nil
}

// rebuildAccess rebuilds a step of an access operation: the names are
// checked again and the masked statement must be the one its parameters
// build. A create-login has no password after a resume: it is a no-op when
// the login exists by then, and fails saying so otherwise.
func rebuildAccess(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p AccessParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if err := p.Validate(env.Target); err != nil {
		return nil, err
	}
	want, err := p.AccessStep.Statement(mssql.MaskedPassword)
	if err != nil {
		return nil, err
	}
	if p.Statement != want {
		return nil, errors.New("the statement the step recorded is not the one its parameters build")
	}
	return p.Step(env, rb.Step.ID, rb.Step.Title), nil
}

// rebuildWait follows the last native task of the item, which it reads when
// it runs: a restore rebuilt before it queues a new one.
func rebuildWait(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p WaitParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	return p.Step(env), nil
}

func rebuildPost(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p PostParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if err := validate.DBName(p.Database); err != nil {
		return nil, err
	}
	return p.Step(env), nil
}

// rebuildDownload downloads the backup again from the start (into a
// temporary file, so a partial one is never left at the destination).
func rebuildDownload(rb jobs.Rebuild) (jobs.Step, error) {
	env, err := envOf(rb)
	if err != nil {
		return nil, err
	}
	var p DownloadParams
	if err := rb.Params(&p); err != nil {
		return nil, err
	}
	if err := checkBucket(env, p.Bucket); err != nil {
		return nil, err
	}
	if err := validate.DBName(p.Database); err != nil {
		return nil, err
	}
	if err := validate.S3Key(p.Key); err != nil {
		return nil, err
	}
	if err := checkLocal(p.Path); err != nil {
		return nil, err
	}
	return p.Step(env, rb.Step.Title), nil
}

// checkBucket refuses a step recorded for another bucket than the target's
// now.
func checkBucket(env Env, bucket string) error {
	if bucket != env.Target.Bucket {
		return fmt.Errorf("the step recorded bucket %s, the target uses %s now", bucket, env.Target.Bucket)
	}
	return nil
}

func checkLocal(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("the step recorded %q, which is not an absolute path", path)
	}
	return nil
}
