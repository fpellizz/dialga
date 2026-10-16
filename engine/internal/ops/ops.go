// Package ops holds the native operations on a target: restore, export,
// finish, cancel and drop, plus the read-only post-restore report. They are
// the commands of mssql-import.sh, with the same pre-flight checks.
//
// A builder only reads: it runs the pre-flight checks (the database exists
// or not, the backup is in the bucket, tasks are running) and returns a
// Prepared operation whose plan carries the exact T-SQL of tsql.go. The
// caller previews the plan (dry-run, the default) or executes it, after the
// confirmation the plan asks for. Execute follows the native task that a
// restore or an export starts until it ends.
package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/session"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// The operations, as the CLI commands and the API name them.
const (
	OpRestore = "restore"
	OpExport  = "export"
	OpFinish  = "finish"
	OpCancel  = "cancel"
	OpDrop    = "drop"
)

// Operations are the operations Build knows.
var Operations = []string{OpRestore, OpExport, OpFinish, OpCancel, OpDrop}

// SQL is what the operations read and run on the SQL Server of a target.
// mssql.Session implements it; tests replace it.
type SQL interface {
	mssql.TaskReader
	DatabaseState(ctx context.Context, db string) (string, error)
	ActiveTasks(ctx context.Context, db string) (int, error)
	StartTask(ctx context.Context, stmt, db string) (mssql.Task, error)
	Exec(ctx context.Context, stmt string) error
	DatabaseReport(ctx context.Context, db string) (mssql.DatabaseReport, error)
}

// Objects reads the bucket of a target. awsx.Bucket implements it.
type Objects interface {
	Size(ctx context.Context, key string) (int64, error)
	Keys(ctx context.Context, prefix string) ([]string, error)
}

// Session is an open target the operations work on.
type Session struct {
	// Name is the target name in the configuration.
	Name   string
	Target config.Target
	SQL    SQL
	Bucket Objects
	// Now dates the default export keys (default time.Now).
	Now func() time.Time
}

// Open connects to the target: AWS credentials with the account guard, the
// bucket and a SQL session with the certificate verified. close ends it.
func Open(ctx context.Context, name string, t config.Target, password string) (*Session, func(), error) {
	return OpenWith(session.Sessions{})(ctx, name, t, password)
}

// OpenWith is Open through sessions: with the engine's shared tunnels (or a
// job's), a tunnel-mode target borrows that tunnel instead of opening one.
func OpenWith(sessions session.Sessions) func(ctx context.Context, name string, t config.Target, password string) (*Session, func(), error) {
	return func(ctx context.Context, name string, t config.Target, password string) (*Session, func(), error) {
		db, cfg, err := sessions.OpenWithAWS(ctx, t, password)
		if err != nil {
			return nil, nil, err
		}
		s := &Session{Name: name, Target: t, SQL: mssql.Session{DB: db}, Bucket: awsx.NewBucket(cfg, t.Bucket)}
		return s, func() { _ = db.Close() }, nil
	}
}

// Params are the parameters of an operation.
type Params struct {
	// Database is the database to restore, back up, finish or drop.
	Database string
	// Key is the S3 key of the backup: required by restore, optional for
	// export. A bare file name goes under the target prefix; s3://bucket/key
	// must name the bucket of the target.
	Key     string
	Restore mssql.RestoreOptions
	// TaskID is the native task to cancel.
	TaskID int
}

// Validate checks the parameters of op against the target without reaching
// it, so callers reject bad input before opening a session.
func Validate(op string, p Params, t config.Target) error {
	switch op {
	case OpRestore, OpExport, OpFinish, OpDrop:
		if err := validate.DBName(p.Database); err != nil {
			return err
		}
	case OpCancel:
		if p.TaskID < 1 {
			return fmt.Errorf("invalid task id %d", p.TaskID)
		}
		return nil
	default:
		return fmt.Errorf("unknown operation %q (known: %s)", op, strings.Join(Operations, ", "))
	}
	switch {
	case op == OpRestore && p.Key == "":
		return errors.New("restore needs the S3 key of the backup")
	case op != OpRestore && op != OpExport:
		return nil
	case op == OpExport && p.Key == "":
		return nil // the default key
	}
	key, _, err := resolveKey(p.Key, prefixOf(op, t), t.Bucket)
	if err != nil {
		return err
	}
	if op == OpExport && strings.Contains(key, "*") {
		return fmt.Errorf("invalid S3 key %q: an export writes one file, no * allowed", key)
	}
	return nil
}

func prefixOf(op string, t config.Target) string {
	if op == OpExport {
		return t.Prefixes.Export
	}
	return t.Prefixes.Import
}

// resolveKey turns what the user typed into a key of bucket: s3://bucket/key
// (that bucket only: RDS reads and writes it through the option group role),
// a key with a "/" as it is, a bare name under prefix (bare reports it).
func resolveKey(in, prefix, bucket string) (key string, bare bool, err error) {
	switch rest, isURI := strings.CutPrefix(in, "s3://"); {
	case isURI:
		b, k, _ := strings.Cut(rest, "/")
		if b != bucket {
			return "", false, fmt.Errorf("the bucket must be %s: RDS reaches only the bucket of the target", bucket)
		}
		key = k
	case strings.Contains(in, "/"):
		key = in
	default:
		key, bare = prefix+"/"+in, true
	}
	if key == "" || strings.HasSuffix(key, "/") {
		return "", false, fmt.Errorf("invalid S3 key %q: it names no object", in)
	}
	return key, bare, validate.S3Key(key)
}

