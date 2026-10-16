package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

func init() { extraRoutes = append(extraRoutes, (*Server).jobStartRoutes) }

func (s *Server) jobStartRoutes() []route {
	return []route{{http.MethodPost, "/api/v1/jobs", s.startJob}}
}

// JobRequest is the body of POST /api/v1/jobs: an import or an export of
// databases on one target.
type JobRequest struct {
	Kind   string           `json:"kind"`
	Target string           `json:"target"`
	Items  []JobRequestItem `json:"items"`
	// Prefix replaces the import prefix of the target for the uploads (""
	// is the bucket root); an item key wins.
	Prefix *string `json:"prefix,omitempty"`
	// SkipFailing leaves out the databases whose pre-flight failed.
	SkipFailing bool `json:"skipFailing,omitempty"`
	// DryRun answers the plan and starts nothing.
	DryRun bool `json:"dryRun,omitempty"`
	// Confirm is the text the plan asks to retype (confirmation).
	Confirm string `json:"confirm,omitempty"`
	// Digest, when set, must be the digest of the preview.
	Digest string `json:"digest,omitempty"`
	// Stamp dates the names a replace creates; send back the one of the
	// preview (plan.stamp) to start the names it showed.
	Stamp string `json:"stamp,omitempty"`
}

// JobRequestItem is one database of a JobRequest: file or files (import:
// one .bak, or the stripes of one backup) or destination (export, optional),
// absolute paths on the machine of the engine.
type JobRequestItem struct {
	Database     string   `json:"database"`
	File         string   `json:"file,omitempty"`
	Files        []string `json:"files,omitempty"`
	Destination  string   `json:"destination,omitempty"`
	Key          string   `json:"key,omitempty"`
	Differential bool     `json:"differential,omitempty"`
	NoRecovery   bool     `json:"noRecovery,omitempty"`
	Overwrite    bool     `json:"overwrite,omitempty"`
	// Import - restore onto a database that exists: "auto", "swap" or "drop"
	// ("" refuses it); BackupFirst, for drop, backs it up to S3 first (default true).
	Replace     string `json:"replace,omitempty"`
	BackupFirst *bool  `json:"backupFirst,omitempty"`
}

// JobRefused is the answer 412 of POST /api/v1/jobs: why, and the plan
// with its checks.
type JobRefused struct {
	Reason string        `json:"reason"`
	Plan   pipeline.Plan `json:"plan"`
}

func (r JobRequest) pipeline() (pipeline.Request, error) {
	req := pipeline.Request{Kind: jobs.Kind(r.Kind), Prefix: r.Prefix, SkipFailing: r.SkipFailing, Stamp: r.Stamp}
	for i, it := range r.Items {
		where := fmt.Sprintf("item %d (%s)", i+1, it.Database)
		switch req.Kind {
		case jobs.Import:
			files := it.Files
			switch {
			case it.Destination != "":
				return req, fmt.Errorf("%s: destination is for export items", where)
			case it.File != "" && len(it.Files) > 0:
				return req, fmt.Errorf("%s: give file or files, not both", where)
			case it.File != "":
				files = []string{it.File}
			}
			req.Import = append(req.Import, pipeline.ImportItem{Files: files, Database: it.Database, Key: it.Key,
				Differential: it.Differential, NoRecovery: it.NoRecovery, Overwrite: it.Overwrite,
				Replace: it.Replace, BackupFirst: it.BackupFirst})
		case jobs.Export:
			if it.File != "" || len(it.Files) > 0 || it.NoRecovery || it.Differential || it.Replace != "" || it.BackupFirst != nil {
				return req, fmt.Errorf("%s: file, files, noRecovery, differential, replace and backupFirst are for import items", where)
			}
			req.Export = append(req.Export, pipeline.ExportItem{Database: it.Database, Key: it.Key,
				Destination: it.Destination, Overwrite: it.Overwrite})
		}
	}
	return req, nil
}

// startJob runs the pre-flight checks of every database and, unless dryRun,
// starts the job: 201 with its first snapshot, then the client follows
// /api/v1/jobs/{id}/events. The job runs in the engine, not in the request:
// closing the connection does not stop it (POST .../cancel does).
func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var body JobRequest
	// A batch of MaxItems databases with stripes outgrows the default limit.
	if !readBodyMax(w, r, &body, 1<<18) {
		return
	}
	req, err := body.pipeline()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	name, t, err := cfg.Target(body.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := req.Validate(t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	password, ok := s.sqlPassword(r.Context(), w, name, t)
	if !ok {
		return
	}
	env, err := s.pipelineEnv(r.Context(), name, t, password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	plan, err := pipeline.Build(r.Context(), env, req)
	switch {
	case errors.Is(err, pipeline.ErrInvalidRequest), errors.Is(err, transfer.ErrInvalidRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		if r.Context().Err() == nil {
			s.opts.Log.Warn("job: pre-flight failed", "target", name, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	case body.DryRun:
		writeJSON(w, plan)
		return
	case !plan.Ready:
		refuse(w, "pre-flight failed, nothing was started: "+strings.Join(plan.Failed(), "; "), plan)
		return
	case body.Digest != "" && body.Digest != plan.Digest:
		refuse(w, "the plan changed since the preview: preview it again", plan)
		return
	case plan.Confirmation != "" && body.Confirm != plan.Confirmation:
		http.Error(w, fmt.Sprintf("not confirmed: this %s asks to retype %q in confirm", plan.Kind, plan.Confirmation),
			http.StatusUnprocessableEntity)
		return
	}
	job, err := plan.Start(s.opts.Jobs, body.Confirm)
	switch {
	case errors.Is(err, jobs.ErrClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.opts.Log.Info("job started", "job", job.ID, "kind", job.Kind, "target", name, "databases", len(job.Items))
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(job)
}

// pipelineEnv is how the jobs of a target reach it: the S3 client and the
// SQL sessions of the steps, with the password from the engine environment.
func (s *Server) pipelineEnv(ctx context.Context, name string, t config.Target, password string) (pipeline.Env, error) {
	openS3 := s.opts.OpenS3
	if openS3 == nil {
		openS3 = transfer.Open
	}
	client, err := openS3(ctx, t)
	if err != nil {
		return pipeline.Env{}, err
	}
	return pipeline.Env{
		Name: name, Target: t, S3: client, TaskInterval: s.opts.TaskInterval,
		// The steps open their sessions with the context of the job, not
		// of the request that started it: the job outlives it.
		OpenOps: func(ctx context.Context) (*ops.Session, func(), error) { return s.opts.OpenOps(ctx, name, t, password) },
	}, nil
}

func refuse(w http.ResponseWriter, reason string, plan *pipeline.Plan) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPreconditionFailed)
	_ = json.NewEncoder(w).Encode(JobRefused{Reason: reason, Plan: *plan})
}
