package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() { extraRoutes = append(extraRoutes, (*Server).opsRoutes) }

// opsRoutes are the native operations: a plan to preview, the same plan to
// execute, and the post-restore report of a database.
func (s *Server) opsRoutes() []route {
	return []route{
		{http.MethodPost, "/api/v1/targets/{name}/plans/{operation}", s.previewPlan},
		{http.MethodPost, "/api/v1/targets/{name}/plans/{operation}/execute", s.executePlan},
		{http.MethodGet, "/api/v1/targets/{name}/databases/{db}/report", s.databaseReport},
	}
}

// PlanRequest is the body of POST .../plans/{operation}: the parameters of
// the operation (database for restore, export, finish, drop; key for
// restore and optionally export; taskId for cancel).
type PlanRequest struct {
	Database     string `json:"database,omitempty"`
	Key          string `json:"key,omitempty"`
	Differential bool   `json:"differential,omitempty"`
	NoRecovery   bool   `json:"noRecovery,omitempty"`
	TaskID       int    `json:"taskId,omitempty"`
	// Replace restores onto a database that exists: "auto", "swap" or "drop"
	// ("" refuses it). BackupFirst, for drop, backs the current database up to
	// S3 first (default true); Stamp dates the names of the replace.
	Replace     string `json:"replace,omitempty"`
	BackupFirst *bool  `json:"backupFirst,omitempty"`
	Stamp       string `json:"stamp,omitempty"`
}

// ExecuteRequest is the body of POST .../plans/{operation}/execute: the
// parameters of the preview, the confirmation and how to run it.
type ExecuteRequest struct {
	Database     string `json:"database,omitempty"`
	Key          string `json:"key,omitempty"`
	Differential bool   `json:"differential,omitempty"`
	NoRecovery   bool   `json:"noRecovery,omitempty"`
	TaskID       int    `json:"taskId,omitempty"`
	// Replace restores onto a database that exists: "auto", "swap" or "drop"
	// ("" refuses it). BackupFirst, for drop, backs the current database up to
	// S3 first (default true); Stamp dates the names of the replace.
	Replace     string `json:"replace,omitempty"`
	BackupFirst *bool  `json:"backupFirst,omitempty"`
	Stamp       string `json:"stamp,omitempty"`
	// Confirm is the text the preview asked to retype (confirmation).
	Confirm string `json:"confirm,omitempty"`
	// Digest, when set, must be the digest of the preview: the plan has not
	// changed since the user saw it.
	Digest string `json:"digest,omitempty"`
	// NoWait returns once the native task is started instead of following it.
	NoWait bool `json:"noWait,omitempty"`
}

func (r ExecuteRequest) plan() PlanRequest {
	return PlanRequest{Database: r.Database, Key: r.Key, Differential: r.Differential, NoRecovery: r.NoRecovery, TaskID: r.TaskID,
		Replace: r.Replace, BackupFirst: r.BackupFirst, Stamp: r.Stamp}
}

func (r PlanRequest) params() ops.Params {
	return ops.Params{Database: r.Database, Key: r.Key, TaskID: r.TaskID,
		Restore: mssql.RestoreOptions{Differential: r.Differential, NoRecovery: r.NoRecovery},
		Replace: ops.ReplaceOptions{Mode: r.Replace, BackupFirst: r.BackupFirst, Stamp: r.Stamp}}
}

// PlanPreview is the answer of POST .../plans/{operation}. Nothing is changed.
type PlanPreview struct {
	Plan core.Plan `json:"plan"`
	// Checks are the read-only pre-flight checks; one failed makes Ready false.
	Checks []check.Event `json:"checks"`
	Ready  bool          `json:"ready"`
	// Confirmation is the text the user retypes to execute, if any.
	Confirmation string `json:"confirmation,omitempty"`
	Digest       string `json:"digest"`
	// Preview is the dry-run text, as the CLI prints it.
	Preview string `json:"preview"`
	// Request is the body with the defaults resolved (the export key): the
	// body of the execute call, so the plan that runs is the one previewed.
	Request PlanRequest `json:"request"`
	// Replace says how a restore over an existing database goes: the strategy
	// auto resolved to, the names it uses, what it keeps.
	Replace *ops.Replacement `json:"replace,omitempty"`
}

// PlanDone is the data of the final "done" event of an execution.
type PlanDone struct {
	// OK: every action ran and the task, when followed, ended in SUCCESS.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Task is the native task the operation started, last state read.
	Task *mssql.Task `json:"task,omitempty"`
	// Result is what a replace kept: the old database or the safety backup.
	Result string `json:"result,omitempty"`
	// Replace is the structured outcome of a replace: strategy, the old
	// database a swap kept, the safety backup a drop took (as far as it got).
	Replace *core.ReplaceOutcome `json:"replace,omitempty"`
}

// readBody decodes a small JSON body, rejecting unknown fields.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return readBodyMax(w, r, v, 1<<16)
}

// readBodyMax is readBody with its own limit in bytes.
func readBodyMax(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON body: one object expected", http.StatusBadRequest)
		return false
	}
	return true
}

// opTarget resolves the target and, when check is set, validates the
// parameters before anything is opened. It writes the error answer itself.
func (s *Server) opTarget(w http.ResponseWriter, r *http.Request, check func(config.Target) error) (string, config.Target, bool) {
	cfg, err := config.Load(s.opts.ConfigPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return "", config.Target{}, false
	}
	name, t, err := cfg.Target(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return "", config.Target{}, false
	}
	if err := check(t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", config.Target{}, false
	}
	return name, t, true
}

