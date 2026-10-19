package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
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
		{http.MethodGet, "/api/v1/targets/{name}/databases/{db}/access", s.databaseAccess},
		{http.MethodGet, "/api/v1/targets/{name}/logins", s.targetLogins},
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
	// Database access (grant, revoke, fix-orphan): Login is the SQL login,
	// User the orphan user of fix-orphan, Level read, readwrite or owner,
	// Backup adds the right to back the database up, CreateLogin creates the
	// login, Remove is what a revoke takes away (level, backup, user).
	Login       string `json:"login,omitempty"`
	User        string `json:"user,omitempty"`
	Level       string `json:"level,omitempty"`
	Backup      bool   `json:"backup,omitempty"`
	CreateLogin bool   `json:"createLogin,omitempty"`
	Remove      string `json:"remove,omitempty"`
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
	// Database access (grant, revoke, fix-orphan): Login is the SQL login,
	// User the orphan user of fix-orphan, Level read, readwrite or owner,
	// Backup adds the right to back the database up, CreateLogin creates the
	// login, Remove is what a revoke takes away (level, backup, user).
	Login       string `json:"login,omitempty"`
	User        string `json:"user,omitempty"`
	Level       string `json:"level,omitempty"`
	Backup      bool   `json:"backup,omitempty"`
	CreateLogin bool   `json:"createLogin,omitempty"`
	Remove      string `json:"remove,omitempty"`
	// LoginPassword is the password of the login a grant creates: for the
	// execution only, never in a preview, plan, event, journal or log.
	LoginPassword string `json:"loginPassword,omitempty"`
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
		Replace: r.Replace, BackupFirst: r.BackupFirst, Stamp: r.Stamp,
		Login: r.Login, User: r.User, Level: r.Level, Backup: r.Backup, CreateLogin: r.CreateLogin, Remove: r.Remove}
}

