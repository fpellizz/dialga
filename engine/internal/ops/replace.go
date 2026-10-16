package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// The strategies of a restore onto a database that already exists. RDS refuses
// a native restore over an existing database, so the plan either restores
// beside it and swaps the names (swap), or drops it first (drop).
const (
	// ReplaceNone refuses an existing database, as a restore always did.
	ReplaceNone = ""
	// ReplaceAuto picks swap when two copies fit in the free storage, else drop.
	ReplaceAuto = "auto"
	// ReplaceSwap restores into a temporary name, then renames: the current
	// database is kept under an old name.
	ReplaceSwap = "swap"
	// ReplaceDrop drops the current database and restores in its place,
	// after a backup of it to S3 unless BackupFirst says no.
	ReplaceDrop = "drop"
)

// ReplaceOptions say what a restore does when the database already exists.
type ReplaceOptions struct {
	// Mode is "", "auto", "swap" or "drop".
	Mode string
	// BackupFirst makes a drop back the current database up to S3 first; nil
	// means true. With "auto" it is true whenever auto picks drop.
	BackupFirst *bool
	// Stamp (yyyymmddhhmmss, UTC) dates the temporary, old and backup names;
	// "" is now. A preview resolves it: send it back to execute the same names.
	Stamp string
	// BackupSize is the size of the backup in bytes when the plan knows it and
	// S3 does not yet (an upload that follows); 0 reads it from S3.
	BackupSize int64
}

// WantsBackup reports whether a drop backs the database up first.
func (o ReplaceOptions) WantsBackup() bool { return o.BackupFirst == nil || *o.BackupFirst }

// ValidateReplace checks the replace options of a restore without reaching
// the target.
func ValidateReplace(o ReplaceOptions, r mssql.RestoreOptions) error {
	switch o.Mode {
	case ReplaceNone:
		if o.BackupFirst != nil {
			return errors.New("backupFirst needs replace (auto, swap or drop)")
		}
	case ReplaceAuto, ReplaceSwap, ReplaceDrop:
		if r.Differential || r.NoRecovery {
			return errors.New("replace restores a full backup with recovery: it cannot combine with differential or noRecovery")
		}
		if o.Mode == ReplaceSwap && o.BackupFirst != nil {
			return errors.New("backupFirst is for drop (swap keeps the current database)")
		}
	default:
		return fmt.Errorf("invalid replace %q (known: auto, swap, drop)", o.Mode)
	}
	if o.Stamp != "" {
		return validate.Stamp(o.Stamp)
	}
	return nil
}

// The operations of a replace step.
const (
	StepBackup     = "backup"
	StepDrop       = "drop"
	StepRestore    = "restore"
	StepWait       = "wait"
	StepSingleUser = "single-user"
	StepRename     = "rename"
	StepMultiUser  = "multi-user"
	StepPost       = "post"
)

// ReplaceStep is one step of a replace: the same list gives the actions of an
// operation and the steps of a job.
type ReplaceStep struct {
	// ID is unique within the replace; it names the action.
	ID    string
	Title string
	Op    string
	Risk  core.Risk
	// Statement is the exact T-SQL, or the description of a read.
	Statement string
	// Database is what the step works on: the database to drop, rename or
	// open, or the name a restore or a backup task is for.
	Database string
	// To is the new name of a rename.
	To string
	// Fallback (rename only) is the database to rename back to To if the
	// rename fails: the compensation of a swap.
	Fallback string
	// Key is the S3 key of a restore or a backup.
	Key string
	// What names the task a wait follows ("restore", "backup").
	What string
}

// Replacement is how a restore replaces the database it finds.
type Replacement struct {
	// Strategy is "swap" or "drop", auto resolved.
	Strategy string `json:"strategy"`
	// Reason says why this strategy (the text of the replace check).
	Reason string `json:"reason"`
	// BackupFirst: a drop backs the database up to S3 before.
	BackupFirst bool   `json:"backupFirst"`
	Stamp       string `json:"stamp"`
	// Temp is where a swap restores, Old where it keeps the current database.
	Temp string `json:"temp,omitempty"`
	Old  string `json:"old,omitempty"`
	// BackupKey is the S3 key of the safety backup of a drop.
	BackupKey string `json:"backupKey,omitempty"`
	// Result is what the user keeps once it is done: the old database or the
	// backup.
	Result string `json:"result"`

	Steps  []ReplaceStep `json:"-"`
	bucket string
}

