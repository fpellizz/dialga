package pipeline

import (
	"errors"
	"fmt"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

// The kinds the steps of the pipeline record in the journal (jobs.WithKind);
// the factories below rebuild a step of each from its params.
const (
	KindUpload   = "pipeline.upload"
	KindTask     = "pipeline.task"
	KindWait     = "pipeline.wait"
	KindPost     = "pipeline.post"
	KindDownload = "pipeline.download"
)

func init() {
	jobs.Register(KindUpload, rebuildUpload)
	jobs.Register(KindTask, rebuildTask)
	jobs.Register(KindWait, rebuildWait)
	jobs.Register(KindPost, rebuildPost)
	jobs.Register(KindDownload, rebuildDownload)
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
	return p.Step(env, rb.Step.Title), nil
}
