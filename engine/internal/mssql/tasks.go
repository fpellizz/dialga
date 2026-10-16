package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Lifecycle of a native backup/restore task, as rds_fn_task_status reports it.
type Lifecycle string

const (
	Created         Lifecycle = "CREATED"
	InProgress      Lifecycle = "IN_PROGRESS"
	Success         Lifecycle = "SUCCESS"
	Error           Lifecycle = "ERROR"
	CancelRequested Lifecycle = "CANCEL_REQUESTED"
	Cancelled       Lifecycle = "CANCELLED"
)

// Done reports whether the task reached a final state.
func (l Lifecycle) Done() bool { return l == Success || l == Error || l == Cancelled }

// Task is one row of msdb.dbo.rds_fn_task_status. Times are the server's
// wall clock, without a zone (RDS runs in UTC unless the instance has a time
// zone set), formatted as 2006-01-02T15:04:05.
type Task struct {
	ID        int       `json:"id"`
	Type      string    `json:"type"`
	Database  string    `json:"database"`
	Percent   int       `json:"percent"`
	Lifecycle Lifecycle `json:"lifecycle"`
	Info      string    `json:"info,omitempty"`
	CreatedAt string    `json:"createdAt,omitempty"`
	UpdatedAt string    `json:"updatedAt,omitempty"`
	S3Object  string    `json:"s3Object,omitempty"`
}

// TaskFilter selects the tasks to list, newest first.
type TaskFilter struct {
	// Database keeps only the tasks of this database; empty means all.
	Database string
	// Limit caps the rows (1-100, default 20).
	Limit int
}

// ErrTaskNotFound means rds_fn_task_status has no task with that id (RDS
// keeps 36 days of history).
var ErrTaskNotFound = errors.New("task not found")

// TaskReader reads the native tasks of an instance. Tasks implements it on a
// SQL session; tests replace it.
type TaskReader interface {
	List(ctx context.Context, f TaskFilter) ([]Task, error)
	Get(ctx context.Context, id int) (Task, error)
}

// Tasks reads the native tasks through a SQL session.
type Tasks struct{ DB *sql.DB }

// The duration column is not read: the AWS docs spell it in several ways
// (on RDS it is "duration(mins)", as in testdata/mock-rds.sql).
const taskColumns = `task_id, task_type, database_name, [% complete], lifecycle,
	task_info, created_at, last_updated, S3_object_arn`

// List returns the tasks matching f, newest first.
func (t Tasks) List(ctx context.Context, f TaskFilter) ([]Task, error) {
	limit := f.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("limit must be between 1 and 100 (got %d)", limit)
	}
	rows, err := t.DB.QueryContext(ctx, `SELECT TOP (@limit) `+taskColumns+`
		FROM msdb.dbo.rds_fn_task_status(NULL, 0)
		WHERE @db = N'' OR database_name = @db
		ORDER BY task_id DESC`,
		sql.Named("limit", limit), sql.Named("db", f.Database))
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// Get returns one task, or ErrTaskNotFound.
func (t Tasks) Get(ctx context.Context, id int) (Task, error) {
	if id < 1 {
		return Task{}, fmt.Errorf("invalid task id %d", id)
	}
	rows, err := t.DB.QueryContext(ctx, `SELECT `+taskColumns+`
		FROM msdb.dbo.rds_fn_task_status(NULL, @id)`, sql.Named("id", id))
	if err != nil {
		return Task{}, err
	}
	tasks, err := scanTasks(rows)
	if err != nil {
		return Task{}, err
	}
	if len(tasks) == 0 {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	return tasks[0], nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	defer func() { _ = rows.Close() }()
	tasks := []Task{}
	for rows.Next() {
		var (
			task                         Task
			typ, db, lifecycle, info, s3 sql.NullString
			pct                          sql.NullInt64
			created, updated             sql.NullTime
		)
		if err := rows.Scan(&task.ID, &typ, &db, &pct, &lifecycle, &info, &created, &updated, &s3); err != nil {
			return nil, err
		}
		task.Type, task.Database, task.Info, task.S3Object = typ.String, db.String, info.String, s3.String
		task.Percent = int(pct.Int64)
		task.Lifecycle = Lifecycle(lifecycle.String)
		task.CreatedAt, task.UpdatedAt = serverTime(created), serverTime(updated)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func serverTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format("2006-01-02T15:04:05")
}

// WaitOptions tunes Wait.
type WaitOptions struct {
	// Interval between two reads (default 30s; RDS updates the status
	// about every 2 minutes).
	Interval time.Duration
	// MaxFailures consecutive failed reads end the wait (default 10): the
	// session may drop (tunnel closed) and come back.
	MaxFailures int
	// OnChange receives the task on the first read and whenever its
	// lifecycle or percentage changes.
	OnChange func(Task)
	// OnRetry receives a failed read that will be retried.
	OnRetry func(error)
}

// Wait reads the task until it reaches a final state and returns it. It only
// waits: cancelling ctx stops the wait, never the task.
func Wait(ctx context.Context, r TaskReader, id int, opts WaitOptions) (Task, error) {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = 10
	}
	var last *Task
	failures := 0
	for {
		task, err := r.Get(ctx, id)
		switch {
		case err == nil:
			failures = 0
			if last == nil || last.Lifecycle != task.Lifecycle || last.Percent != task.Percent {
				if opts.OnChange != nil {
					opts.OnChange(task)
				}
			}
			last = &task
			if task.Lifecycle.Done() {
				return task, nil
			}
		case errors.Is(err, ErrTaskNotFound), ctx.Err() != nil:
			return Task{}, err
		default:
			failures++
			if failures >= opts.MaxFailures {
				return Task{}, fmt.Errorf("task %d: %d reads failed in a row, last: %w", id, failures, err)
			}
			if opts.OnRetry != nil {
				opts.OnRetry(err)
			}
		}
		select {
		case <-ctx.Done():
			return Task{}, ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}
