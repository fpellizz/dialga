package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// maxBackupFiles is how many files RDS restores from one striped backup.
const maxBackupFiles = 10

// Restore prepares rds_restore_database of the backup at key into db. The
// database must not exist (with Differential it must, left RESTORING by a
// full restore with NoRecovery) and the backup must be in the bucket; a "*"
// in key restores a backup striped over several files.
func (s *Session) Restore(ctx context.Context, db, key string, o mssql.RestoreOptions) (*Prepared, error) {
	in := Params{Database: db, Key: key, Restore: o}
	if err := Validate(OpRestore, in, s.Target); err != nil {
		return nil, err
	}
	key, bare, _ := resolveKey(key, s.Target.Prefixes.Import, s.Target.Bucket)
	p := s.prepare(OpRestore, in)
	key = s.checkSource(ctx, p, key, bare, in.Key)
	p.Params.Key = key

	state, err := s.SQL.DatabaseState(ctx, db)
	if err != nil {
		return nil, err
	}
	const step, label = "database", "Target database"
	switch {
	case o.Differential && state == "":
		p.fail(step, label, db+" does not exist: a differential restore needs it RESTORING (full restore with --norecovery first)")
	case o.Differential && state != "RESTORING":
		p.fail(step, label, db+" is "+state+": a differential restore needs it RESTORING (full restore with --norecovery first)")
	case o.Differential:
		p.ok(step, label, db+" is RESTORING (differential restore)")
	case state != "":
		p.fail(step, label, db+" already exists ("+state+"): choose another name or drop it first")
	default:
		p.ok(step, label, db+" does not exist yet")
	}
	if err := s.checkActiveTasks(ctx, p, ""); err != nil {
		return nil, err
	}

	arn := mssql.S3ARN(s.Target.Bucket, key)
	stmt := mssql.RestoreDatabase(db, arn, o)
	p.Plan.Actions = []core.Action{{
		ID: "restore", Title: fmt.Sprintf("Restore %s from s3://%s/%s", db, s.Target.Bucket, key),
		Kind: core.KindSQL, Risk: core.Change, Statement: stmt,
		Run: p.startTask(stmt, db),
	}}
	return p, nil
}

// checkSource looks for the backup of a restore and returns the key to use:
// a bare name missing under the prefix is looked for at the bucket root
// too, as mssql-import.sh does.
func (s *Session) checkSource(ctx context.Context, p *Prepared, key string, bare bool, typed string) string {
	const step, label = "source", "Backup in S3"
	where := func(k string) string { return "s3://" + s.Target.Bucket + "/" + k }
	if strings.Contains(key, "*") {
		literal, _, _ := strings.Cut(key, "*")
		all, err := s.Bucket.Keys(ctx, literal)
		if err != nil {
			p.fail(step, label, where(key)+": "+err.Error())
			return key
		}
		var files []string
		match := globPattern(key)
		for _, k := range all {
			if match.MatchString(k) {
				files = append(files, k)
			}
		}
		switch {
		case len(files) == 0:
			p.fail(step, label, "no object matches "+where(key)+" (upload the files first)")
		case len(files) > maxBackupFiles:
			p.fail(step, label, fmt.Sprintf("RDS restores at most %d files per backup: %s matches %d", maxBackupFiles, where(key), len(files)))
		default:
			p.ok(step, label, fmt.Sprintf("backup in %d files: %s", len(files), strings.Join(files, ", ")))
		}
		return key
	}
	size, err := s.Bucket.Size(ctx, key)
	if bare && errors.Is(err, awsx.ErrObjectNotFound) {
		if rootSize, rootErr := s.Bucket.Size(ctx, typed); rootErr == nil {
			key, size, err = typed, rootSize, nil
		}
	}
	switch {
	case errors.Is(err, awsx.ErrObjectNotFound):
		p.fail(step, label, "object not found: "+where(key)+" (upload it first)")
	case err != nil:
		p.fail(step, label, where(key)+": "+err.Error())
	default:
		p.ok(step, label, fmt.Sprintf("%s (%s)", where(key), humanBytes(size)))
	}
	return key
}

