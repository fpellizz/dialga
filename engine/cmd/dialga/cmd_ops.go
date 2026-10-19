package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() {
	commands = append(commands, (*app).restoreCmd, (*app).exportCmd, (*app).finishCmd,
		(*app).cancelCmd, (*app).dropCmd, (*app).postCmd)
}

// opFlags are the flags of the operations that change something.
type opFlags struct {
	target   string
	apply    bool
	confirm  string
	noWait   bool
	interval time.Duration
}

const opExitStatus = `
Dry run by default: the pre-flight checks and the plan, nothing is changed.
--apply runs the plan; a protected target asks to retype its name, a drop the
database name (on a terminal, or with --confirm).

Exit status: 0 OK, 2 a pre-flight check failed (nothing ran), 3 the native
task ended in ERROR or CANCELLED, 1 error.`

func (f *opFlags) register(cmd *cobra.Command, follows bool) {
	cmd.Flags().StringVarP(&f.target, "target", "t", "", "target name from the configuration")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "run the plan (default: dry run)")
	cmd.Flags().StringVar(&f.confirm, "confirm", "", "the text the plan asks to retype, for non-interactive runs")
	f.interval = 30 * time.Second
	if follows {
		cmd.Flags().BoolVar(&f.noWait, "no-wait", false, "start the native task and return without following it")
		cmd.Flags().DurationVar(&f.interval, "interval", 30*time.Second,
			"time between two reads of the task (RDS updates the status about every 2 minutes)")
	}
}

func (a *app) restoreCmd() *cobra.Command {
	var f opFlags
	var o mssql.RestoreOptions
	var replace string
	var noBackup bool
	cmd := &cobra.Command{
		Use:   "restore <db> <key>",
		Short: "Restore a .bak from the target bucket into a database (rds_restore_database)",
		Long: `Restore a native backup from the target bucket into a new database with
rds_restore_database, then follow the task until it ends.

<key> is an S3 key of the target bucket: a bare file name is looked for under
the import prefix (then at the bucket root), a key with "/" is taken as it is,
s3://<bucket>/<key> must name the target bucket. A "*" restores a backup
striped over several files (10 at most).

RDS refuses a restore over an existing database, so by default so does
dialga. --replace restores onto it anyway:
  swap  restore beside it under a temporary name, rename the current database
        to <db>_old_<stamp> (kept) and the new one to <db>
  drop  back the current database up to S3 (unless --no-backup-first), drop it
        and restore in its place
  auto  swap when two copies fit in the free storage, else drop with the backup
Either way the plan shows the exact T-SQL, and --apply asks to retype the
database name. The renames use rdsadmin.dbo.rds_modify_db_name.
` + opExitStatus,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ops.Params{Database: args[0], Key: args[1], Restore: o, Replace: ops.ReplaceOptions{Mode: replace}}
			if noBackup {
				no := false
				p.Replace.BackupFirst = &no
			}
			return a.runOp(cmd, ops.OpRestore, p, f)
		},
	}
	f.register(cmd, true)
	cmd.Flags().BoolVar(&o.Differential, "differential", false,
		"restore a differential backup on a database left RESTORING (@type = DIFFERENTIAL)")
	cmd.Flags().BoolVar(&o.NoRecovery, "norecovery", false,
		"leave the database RESTORING for a differential restore (@with_norecovery = 1); dialga finish ends it")
	cmd.Flags().StringVar(&replace, "replace", "", "restore onto an existing database: auto, swap or drop")
	cmd.Flags().BoolVar(&noBackup, "no-backup-first", false, "with --replace drop: do not back the current database up to S3 first")
	return cmd
}

func (a *app) exportCmd() *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use:   "export <db> [key]",
		Short: "Back up a database to the target bucket (rds_backup_database)",
		Long: `Back up a database to the target bucket with rds_backup_database, then follow
the task until it ends. It never overwrites an object.

[key] defaults to <export prefix>/<db>_<YYYYMMDD-HHMMSS>.bak (UTC); a bare file
name goes under the export prefix.
` + opExitStatus,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ops.Params{Database: args[0]}
			if len(args) == 2 {
				p.Key = args[1]
			}
			return a.runOp(cmd, ops.OpExport, p, f)
		},
	}
	f.register(cmd, true)
	return cmd
}