// stampOf is the stamp of the names of a replace: the one given, or now.
func (s *Session) stampOf(o ReplaceOptions) string {
	if o.Stamp != "" {
		return o.Stamp
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().UTC().Format("20060102150405")
}

// dataSize is the size of the data files of db in bytes, 0 when unknown.
func (s *Session) dataSize(ctx context.Context, db string) int64 {
	rep, err := s.SQL.DatabaseReport(ctx, db)
	if err != nil {
		return 0
	}
	var mb float64
	for _, f := range rep.Files {
		if f.Type != "LOG" {
			mb += f.SizeMB
		}
	}
	return int64(mb * (1 << 20))
}

// planReplace adds the pre-flight checks of a restore over the existing db and
// returns how it replaces it. size is the size of the backup (0 when unknown).
func (s *Session) planReplace(ctx context.Context, p *Prepared, db, key string, size int64, o ReplaceOptions) (*Replacement, error) {
	const step = "replace"
	existing := s.dataSize(ctx, db)
	need := size
	if need <= 0 {
		need = existing
	}
	var free int64
	freeKnown := false
	if s.FreeStorage != nil {
		if f, err := s.FreeStorage(ctx); err == nil {
			free, freeKnown = f, true
		}
	}

	stamp := s.stampOf(o)
	r := &Replacement{Stamp: stamp, BackupFirst: o.WantsBackup(), bucket: s.Target.Bucket}
	strategy := o.Mode
	switch {
	case o.Mode != ReplaceAuto:
	case !freeKnown || need <= 0:
		strategy = ReplaceSwap
		p.warn(step, "Replace", "swap: free storage or the size of the backup is unknown, so auto keeps the current database (swap) "+
			"and restores beside it: if the instance runs out of space the restore fails and nothing is changed")
	case 2*need <= free:
		strategy = ReplaceSwap
		p.ok(step, "Replace", fmt.Sprintf("swap: two copies (2 × %s) fit in %s free", humanBytes(need), humanBytes(free)))
	default:
		strategy, r.BackupFirst = ReplaceDrop, true
		p.ok(step, "Replace", fmt.Sprintf("drop: not enough free storage for two copies (needs %s, %s free); the current database is backed up to S3 first",
			humanBytes(2*need), humanBytes(free)))
	}
	r.Strategy = strategy

	switch {
	case o.Mode == ReplaceAuto:
	case strategy == ReplaceSwap && freeKnown && need > 0 && 2*need > free:
		p.warn(step, "Replace", fmt.Sprintf("swap: two copies (2 × %s) do not fit in %s free: the restore beside the current database may fail "+
			"(the current database stays untouched until it ends)", humanBytes(need), humanBytes(free)))
	case strategy == ReplaceSwap:
		p.ok(step, "Replace", "swap: restore beside "+db+", then swap the names; the current database is kept")
	case strategy == ReplaceDrop && !r.BackupFirst:
		p.warn(step, "Replace", "drop: the current database is dropped WITHOUT a backup, then restored: if the restore fails, its data is gone")
	default:
		p.ok(step, "Replace", "drop: the current database is backed up to S3, dropped, then restored")
	}
	if strategy == ReplaceDrop && freeKnown && need > 0 && need > free+existing {
		p.warn("storage", "Free storage", fmt.Sprintf("the backup is %s, %s is free and %s is freed by the drop: the restored database may not fit",
			humanBytes(need), humanBytes(free), humanBytes(existing)))
	}

	if n, err := s.SQL.ActiveSessions(ctx, db); err != nil {
		p.warn("sessions", "Active sessions", "sessions on "+db+" not read: "+err.Error())
	} else if n > 0 {
		p.warn("sessions", "Active sessions", fmt.Sprintf("%d active session(s) on %s will be disconnected", n, db))
	} else {
		p.ok("sessions", "Active sessions", "no active session on "+db)
	}

	switch strategy {
	case ReplaceSwap:
		err := s.swapSteps(ctx, p, r, db, key)
		return r, err
	default:
		err := s.dropSteps(ctx, p, r, db, key)
		return r, err
	}
}

// nameFree fails the check when name is not a valid name or exists already.
func (s *Session) nameFree(ctx context.Context, p *Prepared, name string) error {
	const step, label = "names", "Temporary names"
	if err := validate.DerivedDBName(name); err != nil {
		p.fail(step, label, name+": "+err.Error())
		return nil
	}
	state, err := s.SQL.DatabaseState(ctx, name)
	if err != nil {
		return err
	}
	if state != "" {
		p.fail(step, label, name+" already exists ("+state+"): choose another stamp")
		return nil
	}
	p.ok(step, label, name+" is free")
	return nil
}

func (s *Session) swapSteps(ctx context.Context, p *Prepared, r *Replacement, db, key string) error {
	r.Temp, r.Old = db+"_dialga_new_"+r.Stamp, db+"_old_"+r.Stamp
	if err := s.nameFree(ctx, p, r.Temp); err != nil {
		return err
	}
	if err := s.nameFree(ctx, p, r.Old); err != nil {
		return err
	}
	arn := mssql.S3ARN(s.Target.Bucket, key)
	r.Steps = []ReplaceStep{
		{ID: "restore", Op: StepRestore, Risk: core.Change, Database: r.Temp, Key: key, Statement: mssql.RestoreDatabase(r.Temp, arn, mssql.RestoreOptions{}),
			Title: fmt.Sprintf("Restore %s from s3://%s/%s (temporary name)", r.Temp, s.Target.Bucket, key)},
		waitStep("wait", "restore"),
		{ID: "single-user", Op: StepSingleUser, Risk: core.Destructive, Database: db, Statement: mssql.SetSingleUser(db),
			Title: "Close " + db + " to other sessions (disconnects them)"},
		{ID: "rename-old", Op: StepRename, Risk: core.Destructive, Database: db, To: r.Old, Statement: mssql.RenameDatabase(db, r.Old),
			Title: fmt.Sprintf("Rename %s to %s (the current database is kept)", db, r.Old)},
		{ID: "rename-new", Op: StepRename, Risk: core.Change, Database: r.Temp, To: db, Fallback: r.Old, Statement: mssql.RenameDatabase(r.Temp, db),
			Title: fmt.Sprintf("Rename %s to %s (the restored database takes its place)", r.Temp, db)},
		{ID: "multi-user", Op: StepMultiUser, Risk: core.Change, Database: db, Statement: mssql.SetMultiUser(db),
			Title: "Open " + db + " to all sessions again"},
		{ID: "multi-user-old", Op: StepMultiUser, Risk: core.Change, Database: r.Old, Statement: mssql.SetMultiUser(r.Old),
			Title: "Open " + r.Old + " to all sessions again"},
		postStep(db),
	}
	r.Result = fmt.Sprintf("%s replaced: the previous database is kept as %s (drop it with dialga drop %s when it is no longer needed)", db, r.Old, r.Old)
	return nil
}

func (s *Session) dropSteps(ctx context.Context, p *Prepared, r *Replacement, db, key string) error {
	arn := mssql.S3ARN(s.Target.Bucket, key)
	if r.BackupFirst {
		r.BackupKey = fmt.Sprintf("%s/%s_before_replace_%s.bak", s.Target.Prefixes.Export, db, r.Stamp)
		const step, label = "destination", "Safety backup in S3"
		where := "s3://" + s.Target.Bucket + "/" + r.BackupKey
		if err := validate.S3Key(r.BackupKey); err != nil {
			p.fail(step, label, err.Error())
		} else {
			switch _, err := s.Bucket.Size(ctx, r.BackupKey); {
			case errors.Is(err, awsx.ErrObjectNotFound):
				p.ok(step, label, where+" is free")
			case err != nil:
				p.fail(step, label, where+": "+err.Error())
			default:
				p.fail(step, label, where+" already exists: a backup never overwrites, choose another stamp")
			}
		}
		r.Steps = append(r.Steps,
			ReplaceStep{ID: "backup", Op: StepBackup, Risk: core.Change, Database: db, Key: r.BackupKey,
				Statement: mssql.BackupDatabase(db, mssql.S3ARN(s.Target.Bucket, r.BackupKey)),
				Title:     fmt.Sprintf("Back up %s to %s (safety copy before the replace)", db, where)},
			waitStep("wait-backup", "backup"))
	}
	r.Steps = append(r.Steps,
		ReplaceStep{ID: "drop", Op: StepDrop, Risk: core.Destructive, Database: db, Statement: mssql.DropDatabase(db), Title: "Drop database " + db},
		ReplaceStep{ID: "restore", Op: StepRestore, Risk: core.Change, Database: db, Key: key, Statement: mssql.RestoreDatabase(db, arn, mssql.RestoreOptions{}),
			Title: fmt.Sprintf("Restore %s from s3://%s/%s", db, s.Target.Bucket, key)},
		waitStep("wait", "restore"),
		postStep(db))
	if r.BackupFirst {
		r.Result = fmt.Sprintf("%s replaced: the previous database was backed up to s3://%s/%s", db, s.Target.Bucket, r.BackupKey)
	} else {
		r.Result = db + " replaced: the previous database was dropped without a backup"
	}
	return nil
}

func waitStep(id, what string) ReplaceStep {
	return ReplaceStep{ID: id, Op: StepWait, Risk: core.Change, What: what, Title: "Wait for the " + what + " task",
		Statement: "SELECT * FROM msdb.dbo.rds_fn_task_status(NULL, <task id>) until the task ends"}
}

func postStep(db string) ReplaceStep {
	return ReplaceStep{ID: "post", Op: StepPost, Risk: core.Change, Database: db, Title: "Post-restore report of " + db + " (read-only)",
		Statement: "read-only: state, files, login access, orphaned users, Windows users, objects"}
}

// actions are the steps as plan actions, running through p.
func (s *Session) replaceActions(p *Prepared, r *Replacement) []core.Action {
	actions := make([]core.Action, len(r.Steps))
	for i, st := range r.Steps {
		actions[i] = core.Action{ID: st.ID, Title: st.Title, Kind: core.KindSQL, Risk: st.Risk, Statement: st.Statement, Run: s.runStep(p, st)}
	}
	return actions
}

func (s *Session) runStep(p *Prepared, st ReplaceStep) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var detail string
		var err error
		switch st.Op {
		case StepBackup, StepRestore:
			detail, err = p.startTask(st.Statement, st.Database)(ctx)
		case StepWait:
			detail, err = p.followTask(st.What)
		case StepDrop:
			detail, err = s.DropExisting(ctx, st.Database)
			p.dropped = err == nil
		case StepSingleUser:
			detail, err = s.SingleUser(ctx, st.Database)
		case StepRename:
			detail, err = s.Rename(ctx, st.Database, st.To, st.Fallback)
		case StepMultiUser:
			detail, err = s.MultiUser(ctx, st.Database)
		case StepPost:
			detail, err = s.postDetail(ctx, st.Database)
		default:
			err = fmt.Errorf("unknown replace step %q", st.Op)
		}
		if err != nil && p.dropped {
			err = fmt.Errorf("%w (%s was dropped: %s)", err, p.Params.Database, p.Replace.recovery())
		}
		return detail, err
	}
}

