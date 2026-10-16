package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// Session runs the native operations and their read-only checks on a SQL
// session. It reads tasks like Tasks, so it is a TaskReader too.
type Session struct {
	DB *sql.DB
	// Log receives how StartTask found the task (default slog.Default()).
	Log *slog.Logger
}

// List returns the tasks matching f, newest first.
func (s Session) List(ctx context.Context, f TaskFilter) ([]Task, error) {
	return Tasks{DB: s.DB}.List(ctx, f)
}

// Get returns one task, or ErrTaskNotFound.
func (s Session) Get(ctx context.Context, id int) (Task, error) { return Tasks{DB: s.DB}.Get(ctx, id) }

// DatabaseState returns state_desc of db (ONLINE, RESTORING...), or "" when
// the instance has no database with that name.
func (s Session) DatabaseState(ctx context.Context, db string) (string, error) {
	var state string
	err := s.DB.QueryRowContext(ctx, `SELECT state_desc FROM sys.databases WHERE name = @db`,
		sql.Named("db", db)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return state, err
}

// ActiveTasks counts the native tasks created, running or being cancelled,
// of db or of every database when db is "". RDS runs two at a time and
// queues the others.
func (s Session) ActiveTasks(ctx context.Context, db string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM msdb.dbo.rds_fn_task_status(NULL, 0)
		WHERE lifecycle IN ('CREATED', 'IN_PROGRESS', 'CANCEL_REQUESTED')
		  AND (@db = N'' OR database_name = @db)`, sql.Named("db", db)).Scan(&n)
	return n, err
}

// ActiveSessions counts the sessions, other than this one, that work in db.
func (s Session) ActiveSessions(ctx context.Context, db string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.dm_exec_sessions
		WHERE database_id = DB_ID(@db) AND session_id <> @@SPID AND is_user_process = 1`,
		sql.Named("db", db)).Scan(&n)
	return n, err
}

// Exec runs a statement that answers nothing to read (finish, drop, cancel).
func (s Session) Exec(ctx context.Context, stmt string) error {
	_, err := s.DB.ExecContext(ctx, stmt)
	return err
}

// StartTask runs a statement that queues a native task for db
// (rds_restore_database, rds_backup_database) and returns the task.
//
// The procedures answer with the task row: its task_id is read from there.
// Without one, the task is the newest one of db created after the
// statement, as mssql-import.sh finds it.
func (s Session) StartTask(ctx context.Context, stmt, db string) (Task, error) {
	var before int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT ISNULL(MAX(task_id), 0) FROM msdb.dbo.rds_fn_task_status(NULL, 0)`).Scan(&before); err != nil {
		return Task{}, err
	}
	id, err := s.execReadingTaskID(ctx, stmt)
	if err != nil {
		return Task{}, err
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	// Which way found it is worth knowing on a real instance: the answer of
	// the procedure is the expected one, the newest task the fallback.
	if id != 0 {
		log.Info("native task found in the answer of the procedure", "task", id, "database", db)
	} else {
		if err := s.DB.QueryRowContext(ctx, `SELECT ISNULL(MAX(task_id), 0)
			FROM msdb.dbo.rds_fn_task_status(NULL, 0)
			WHERE task_id > @before AND database_name = @db`,
			sql.Named("before", before), sql.Named("db", db)).Scan(&id); err != nil {
			return Task{}, err
		}
		if id != 0 {
			log.Info("native task found as the newest one of the database (no task_id in the answer)",
				"task", id, "database", db, "after", before)
		}
	}
	if id == 0 {
		return Task{}, fmt.Errorf("the statement ran but no task of %s showed up: look for it with dialga status --db %s", db, db)
	}
	return s.Get(ctx, id)
}

// execReadingTaskID runs stmt and returns the first task_id of its answer,
// 0 when it has none. It reads every result set: the errors the procedures
// raise after a first answer only show up that way.
func (s Session) execReadingTaskID(ctx context.Context, stmt string) (int, error) {
	rows, err := s.DB.QueryContext(ctx, stmt)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	id := 0
	for {
		cols, err := rows.Columns()
		if err != nil {
			return 0, err
		}
		col := slices.IndexFunc(cols, func(c string) bool { return strings.EqualFold(c, "task_id") })
		for rows.Next() {
			if id != 0 || col < 0 {
				continue
			}
			dest := make([]any, len(cols))
			var taskID sql.NullInt64
			for i := range dest {
				dest[i] = new(any)
			}
			dest[col] = &taskID
			if err := rows.Scan(dest...); err != nil {
				return 0, err
			}
			id = int(taskID.Int64)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return id, rows.Err()
}