func (a *app) finishCmd() *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use:   "finish <db>",
		Short: "Bring online a database left RESTORING by restore --norecovery (rds_finish_restore)",
		Long:  "Bring online a database left RESTORING by restore --norecovery, with rds_finish_restore.\n" + opExitStatus,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOp(cmd, ops.OpFinish, ops.Params{Database: args[0]}, f)
		},
	}
	f.register(cmd, false)
	return cmd
}

func (a *app) cancelCmd() *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use:   "cancel <task-id>",
		Short: "Cancel a native backup/restore task that has not ended (rds_cancel_task)",
		Long:  "Cancel a native backup/restore task that has not ended, with rds_cancel_task.\n" + opExitStatus,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil || id < 1 {
				return fmt.Errorf("invalid task id %q", args[0])
			}
			return a.runOp(cmd, ops.OpCancel, ops.Params{TaskID: id}, f)
		},
	}
	f.register(cmd, false)
	return cmd
}

func (a *app) dropCmd() *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use:   "drop <db>",
		Short: "Drop a database for good (rds_drop_database)",
		Long: `Drop a database with rds_drop_database (a failed restore, a test copy). It
cannot be undone: --apply asks to retype the database name.
` + opExitStatus,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOp(cmd, ops.OpDrop, ops.Params{Database: args[0]}, f)
		},
	}
	f.register(cmd, false)
	return cmd
}

// opResult is the JSON document of an operation.
type opResult struct {
	Target       string        `json:"target"`
	Instance     string        `json:"instance"`
	Operation    string        `json:"operation"`
	Ready        bool          `json:"ready"`
	Checks       []check.Event `json:"checks"`
	Plan         core.Plan     `json:"plan"`
	Digest       string        `json:"digest"`
	Confirmation string        `json:"confirmation,omitempty"`
	Applied      bool          `json:"applied"`
	Steps        []core.Event  `json:"steps,omitempty"`
	Task         *mssql.Task   `json:"task,omitempty"`
	Error        string        `json:"error,omitempty"`
	// Result is what a replace kept: the old database or the safety backup.
	Result string `json:"result,omitempty"`
	// JobID is the job the operation ran as: dialga jobs show <id>, and the
	// Activity page of the app.
	JobID string `json:"jobId,omitempty"`
	// GeneratedPassword is the password --generate-password made for the new
	// login, once the login exists; never in a dry run.
	GeneratedPassword string `json:"generatedPassword,omitempty"`
}

// runOp prepares op, prints the pre-flight checks and either the preview
// (dry run) or, with --apply and the confirmation, runs it as a job of the
// journal (origin cli): the actions of the plan are its steps.
func (a *app) runOp(cmd *cobra.Command, op string, p ops.Params, f opFlags) error {
	return a.runOpWith(cmd, op, p, f, loginPassword{})
}

// loginPassword is the password of the login a grant creates: given by the
// user or generated (then it is shown once, after the login exists). It
// never reaches a plan, a preview or the journal.
type loginPassword struct {
	value     string
	generated bool
}

