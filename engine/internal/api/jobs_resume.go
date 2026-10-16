package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
)

// ResumeRequest is the body of POST /api/v1/jobs/{id}/resume (optional).
type ResumeRequest struct {
	// Confirm is the target name, retyped: needed when the target is protected.
	Confirm string `json:"confirm,omitempty"`
}

// InstanceKey identifies an RDS instance for the scheduler's task slots,
// across targets: the same key the pipeline gives a new job.
func InstanceKey(t config.Target) string {
	return t.AccountID + "/" + t.Region + "/" + t.Instance
}

// resumeJob runs an interrupted job again (same id), from what the journal
// kept, on its target as configured now. The answer comes when the job is
// queued again (202): follow it with GET /api/v1/jobs/{id}/events.
func (s *Server) resumeJob(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	var req ResumeRequest
	body := http.MaxBytesReader(w, r.Body, 4<<10)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	job, err := s.opts.Jobs.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if job.State != jobs.Interrupted {
		http.Error(w, fmt.Sprintf("%v: job %s is %s", jobs.ErrNotInterrupted, id, job.State), http.StatusConflict)
		return
	}
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	name, t, err := cfg.Target(job.Target)
	if err != nil {
		http.Error(w, fmt.Sprintf("the target %s of this job is not in the configuration any more", job.Target),
			http.StatusUnprocessableEntity)
		return
	}
	if t.Instance != job.Instance {
		http.Error(w, fmt.Sprintf("target %s now points at instance %s, the job ran on %s: not resumed", name, t.Instance, job.Instance),
			http.StatusUnprocessableEntity)
		return
	}
	if s.opts.JobEnv == nil {
		http.Error(w, "resuming needs the import/export pipeline, which this engine does not have", http.StatusNotImplemented)
		return
	}
	if t.Protected && req.Confirm != name {
		http.Error(w, fmt.Sprintf("not confirmed: this %s asks to retype %q in confirm", job.Kind, name), http.StatusUnprocessableEntity)
		return
	}
	password, ok := s.sqlPassword(r.Context(), w, name, t)
	if !ok {
		return
	}
	env, err := s.opts.JobEnv(r.Context(), name, t, password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	snap, err := s.opts.Jobs.Resume(id, jobs.ResumeOptions{Env: env, InstanceKey: InstanceKey(t)})
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, jobs.ErrNotInterrupted):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, jobs.ErrNotResumable):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.opts.Log.Info("job resumed", "job", id, "target", name)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(snap)
}