func (r PlanRequest) params() ops.Params {
	return ops.Params{Database: r.Database, Key: r.Key, TaskID: r.TaskID,
		Restore: mssql.RestoreOptions{Differential: r.Differential, NoRecovery: r.NoRecovery},
		Replace: ops.ReplaceOptions{Mode: r.Replace, BackupFirst: r.BackupFirst, Stamp: r.Stamp},
		Login:   r.Login, User: r.User, Level: r.Level, Backup: r.Backup, CreateLogin: r.CreateLogin, Remove: r.Remove}
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

// JobRef is the data of the first event of an execution, "job".
type JobRef struct {
	ID string `json:"id"`
}

// PlanDone is the data of the final "done" event of an execution.
type PlanDone struct {
	// OK: every action ran and the task, when followed, ended in SUCCESS.
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Task is the native task the operation started, last state read.
	Task *mssql.Task `json:"task,omitempty"`
	// JobID is the job the operation ran as (Activity).
	JobID string `json:"jobId,omitempty"`
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
	sess, closeOps, _, ok := s.openOpsWithPassword(w, r, name, t)
	return sess, closeOps, ok
}

// openOpsWithPassword is openOps that also gives the password it used: a job
// opens its own sessions with it.
func (s *Server) openOpsWithPassword(w http.ResponseWriter, r *http.Request, name string, t config.Target) (*ops.Session, func(), string, bool) {
	password, ok := s.sqlPassword(r.Context(), w, name, t)
	if !ok {
		return nil, nil, "", false
	}
	sess, closeOps, err := s.opts.OpenOps(r.Context(), name, t, password)
	if err != nil {
		if r.Context().Err() == nil {
			s.opts.Log.Warn("ops: session failed", "target", name, "err", err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return nil, nil, "", false
	}
	return sess, closeOps, password, true
}

// builtPlan is a plan after its pre-flight checks, with what runs it.
type builtPlan struct {
	prep     *ops.Prepared
	closeOps func()
	name     string
	target   config.Target
	password string
}

// buildPlan runs the pre-flight checks of the operation in the path. ref,
// when set, learns the target as soon as it is resolved.
func (s *Server) buildPlan(w http.ResponseWriter, r *http.Request, req PlanRequest, ref *refused) (builtPlan, bool) {
	op := r.PathValue("operation")
	name, t, ok := s.opTarget(w, r, func(t config.Target) error { return ops.Validate(op, req.params(), t) })
	if !ok {
		return builtPlan{}, false
	}
	if ref != nil {
		ref.target, ref.instance = name, t.Instance
	}
	sess, closeOps, password, ok := s.openOpsWithPassword(w, r, name, t)
	if !ok {
		return builtPlan{}, false
	}
	prep, err := sess.Build(r.Context(), op, req.params())
	if err != nil {
		closeOps()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return builtPlan{}, false
	}
	return builtPlan{prep: prep, closeOps: closeOps, name: name, target: t, password: password}, true
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
	built, ok := s.buildPlan(w, r, req, nil)
	if !ok {
		return
	}
	defer built.closeOps()
	prep := built.prep
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

// executeBuildTimeout bounds the pre-flight checks of an execution, which no
// longer end with the request (opening a tunnel included).
const executeBuildTimeout = 5 * time.Minute

// executePlan builds the plan again (the instance may have changed since the
// preview), checks it, the digest and the confirmation, then runs it as a job
// of kind operation and streams it: "job" first, with the id of the job,
// "notice" for the pre-flight warnings, "step" for every action (and the
// wait), "task" for every change of the followed task, one "done". The job
// goes on if the client goes away; POST /api/v1/jobs/{id}/cancel stops it.
// A request that is refused leaves a failed job (refusals.go).
func (s *Server) executePlan(w http.ResponseWriter, r *http.Request) {
	var req ExecuteRequest
	if !knownOperation(w, r) || !readBody(w, r, &req) {
		return
	}
	rw := &refusalWriter{ResponseWriter: w}
	ref := &refused{kind: jobs.Operation, operation: r.PathValue("operation")}
	if item := pipeline.OperationItem(req.plan().params()); item != "" {
		ref.items = []string{item}
	}
	defer s.recordRefusal(rw, ref)
	w = rw
	// Once Run is pressed the operation goes ahead: the pre-flight checks do
	// not stop when the client goes away before the job starts (the dialog
	// closed, the window reloaded), which refused the run with "context
	// canceled". The stream below still follows the client.
	bctx, cancelBuild := context.WithTimeout(context.WithoutCancel(r.Context()), executeBuildTimeout)
	defer cancelBuild()
	built, ok := s.buildPlan(w, r.WithContext(bctx), req.plan(), ref)
	if !ok {
		return
	}
	defer built.closeOps()
	prep := built.prep
	if !prep.Ready() {
		rw.checks = nonOK(prep.Checks)
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
		rw.checks = nonOK(prep.Checks)
		http.Error(w, "the plan changed since the preview: preview it again", http.StatusPreconditionFailed)
		return
	}
	if want := prep.Plan.Confirmation(); want != "" && req.Confirm != want {
		http.Error(w, fmt.Sprintf("%v: retype %q", core.ErrNotConfirmed, want), http.StatusUnprocessableEntity)
		return
	}
	if prep.NeedsLoginPassword() {
		// The message says what is wrong with it, never what it is.
		if err := ops.CheckLoginPassword(req.LoginPassword); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	if prep.Replace != nil && req.NoWait {
		http.Error(w, ops.ErrReplaceNoWait.Error(), http.StatusUnprocessableEntity)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// The wait step hands the task in full (with its info) just before the
	// job reports it, so the "task" event carries what the CLI shows.
	var lastTask atomic.Pointer[mssql.Task]
	env := pipeline.Env{
		Name: built.name, Target: built.target, TaskInterval: s.opts.TaskInterval,
		// The steps open their sessions with the context of the job, not of
		// this request: the job outlives it.
		OpenOps: func(ctx context.Context) (*ops.Session, func(), error) {
			return s.opts.OpenOps(ctx, built.name, built.target, built.password)
		},
		OnTask:        func(t mssql.Task) { lastTask.Store(&t) },
		LoginPassword: req.LoginPassword,
	}
	spec, err := pipeline.OperationSpec(env, prep, req.NoWait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := s.opts.Jobs.Start(spec)
	switch {
	case errors.Is(err, jobs.ErrClosed):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sub, err := s.opts.Jobs.Subscribe(job.ID, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer sub.Close()
	op, name := prep.Plan.Operation, prep.Plan.Target
	s.opts.Log.Info("plan started", "job", job.ID, "target", name, "operation", op, "digest", prep.Digest())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	send := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}
	send("job", JobRef{ID: job.ID})
	for _, c := range prep.Checks {
		if c.Status == check.Warning {
			send("notice", Notice{Message: c.Label + ": " + c.Detail})
		}
	}
	titles := map[string]string{}
	for _, st := range job.Items[0].Steps {
		titles[st.ID] = st.Title
	}
	toStep := pipeline.OperationEvents(titles)
	handle := func(ev jobs.Event) {
		switch e, ok := toStep(ev); {
		case ok:
			send("step", e)
		case ev.Type == jobs.EventTaskStatus && ev.Task != nil && strings.HasPrefix(ev.Step, "wait"):
			// The task a wait follows, not the one a restore queued.
			t := mssql.Task{ID: ev.Task.ID, Type: ev.Task.Type, Database: job.Items[0].Name, Percent: ev.Task.Percent, Lifecycle: ev.Task.Lifecycle}
			if last := lastTask.Load(); last != nil && last.ID == t.ID {
				t = *last
			}
			send("task", t)
		}
	}
	for _, ev := range sub.Replay {
		handle(ev)
	}
	for open := true; open; {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				open = false
				break
			}
			handle(ev)
		case <-r.Context().Done():
			// The operation goes on without this client.
			s.opts.Log.Info("plan: client gone, the job goes on", "job", job.ID)
			return
		}
	}
	final, err := s.opts.Jobs.Get(job.ID)
	if err != nil {
		s.opts.Log.Warn("plan: job not readable", "job", job.ID, "err", err)
		return
	}
	done := planDone(final, prep, lastTask.Load())
	send("done", done)
	s.opts.Log.Info("plan finished", "job", job.ID, "target", name, "operation", op, "ok", done.OK)
}

// planDone is the final event of an execution, from the job that ran it.
func planDone(job jobs.Job, prep *ops.Prepared, task *mssql.Task) PlanDone {
	item := job.Items[0]
	done := PlanDone{OK: job.State == jobs.Succeeded, JobID: job.ID, Task: task, Replace: item.Replace}
	if done.Task == nil {
		done.Task = pipeline.OperationTask(item)
	}
	switch {
	case done.OK:
		done.Result = prep.Result()
	case item.Error != "":
		done.Error = item.Error
	case job.State == jobs.Cancelled:
		done.Error = "the job was cancelled"
	default:
		done.Error = "the job " + string(job.State)
	}
	return done
}

// databaseReport answers the post-restore report of a database (read-only).
// It is a view, not a run: it leaves no job.
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

// databaseAccess answers who can use a database (read-only). Like the
// report it is a view: it leaves no job.
func (s *Server) databaseAccess(w http.ResponseWriter, r *http.Request) {
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
	view, err := sess.Access(r.Context(), db)
	switch {
	case errors.Is(err, mssql.ErrNoDatabase):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, view)
	}
}

// LoginList is the answer of GET .../logins.
type LoginList struct {
	Logins []mssql.Login `json:"logins"`
}

// targetLogins lists the SQL logins of the instance (read-only, no job).
func (s *Server) targetLogins(w http.ResponseWriter, r *http.Request) {
	name, t, ok := s.opTarget(w, r, func(config.Target) error { return nil })
	if !ok {
		return
	}
	sess, closeOps, ok := s.openOps(w, r, name, t)
	if !ok {
		return
	}
	defer closeOps()
	logins, err := sess.Logins(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, LoginList{Logins: logins})
}