// runOpWith is runOp for an operation that needs a password to execute.
func (a *app) runOpWith(cmd *cobra.Command, op string, p ops.Params, f opFlags, pw loginPassword) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	if f.interval <= 0 {
		return fmt.Errorf("--interval must be positive (got %s)", f.interval)
	}
	name, t, err := a.target(f.target)
	if err != nil {
		return err
	}
	if err := ops.Validate(op, p, t); err != nil {
		return err
	}
	if f.apply && p.CreateLogin {
		if err := ops.CheckLoginPassword(pw.value); err != nil {
			return err
		}
	}
	password, err := a.sqlPassword(name, t)
	if err != nil {
		return fmt.Errorf("SQL password: %w", err)
	}
	// One tunnel for the whole run in tunnel mode: the pre-flight and every
	// step of the job go through it.
	openOps, closeAll, err := a.jobOpenOps(ctx, name, t, password)
	if err != nil {
		return err
	}
	defer closeAll()
	s, closeOps, err := openOps(ctx)
	if err != nil {
		return err
	}
	defer closeOps()
	prep, err := s.Build(ctx, op, p)
	if err != nil {
		return err
	}
	res := opResult{
		Target: name, Instance: t.Instance, Operation: op, Ready: prep.Ready(), Checks: prep.Checks,
		Plan: prep.Plan, Digest: prep.Digest(), Confirmation: prep.Plan.Confirmation(),
	}
	text := a.output != "json"
	tag := (&checkReport{color: a.stdoutIsTerminal}).tag
	if text {
		_, _ = fmt.Fprintf(out, "Pre-flight checks of %s on %s (instance %s):\n", op, name, t.Instance)
		for _, c := range prep.Checks {
			_, _ = fmt.Fprintf(out, "  %s  %-22s %s\n", tag(c.Status), c.Label, c.Detail)
		}
	}
	finish := func(code int) error {
		if !text {
			if err := json.NewEncoder(out).Encode(res); err != nil {
				return err
			}
		}
		if code != 0 {
			return exitError{code}
		}
		return nil
	}

	if !f.apply {
		if text {
			if err := prep.Plan.Preview(out); err != nil {
				return err
			}
			if prep.Ready() {
				_, _ = fmt.Fprintln(out, "Rerun with --apply to run it.")
			} else {
				_, _ = fmt.Fprintln(out, "Some pre-flight checks failed: the plan cannot run.")
			}
		}
		if !prep.Ready() {
			return finish(exitCheckFailed)
		}
		return finish(0)
	}
	if !prep.Ready() {
		if text {
			_, _ = fmt.Fprintln(out, "Some pre-flight checks failed: nothing was changed.")
		}
		var failed []string
		for _, c := range prep.Checks {
			if c.Status == check.Failed {
				failed = append(failed, c.Label+": "+c.Detail)
			}
		}
		a.recordRefusal(cmd.ErrOrStderr(), refusal{kind: jobs.Operation, operation: op, target: name, instance: t.Instance,
			items: []string{pipeline.OperationItem(prep.Params)}, status: http412,
			reason: "pre-flight failed, nothing was changed: " + strings.Join(failed, "; "), checks: nonOKChecks(prep.Checks)})
		return finish(exitCheckFailed)
	}
	if _, err := a.confirmation(prep.Plan, f.confirm); err != nil {
		if errors.Is(err, core.ErrNotConfirmed) {
			a.recordRefusal(cmd.ErrOrStderr(), refusal{kind: jobs.Operation, operation: op, target: name, instance: t.Instance,
				items: []string{pipeline.OperationItem(prep.Params)}, status: http422, reason: err.Error()})
		}
		return err
	}

	if text {
		_, _ = fmt.Fprintf(out, "Running %s on %s (instance %s):\n", op, name, t.Instance)
	}
	res.Applied = true
	var lastTask atomic.Pointer[mssql.Task]
	env := pipeline.Env{Name: name, Target: t, TaskInterval: f.interval, OpenOps: openOps, LoginPassword: pw.value,
		OnTask: func(task mssql.Task) { lastTask.Store(&task) }}
	spec, err := pipeline.OperationSpec(env, prep, f.noWait)
	if err != nil {
		return err
	}
	m := openCLIJobs(cmd.ErrOrStderr())
	defer closeJobs(m)
	job, err := m.Start(spec)
	if err != nil {
		return err
	}
	res.JobID = job.ID
	if text {
		_, _ = fmt.Fprintf(out, "Job %s (dialga jobs show %s). Ctrl-C cancels it: a native task already running keeps running.\n", job.ID, job.ID)
	}
	titles := map[string]string{}
	for _, st := range job.Items[0].Steps {
		titles[st.ID] = st.Title
	}
	toStep := pipeline.OperationEvents(titles)
	onStep := func(e core.Event) {
		if e.Status != core.Running {
			res.Steps = append(res.Steps, e)
		}
		if !text {
			return
		}
		switch {
		case strings.HasPrefix(e.Step, "wait") && e.Status == core.Running:
			_, _ = fmt.Fprintf(out, "Following the task every %s (Ctrl-C stops the wait, not the task).\n", f.interval)
		case e.Status != core.Running && e.Detail != "":
			_, _ = fmt.Fprintf(out, "  %s  %s: %s\n", tag(check.Status(e.Status)), e.Label, e.Detail)
		case e.Status != core.Running:
			_, _ = fmt.Fprintf(out, "  %s  %s\n", tag(check.Status(e.Status)), e.Label)
		}
	}
	// A generated password is shown once, as soon as the login exists: if a
	// later step fails it would be lost otherwise.
	reveal := func() {
		created := slices.ContainsFunc(res.Steps, func(e core.Event) bool { return e.Step == ops.AccCreateLogin && e.Status == core.OK })
		if !pw.generated || !created || res.GeneratedPassword != "" {
			return
		}
		res.GeneratedPassword = pw.value
		if text {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Password of login %s (shown once, not kept anywhere): %s\n", p.Login, pw.value)
		}
	}
	final, err := followJob(ctx, m, job.ID, func(ev jobs.Event) {
		switch e, ok := toStep(ev); {
		case ok:
			onStep(e)
		case ev.Type == jobs.EventTaskStatus && ev.Task != nil && strings.HasPrefix(ev.Step, "wait") && text:
			_, _ = fmt.Fprintf(out, "  [%s] task %d: %s %d%%\n", ev.Time.Local().Format("15:04:05"), ev.Task.ID, ev.Task.Lifecycle, ev.Task.Percent)
		}
	})
	if err != nil {
		reveal()
		res.Error = err.Error()
		if !text {
			_ = json.NewEncoder(out).Encode(res)
		}
		return err
	}
	reveal()
	item := final.Items[0]
	task := lastTask.Load()
	if task == nil {
		task = pipeline.OperationTask(item)
	}
	if final.State == jobs.Succeeded {
		res.Task, res.Result = task, prep.Result()
	} else {
		res.Task = task
	}
	switch {
	case final.State == jobs.Succeeded:
	case task != nil && !f.noWait && task.Lifecycle.Done() && task.Lifecycle != mssql.Success:
		// The task ended in ERROR or CANCELLED: the job failed, the exit
		// status is that of the task.
		if text {
			printOpEnd(out, name, op, p, task, f.noWait)
		}
		return finish(exitTaskFailed)
	default:
		err := errors.New(cmp.Or(item.Error, "job "+string(final.State)))
		if final.State == jobs.Cancelled && task != nil {
			err = fmt.Errorf("interrupted: job %s cancelled, task %d keeps running on the instance", final.ID, task.ID)
		}
		res.Error = err.Error()
		if !text {
			_ = json.NewEncoder(out).Encode(res)
		}
		return err
	}
	if text {
		printOpEnd(out, name, op, p, task, f.noWait)
		if r := prep.Result(); r != "" {
			_, _ = fmt.Fprintln(out, r+".")
		}
	}
	return finish(0)
}