// openOps opens the session of a resolved target, answering 422 without a
// password and 502 when AWS or SQL Server do not answer.
func (s *Server) openOps(w http.ResponseWriter, r *http.Request, name string, t config.Target) (*ops.Session, func(), bool) {
	password, ok := s.sqlPassword(r.Context(), w, name, t)
	if !ok {
		return nil, nil, false
	}
	sess, closeOps, err := s.opts.OpenOps(r.Context(), name, t, password)
	if err != nil {
		if r.Context().Err() == nil {
			s.opts.Log.Warn("ops: session failed", "target", name, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return nil, nil, false
	}
	return sess, closeOps, true
}

// buildPlan runs the pre-flight checks of the operation in the path.
func (s *Server) buildPlan(w http.ResponseWriter, r *http.Request, req PlanRequest) (*ops.Prepared, func(), bool) {
	op := r.PathValue("operation")
	name, t, ok := s.opTarget(w, r, func(t config.Target) error { return ops.Validate(op, req.params(), t) })
	if !ok {
		return nil, nil, false
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return nil, nil, false
	}
	prep, err := sess.Build(r.Context(), op, req.params())
	if err != nil {
		closeOps()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return nil, nil, false
	}
	return prep, closeOps, true
}

func knownOperation(w http.ResponseWriter, r *http.Request) bool {
	if op := r.PathValue("operation"); !slices.Contains(ops.Operations, op) {
		http.Error(w, fmt.Sprintf("unknown operation %q (known: %s)", op, strings.Join(ops.Operations, ", ")), http.StatusNotFound)
		return false
	}
	return true
}

// previewPlan answers the plan of an operation after its pre-flight checks.
// It only reads: a failed check is in the answer (ready false), not an error.
func (s *Server) previewPlan(w http.ResponseWriter, r *http.Request) {
	var req PlanRequest
	if !knownOperation(w, r) || !readBody(w, r, &req) {
		return
	}
	prep, closeOps, ok := s.buildPlan(w, r, req)
	if !ok {
		return
	}
	defer closeOps()
	var text strings.Builder
	_ = prep.Plan.Preview(&text)
	resolved := req
	resolved.Key = prep.Params.Key
	if prep.Replace != nil {
		resolved.Stamp = prep.Replace.Stamp
	}
	writeJSON(w, PlanPreview{
		Plan: prep.Plan, Checks: prep.Checks, Ready: prep.Ready(), Confirmation: prep.Plan.Confirmation(),
		Digest: prep.Digest(), Preview: text.String(), Request: resolved, Replace: prep.Replace,
	})
}

// executePlan builds the plan again (the instance may have changed since the
// preview), checks it, the digest and the confirmation, then streams the
// execution: "notice" for the pre-flight warnings, "step" for every action
// (and the wait), "task" for every change of the followed task, one "done".
func (s *Server) executePlan(w http.ResponseWriter, r *http.Request) {
	var req ExecuteRequest
	if !knownOperation(w, r) || !readBody(w, r, &req) {
		return
	}
	prep, closeOps, ok := s.buildPlan(w, r, req.plan())
	if !ok {
		return
	}
	defer closeOps()
	if !prep.Ready() {
		var failed []string
		for _, c := range prep.Checks {
			if c.Status == check.Failed {
				failed = append(failed, c.Label+": "+c.Detail)
			}
		}
		http.Error(w, "pre-flight failed, nothing was changed: "+strings.Join(failed, "; "), http.StatusPreconditionFailed)
		return
	}
	if req.Digest != "" && req.Digest != prep.Digest() {
		http.Error(w, "the plan changed since the preview: preview it again", http.StatusPreconditionFailed)
		return
	}
	if want := prep.Plan.Confirmation(); want != "" && req.Confirm != want {
		http.Error(w, fmt.Sprintf("%v: retype %q", core.ErrNotConfirmed, want), http.StatusUnprocessableEntity)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	send := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	for _, c := range prep.Checks {
		if c.Status == check.Warning {
			send("notice", Notice{Message: c.Label + ": " + c.Detail})
		}
	}
	op, name := prep.Plan.Operation, prep.Plan.Target
	s.opts.Log.Info("plan started", "target", name, "operation", op, "digest", prep.Digest())
	task, err := prep.Execute(r.Context(), ops.ExecOptions{
		Confirm: req.Confirm, Wait: !req.NoWait, Interval: s.opts.TaskInterval,
		OnStep: func(e core.Event) { send("step", e) },
		OnTask: func(t mssql.Task) { send("task", t) },
		OnRetry: func(err error) {
			s.opts.Log.Warn("plan: reading the task failed, retrying", "target", name, "err", err)
		},
	})
	done := PlanDone{OK: err == nil, Task: task, Replace: prep.Outcome()}
	if err != nil {
		done.Error = err.Error()
	} else if task != nil && !req.NoWait && task.Lifecycle != mssql.Success {
		done.OK = false
	} else {
		done.Result = prep.Result()
	}
	send("done", done)
	s.opts.Log.Info("plan finished", "target", name, "operation", op, "ok", done.OK)
}

// databaseReport answers the post-restore report of a database (read-only).
func (s *Server) databaseReport(w http.ResponseWriter, r *http.Request) {
	db := r.PathValue("db")
	name, t, ok := s.opTarget(w, r, func(config.Target) error { return validate.DBName(db) })
	if !ok {
		return
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return
	}
	defer closeOps()
	report, err := sess.Post(r.Context(), db)
	switch {
	case errors.Is(err, mssql.ErrNoDatabase):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, report)
	}
}