// globPattern matches keys the way the shell matches them in
// mssql-import.sh: "*" is any run of characters, "/" included. The other
// characters of a valid key are literal.
func globPattern(key string) *regexp.Regexp {
	parts := strings.Split(key, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

// Export prepares rds_backup_database of db to key, by default
// <export prefix>/<db>_<YYYYMMDD-HHMMSS>.bak (UTC). It never overwrites:
// an existing object fails the pre-flight, and the statement says so too.
func (s *Session) Export(ctx context.Context, db, key string) (*Prepared, error) {
	in := Params{Database: db, Key: key}
	if err := Validate(OpExport, in, s.Target); err != nil {
		return nil, err
	}
	if key == "" {
		now := time.Now
		if s.Now != nil {
			now = s.Now
		}
		key = fmt.Sprintf("%s/%s_%s.bak", s.Target.Prefixes.Export, db, now().UTC().Format("20060102-150405"))
	} else {
		key, _, _ = resolveKey(key, s.Target.Prefixes.Export, s.Target.Bucket)
	}
	p := s.prepare(OpExport, in)
	p.Params.Key = key

	if err := s.checkDatabase(ctx, p, db, "ONLINE", "only an online database can be backed up"); err != nil {
		return nil, err
	}
	where := "s3://" + s.Target.Bucket + "/" + key
	const step, label = "destination", "Destination in S3"
	switch _, err := s.Bucket.Size(ctx, key); {
	case errors.Is(err, awsx.ErrObjectNotFound):
		p.ok(step, label, where+" is free")
	case err != nil:
		p.fail(step, label, where+": "+err.Error())
	default:
		p.fail(step, label, where+" already exists: an export never overwrites, choose another key")
	}
	if err := s.checkActiveTasks(ctx, p, ""); err != nil {
		return nil, err
	}

	stmt := mssql.BackupDatabase(db, mssql.S3ARN(s.Target.Bucket, key))
	p.Plan.Actions = []core.Action{{
		ID: "export", Title: fmt.Sprintf("Back up %s to %s", db, where),
		Kind: core.KindSQL, Risk: core.Change, Statement: stmt,
		Run: p.startTask(stmt, db),
	}}
	return p, nil
}

// Finish prepares rds_finish_restore of db, left RESTORING by a restore
// with NoRecovery.
func (s *Session) Finish(ctx context.Context, db string) (*Prepared, error) {
	in := Params{Database: db}
	if err := Validate(OpFinish, in, s.Target); err != nil {
		return nil, err
	}
	p := s.prepare(OpFinish, in)
	if err := s.checkDatabase(ctx, p, db, "RESTORING", "nothing to finish"); err != nil {
		return nil, err
	}
	stmt := mssql.FinishRestore(db)
	p.Plan.Actions = []core.Action{{
		ID: "finish", Title: fmt.Sprintf("Finish the restore of %s (bring it online)", db),
		Kind: core.KindSQL, Risk: core.Change, Statement: stmt,
		Run: func(ctx context.Context) (string, error) {
			if err := s.SQL.Exec(ctx, stmt); err != nil {
				return "", err
			}
			state, err := s.SQL.DatabaseState(ctx, db)
			if err != nil || state == "" {
				return "restore finished", nil
			}
			return db + " is " + state, nil
		},
	}}
	return p, nil
}

// Cancel prepares rds_cancel_task of a native task that has not ended.
func (s *Session) Cancel(ctx context.Context, id int) (*Prepared, error) {
	in := Params{TaskID: id}
	if err := Validate(OpCancel, in, s.Target); err != nil {
		return nil, err
	}
	p := s.prepare(OpCancel, in)
	const step, label = "task", "Native task"
	task, err := s.SQL.Get(ctx, id)
	what := fmt.Sprintf("task %d", id)
	switch {
	case errors.Is(err, mssql.ErrTaskNotFound):
		p.fail(step, label, what+" not found (RDS keeps 36 days of history)")
	case err != nil:
		return nil, err
	default:
		what = fmt.Sprintf("task %d (%s of %s)", id, task.Type, task.Database)
		switch {
		case task.Lifecycle.Done():
			p.fail(step, label, what+" already ended in "+string(task.Lifecycle))
		case task.Lifecycle == mssql.CancelRequested:
			p.warn(step, label, what+": cancel already requested")
		default:
			p.ok(step, label, fmt.Sprintf("%s: %s %d%%", what, task.Lifecycle, task.Percent))
		}
	}
	stmt := mssql.CancelTask(id)
	p.Plan.Actions = []core.Action{{
		ID: "cancel", Title: "Cancel " + what,
		Kind: core.KindSQL, Risk: core.Change, Statement: stmt,
		Run: func(ctx context.Context) (string, error) {
			if err := s.SQL.Exec(ctx, stmt); err != nil {
				return "", err
			}
			return fmt.Sprintf("cancel requested: follow it with dialga wait %d", id), nil
		},
	}}
	return p, nil
}

// Drop prepares rds_drop_database of db. It cannot be undone: the plan asks
// to retype the database name (the target name on a protected target).
func (s *Session) Drop(ctx context.Context, db string) (*Prepared, error) {
	in := Params{Database: db}
	if err := Validate(OpDrop, in, s.Target); err != nil {
		return nil, err
	}
	p := s.prepare(OpDrop, in)
	p.Plan.Subject = db
	if err := s.checkDatabase(ctx, p, db, "", ""); err != nil {
		return nil, err
	}
	n, err := s.SQL.ActiveTasks(ctx, db)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		p.warn("tasks", "Native tasks", fmt.Sprintf("%d native task(s) of %s still running: the drop may fail or break them", n, db))
	} else {
		p.ok("tasks", "Native tasks", "no native task of "+db+" running")
	}
	stmt := mssql.DropDatabase(db)
	p.Plan.Actions = []core.Action{{
		ID: "drop", Title: "Drop database " + db,
		Kind: core.KindSQL, Risk: core.Destructive, Statement: stmt,
		Run: func(ctx context.Context) (string, error) {
			if err := s.SQL.Exec(ctx, stmt); err != nil {
				return "", err
			}
			return db + " dropped", nil
		},
	}}
	return p, nil
}

// checkDatabase checks that db exists and, when want is set, is in that
// state (why explains the refusal).
func (s *Session) checkDatabase(ctx context.Context, p *Prepared, db, want, why string) error {
	state, err := s.SQL.DatabaseState(ctx, db)
	if err != nil {
		return err
	}
	const step, label = "database", "Database"
	switch {
	case state == "":
		p.fail(step, label, db+" not found on the instance")
	case want != "" && state != want:
		p.fail(step, label, db+" is "+state+", not "+want+": "+why)
	default:
		p.ok(step, label, db+" is "+state)
	}
	return nil
}

// checkActiveTasks warns when native tasks are running or queued.
func (s *Session) checkActiveTasks(ctx context.Context, p *Prepared, db string) error {
	n, err := s.SQL.ActiveTasks(ctx, db)
	if err != nil {
		return err
	}
	if n > 0 {
		p.warn("tasks", "Native tasks", fmt.Sprintf("%d native task(s) running or queued: RDS runs two at a time, the new one may wait", n))
	} else {
		p.ok("tasks", "Native tasks", "no native task running")
	}
	return nil
}

// startTask runs a statement that queues a native task and keeps the task.
func (p *Prepared) startTask(stmt, db string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		task, err := p.sql.StartTask(ctx, stmt, db)
		if err != nil {
			return "", err
		}
		p.task = &task
		return fmt.Sprintf("task %d created", task.ID), nil
	}
}

// humanBytes formats a size with binary units, as numfmt --to=iec-i does.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