func printOpEnd(w io.Writer, target, op string, p ops.Params, task *mssql.Task, noWait bool) {
	switch {
	case task == nil:
		_, _ = fmt.Fprintln(w, "Done.")
	case p.Replace.Mode != ops.ReplaceNone && op == ops.OpRestore && task.Lifecycle == mssql.Success:
		printTaskEnd(w, *task) // a replace has its post-restore report in its steps
	case noWait:
		_, _ = fmt.Fprintf(w, "Follow it with: dialga wait %d -t %s\n", task.ID, target)
	default:
		printTaskEnd(w, *task)
		switch {
		case task.Lifecycle != mssql.Success || op != ops.OpRestore:
		case p.Restore.NoRecovery:
			_, _ = fmt.Fprintf(w, "%s stays RESTORING: restore a differential backup with --differential, or bring it online with: dialga finish %s -t %s\n",
				p.Database, p.Database, target)
		default:
			_, _ = fmt.Fprintf(w, "Next: dialga post %s -t %s\n", p.Database, target)
		}
	}
}

// confirmation returns the text to pass to Execute: --confirm when given,
// otherwise what the user retypes on the terminal. A plan that asks for
// nothing ignores --confirm.
func (a *app) confirmation(plan core.Plan, given string) (string, error) {
	want := plan.Confirmation()
	if want == "" {
		return "", nil
	}
	if given == "" {
		what := fmt.Sprintf("Retype %q to run %s on %s: ", want, plan.Operation, plan.Target)
		if plan.Subject != "" && want == plan.Subject && plan.Operation == ops.OpRestore {
			what = fmt.Sprintf("This replaces database %s on %s: the current one is renamed or dropped. Retype the database name to confirm: ", want, plan.Target)
		} else if plan.Subject != "" && want == plan.Subject {
			what = fmt.Sprintf("This drops %s on %s for good. Retype the database name to confirm: ", want, plan.Target)
		}
		answer, err := a.readConfirm(what)
		if err != nil {
			return "", fmt.Errorf("this plan asks to retype %q: run it on a terminal or pass --confirm (%w)", want, err)
		}
		given = answer
	}
	if given != want {
		return "", fmt.Errorf("%w (expected %q): nothing was changed", core.ErrNotConfirmed, want)
	}
	return given, nil
}