// Build prepares op with p: the entry point of the CLI and the API.
func (s *Session) Build(ctx context.Context, op string, p Params) (*Prepared, error) {
	switch op {
	case OpRestore:
		return s.Restore(ctx, p.Database, p.Key, p.Restore)
	case OpExport:
		return s.Export(ctx, p.Database, p.Key)
	case OpFinish:
		return s.Finish(ctx, p.Database)
	case OpCancel:
		return s.Cancel(ctx, p.TaskID)
	case OpDrop:
		return s.Drop(ctx, p.Database)
	}
	return nil, Validate(op, p, s.Target)
}

// Prepared is an operation after its pre-flight checks: a plan to preview
// and, when Ready, to execute.
type Prepared struct {
	Plan core.Plan
	// Checks are the pre-flight checks in order, ok, warning or failed. A
	// failed one keeps the plan from running.
	Checks []check.Event
	// Params are the parameters with the defaults resolved (keys).
	Params Params

	sql SQL
	// task is the native task the plan started, set while it runs.
	task *mssql.Task
}

func (s *Session) prepare(op string, p Params) *Prepared {
	return &Prepared{
		Plan:   core.Plan{Operation: op, Target: s.Name, Instance: s.Target.Instance, Protected: s.Target.Protected},
		Params: p,
		sql:    s.SQL,
	}
}

func (p *Prepared) ok(step, label, detail string) {
	p.Checks = append(p.Checks, check.Event{Step: step, Label: label, Status: check.OK, Detail: detail})
}

func (p *Prepared) warn(step, label, detail string) {
	p.Checks = append(p.Checks, check.Event{Step: step, Label: label, Status: check.Warning, Detail: detail})
}

func (p *Prepared) fail(step, label, detail string) {
	p.Checks = append(p.Checks, check.Event{Step: step, Label: label, Status: check.Failed, Detail: detail})
}

// Ready reports whether every pre-flight check passed (warnings included).
func (p *Prepared) Ready() bool {
	for _, c := range p.Checks {
		if c.Status == check.Failed {
			return false
		}
	}
	return true
}

// Digest identifies the plan: the same digest means the same statements on
// the same target, so what runs is what the user previewed.
func (p *Prepared) Digest() string {
	data, _ := json.Marshal(p.Plan)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// ErrNotReady means a pre-flight check failed: nothing was run.
var ErrNotReady = errors.New("a pre-flight check failed: nothing was changed")

// ExecOptions tunes Execute.
type ExecOptions struct {
	// Confirm is the text the user retyped (Plan.Confirmation).
	Confirm string
	// Wait follows the native task the plan starts until it ends.
	Wait bool
	// Interval between two reads of the task (default 30s).
	Interval time.Duration
	// OnStep receives the events of the actions, then of the wait.
	OnStep func(core.Event)
	// OnTask receives the task on the first read and on every change.
	OnTask func(mssql.Task)
	// OnRetry receives a failed read of the task that will be retried.
	OnRetry func(error)
}

// Execute runs the plan after checking the confirmation and, with
// opts.Wait, follows the native task it started. It returns that task, last
// state read (nil when the operation starts none). A task that ends in ERROR
// or CANCELLED is not an error: the caller reads its lifecycle. Cancelling
// ctx while waiting stops the wait, never the task.
func (p *Prepared) Execute(ctx context.Context, opts ExecOptions) (*mssql.Task, error) {
	if !p.Ready() {
		return nil, ErrNotReady
	}
	emit := opts.OnStep
	if emit == nil {
		emit = func(core.Event) {}
	}
	if err := p.Plan.Execute(ctx, opts.Confirm, emit); err != nil {
		return p.task, err
	}
	if p.task == nil || !opts.Wait {
		return p.task, nil
	}
	id := p.task.ID
	label := fmt.Sprintf("Wait for task %d", id)
	emit(core.Event{Step: "wait", Label: label, Status: core.Running})
	task, err := mssql.Wait(ctx, p.sql, id, mssql.WaitOptions{
		Interval: opts.Interval, OnChange: opts.OnTask, OnRetry: opts.OnRetry,
	})
	if errors.Is(err, context.Canceled) {
		err = fmt.Errorf("wait interrupted: task %d keeps running on the instance", id)
	}
	if err != nil {
		emit(core.Event{Step: "wait", Label: label, Status: core.Failed, Detail: err.Error()})
		return p.task, err
	}
	if task.Lifecycle == mssql.Success {
		emit(core.Event{Step: "wait", Label: label, Status: core.OK, Detail: "task completed"})
	} else {
		emit(core.Event{Step: "wait", Label: label, Status: core.Failed, Detail: "task ended in " + string(task.Lifecycle)})
	}
	return &task, nil
}