// recovery tells what is left to recover the database from after a drop.
func (r *Replacement) recovery() string {
	if r.BackupKey != "" {
		return "its backup is s3://" + r.bucket + "/" + r.BackupKey
	}
	return "there is no backup of it"
}

// followTask waits for the task the last step started, with the options of
// Execute. Unlike the actions it follows ctx: Ctrl-C stops the wait, never
// the task, and the replace stops before its next step.
func (p *Prepared) followTask(what string) (string, error) {
	if p.task == nil {
		return "", errors.New("no native task to follow")
	}
	id := p.task.ID
	task, err := mssql.Wait(p.waitCtx, p.sql, id, mssql.WaitOptions{
		Interval: p.exec.Interval, OnChange: p.exec.OnTask, OnRetry: p.exec.OnRetry,
	})
	if errors.Is(err, context.Canceled) {
		return "", fmt.Errorf("wait interrupted: task %d keeps running on the instance and the replace stops here", id)
	}
	if err != nil {
		return "", err
	}
	p.task = &task
	if task.Lifecycle != mssql.Success {
		msg := fmt.Sprintf("the %s task %d ended in %s", what, id, task.Lifecycle)
		if line := lastLine(task.Info); line != "" {
			msg += ": " + line
		}
		return "", errors.New(msg)
	}
	return fmt.Sprintf("task %d completed", id), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// DropExisting drops db unless it is gone already (a resumed replace).
func (s *Session) DropExisting(ctx context.Context, db string) (string, error) {
	state, err := s.SQL.DatabaseState(ctx, db)
	if err != nil {
		return "", err
	}
	if state == "" {
		return db + " is not there: already dropped", nil
	}
	if err := s.SQL.Exec(ctx, mssql.DropDatabase(db)); err != nil {
		return "", err
	}
	return db + " dropped", nil
}

// SingleUser closes db to every session but one, unless it is gone (renamed
// already, in a resumed swap).
func (s *Session) SingleUser(ctx context.Context, db string) (string, error) {
	state, err := s.SQL.DatabaseState(ctx, db)
	if err != nil {
		return "", err
	}
	if state == "" {
		return db + " is not there: already renamed", nil
	}
	if err := s.SQL.Exec(ctx, mssql.SetSingleUser(db)); err != nil {
		return "", err
	}
	return db + " is SINGLE_USER", nil
}

// MultiUser opens db to every session again.
func (s *Session) MultiUser(ctx context.Context, db string) (string, error) {
	if err := s.SQL.Exec(ctx, mssql.SetMultiUser(db)); err != nil {
		return "", err
	}
	return db + " is MULTI_USER", nil
}

// Rename renames from to to. It looks at the names first, so a resumed swap
// does not repeat a rename that was done: from gone and to there is done, to
// there while from is too is refused. If the rename fails it puts things
// back: from stays where it was, open to every session; with fallback, the
// database of that name is renamed back to to (and opened), which is how a
// swap whose second rename fails gives the old database its name again.
func (s *Session) Rename(ctx context.Context, from, to, fallback string) (string, error) {
	fromState, err := s.SQL.DatabaseState(ctx, from)
	if err != nil {
		return "", err
	}
	toState, err := s.SQL.DatabaseState(ctx, to)
	if err != nil {
		return "", err
	}
	switch {
	case fromState == "" && toState != "":
		return fmt.Sprintf("%s is already named %s", from, to), nil
	case fromState == "":
		return "", fmt.Errorf("neither %s nor %s exists: nothing to rename", from, to)
	case toState != "":
		return "", fmt.Errorf("%s already exists: not renaming %s to it", to, from)
	}
	renameErr := s.SQL.Exec(ctx, mssql.RenameDatabase(from, to))
	if renameErr == nil {
		return fmt.Sprintf("%s renamed to %s", from, to), nil
	}
	var undo []string
	if state, err := s.SQL.DatabaseState(ctx, from); err == nil && state != "" {
		if err := s.SQL.Exec(ctx, mssql.SetMultiUser(from)); err != nil {
			undo = append(undo, fmt.Sprintf("%s could not be opened again: %v", from, err))
		} else {
			undo = append(undo, from+" was left in place and is open to all sessions")
		}
	}
	if fallback != "" {
		toNow, err1 := s.SQL.DatabaseState(ctx, to)
		fbNow, err2 := s.SQL.DatabaseState(ctx, fallback)
		if err1 == nil && err2 == nil && toNow == "" && fbNow != "" {
			if err := s.SQL.Exec(ctx, mssql.RenameDatabase(fallback, to)); err != nil {
				return "", fmt.Errorf("rename %s to %s failed: %w; renaming %s back to %s failed too: %v: "+
					"rename it by hand with EXEC rdsadmin.dbo.rds_modify_db_name", from, to, renameErr, fallback, to, err)
			}
			if err := s.SQL.Exec(ctx, mssql.SetMultiUser(to)); err != nil {
				undo = append(undo, fmt.Sprintf("%s is back as %s but could not be opened again: %v", fallback, to, err))
			} else {
				undo = append(undo, fmt.Sprintf("the previous database %s is back as %s and open; the new copy stays as %s", fallback, to, from))
			}
		}
	}
	if len(undo) == 0 {
		return "", fmt.Errorf("rename %s to %s failed: %w", from, to, renameErr)
	}
	return "", fmt.Errorf("rename %s to %s failed: %w; %s", from, to, renameErr, strings.Join(undo, "; "))
}

// postDetail is the post-restore report of db as the line of a step: a failed
// finding is an error, the warnings are in the line.
func (s *Session) postDetail(ctx context.Context, db string) (string, error) {
	rep, err := s.Post(ctx, db)
	if err != nil {
		return "", err
	}
	var failed, warned []string
	for _, f := range rep.Findings {
		switch f.Status {
		case check.Failed:
			failed = append(failed, f.Label+": "+f.Detail)
		case check.Warning:
			warned = append(warned, f.Label+": "+f.Detail)
		}
	}
	if len(failed) > 0 {
		return "", errors.New(strings.Join(failed, "; "))
	}
	detail := fmt.Sprintf("%s, compatibility level %d", rep.Database.State, rep.Database.CompatibilityLevel)
	if len(warned) > 0 {
		detail += ". " + strings.Join(warned, ". ")
	}
	return detail, nil
}