func (a *app) postCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "post <db>",
		Short: "Report on a restored database: state, files, login access, orphaned users, objects",
		Long: `Report on a restored database, read-only: properties, files, whether the SQL
login can enter it, SQL users without a login (orphaned), Windows users and the
objects by type.

Exit status: 0 OK (warnings included), 2 the database is missing or not
online, or the login cannot enter it, 1 error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, t, err := a.target(target)
			if err != nil {
				return err
			}
			if err := validate.DBName(args[0]); err != nil {
				return err
			}
			s, closeOps, err := a.openTarget(cmd.Context(), name, t)
			if err != nil {
				return err
			}
			defer closeOps()
			r, err := s.Post(cmd.Context(), args[0])
			if errors.Is(err, mssql.ErrNoDatabase) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: %v\n", err)
				return exitError{exitCheckFailed}
			}
			if err != nil {
				return err
			}
			if a.output == "json" {
				err = json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			} else {
				err = printReport(cmd.OutOrStdout(), r, (&checkReport{color: a.stdoutIsTerminal}).tag)
			}
			if err != nil {
				return err
			}
			if !r.OK {
				return exitError{exitCheckFailed}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

func printReport(w io.Writer, r ops.Report, tag func(check.Status) string) error {
	d := r.Database
	_, _ = fmt.Fprintf(w, "Database %s on %s (instance %s)\n", d.Name, r.Target, r.Instance)
	mode := "read-write"
	if d.ReadOnly {
		mode = "read-only"
	}
	_, _ = fmt.Fprintf(w, "  state %s, recovery %s, compatibility level %d, collation %s\n",
		d.State, d.RecoveryModel, d.CompatibilityLevel, orDash(d.Collation))
	_, _ = fmt.Fprintf(w, "  owner %s, access %s, %s\n", orDash(d.Owner), d.UserAccess, mode)
	_, _ = fmt.Fprintln(w, "Files:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tNAME\tSIZE MB\tMAX MB\tPHYSICAL NAME")
	for _, f := range r.Files {
		maxMB := "unlimited"
		if f.MaxSizeMB != nil {
			maxMB = strconv.FormatFloat(*f.MaxSizeMB, 'f', 1, 64)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%.1f\t%s\t%s\n", f.Type, f.Name, f.SizeMB, maxMB, f.PhysicalName)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "Checks:")
	for _, f := range r.Findings {
		_, _ = fmt.Fprintf(w, "  %s  %-16s %s\n", tag(f.Status), f.Label, f.Detail)
	}
	if !r.Access {
		return nil
	}
	_, _ = fmt.Fprintln(w, "Objects:")
	if len(r.Objects) == 0 {
		_, err := fmt.Fprintln(w, "  no user objects")
		return err
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tCOUNT")
	for _, o := range r.Objects {
		_, _ = fmt.Fprintf(tw, "  %s\t%d\n", o.Type, o.Count)
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// target resolves the target of an operation from the configuration.
func (a *app) target(name string) (string, config.Target, error) {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return "", config.Target{}, err
	}
	name, t, err := cfg.Target(name)
	if err != nil {
		return "", config.Target{}, err
	}
	return name, t, nil
}

// openTarget opens the target for the operations, asking for the SQL
// password on the terminal when the configuration says so.
func (a *app) openTarget(ctx context.Context, name string, t config.Target) (*ops.Session, func(), error) {
	password, err := a.sqlPassword(name, t)
	if err != nil {
		return nil, nil, fmt.Errorf("SQL password: %w", err)
	}
	return a.openOps(ctx, name, t, password)
}

// readLineFromTerminal asks for a line of text on the terminal (stderr for
// the prompt, so stdout stays clean).
func readLineFromTerminal(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("stdin is not a terminal")
	}
	_, _ = fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
