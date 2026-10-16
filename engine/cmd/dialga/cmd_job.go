package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/pipeline"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() { commands = append(commands, (*app).importCmd, (*app).runCmd) }

// jobFlags are the flags of the commands that run a job.
type jobFlags struct {
	target      string
	apply       bool
	confirm     string
	skipFailing bool
	interval    time.Duration
}

func (f *jobFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&f.target, "target", "t", "", "target name from the configuration")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "start the job (default: dry run, nothing changes)")
	cmd.Flags().StringVar(&f.confirm, "confirm", "", "the text the plan asks to retype, for non-interactive runs")
	cmd.Flags().BoolVar(&f.skipFailing, "skip-failing", false, "leave out the databases whose pre-flight failed instead of refusing the job")
	cmd.Flags().DurationVar(&f.interval, "interval", 30*time.Second,
		"time between two reads of a native task (RDS updates the status about every 2 minutes)")
}

const jobExitStatus = `
Dry run by default: the pre-flight checks of every database and the plan,
nothing is changed. --apply starts the job and follows it here; a protected
target, or a plan that replaces something, asks to retype a text once (on a
terminal, or with --confirm). Ctrl-C cancels the job: uploads are aborted,
native tasks already running keep running on the instance.

With -o json, stdout gets one JSON document at the end and, with --apply,
stderr gets the job events as JSON lines ({"event":<type>,"data":…}).

Exit status: 0 done, 2 a pre-flight check failed (nothing started), 3 a
native task ended in ERROR or CANCELLED, 1 error, failure or cancel.`

func (a *app) importCmd() *cobra.Command {
	var f jobFlags
	var dbs []string
	var key, prefix string
	var noRecovery, differential, overwrite bool
	cmd := &cobra.Command{
		Use:   "import <file.bak>...",
		Short: "Import backups: upload, restore, wait and post-restore report, as one job",
		Long: `Import backups into the instance of a target, one database per file, as one
job: upload to S3 (multipart, checksum verified), native restore, wait for
the task, read-only post-restore report (not with --norecovery). The
databases go through the steps in parallel, within the limits of the
target (max_tasks native tasks at once).

The database is named after the file (sales.bak -> sales) unless --db
gives the names, one per file in order. A striped backup (several files of
one backup) goes in a plan for "dialga run".
` + jobExitStatus,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(dbs) > 0 && len(dbs) != len(args):
				return fmt.Errorf("--db names one database per file: %d file(s), %d name(s)", len(args), len(dbs))
			case key != "" && len(args) > 1:
				return errors.New("--key names the object of one file: use --prefix for several")
			}
			req := pipeline.Request{Kind: jobs.Import, SkipFailing: f.skipFailing}
			if cmd.Flags().Changed("prefix") {
				req.Prefix = &prefix
			}
			for i, file := range args {
				path, err := filepath.Abs(file)
				if err != nil {
					return err
				}
				db := ""
				if len(dbs) > 0 {
					db = dbs[i]
				} else if db, err = databaseOf(path); err != nil {
					return err
				}
				req.Import = append(req.Import, pipeline.ImportItem{Files: []string{path}, Database: db, Key: key,
					Differential: differential, NoRecovery: noRecovery, Overwrite: overwrite})
			}
			return a.runJob(cmd, f, req)
		},
	}
	f.register(cmd)
	cmd.Flags().StringSliceVar(&dbs, "db", nil, "database names, one per file in order (default: the file names)")
	cmd.Flags().StringVar(&key, "key", "", "S3 key of the upload (one file only)")
	cmd.Flags().StringVar(&prefix, "prefix", "", `key prefix instead of the import prefix of the target ("" is the bucket root)`)
	cmd.Flags().BoolVar(&noRecovery, "norecovery", false, "leave the databases RESTORING (a differential follows): no post-restore report")
	cmd.Flags().BoolVar(&differential, "differential", false, "differential backups, onto databases left RESTORING")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace the objects that already exist in S3")
	return cmd
}

// databaseOf names a database after its backup file: sales.bak -> sales.
func databaseOf(path string) (string, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if err := validate.DBName(name); err != nil {
		return "", fmt.Errorf("cannot name the database after %s (%v): pass --db", filepath.Base(path), err)
	}
	return name, nil
}

func (a *app) runCmd() *cobra.Command {
	var f jobFlags
	cmd := &cobra.Command{
		Use:   "run <plan.yaml>",
		Short: "Run an import or an export of several databases from a plan file",
		Long: `Run an import or an export of several databases described by a plan file
(apiVersion v1, kind Import or Export, target, items: docs/piani-job.md).
Relative paths in the plan are relative to its folder. -t gives the target
when the plan names none.
` + jobExitStatus,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := pipeline.ReadFile(args[0])
			if err != nil {
				return err
			}
			switch {
			case plan.Target != "" && f.target != "" && plan.Target != f.target:
				return fmt.Errorf("the plan is for target %s, -t says %s", plan.Target, f.target)
			case plan.Target != "":
				f.target = plan.Target
			}
			req := plan.Request()
			req.SkipFailing = req.SkipFailing || f.skipFailing
			return a.runJob(cmd, f, req)
		},
	}
	f.register(cmd)
	return cmd
}

// jobDoc is the JSON document of import and run.
type jobDoc struct {
	Target  string         `json:"target"`
	Applied bool           `json:"applied"`
	Ready   bool           `json:"ready"`
	Plan    *pipeline.Plan `json:"plan"`
	Job     *jobs.Job      `json:"job,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// runJob plans the job, prints the dry run or starts it and follows it to
// the end in this process (the jobs of the CLI are not kept in the journal
// of the desktop app).
func (a *app) runJob(cmd *cobra.Command, f jobFlags, req pipeline.Request) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	if f.interval <= 0 {
		return fmt.Errorf("--interval must be positive (got %s)", f.interval)
	}
	name, t, err := a.target(f.target)
	if err != nil {
		return err
	}
	if err := req.Validate(t); err != nil {
		return err
	}
	password, err := a.sqlPassword(t)
	if err != nil {
		return fmt.Errorf("SQL password: %w", err)
	}
	client, err := a.openS3(ctx, t)
	if err != nil {
		return err
	}
	env := pipeline.Env{
		Name: name, Target: t, S3: client, TaskInterval: f.interval,
		OpenOps: func(ctx context.Context) (*ops.Session, func(), error) { return a.openOps(ctx, name, t, password) },
	}
	plan, err := pipeline.Build(ctx, env, req)
	if err != nil {
		return err
	}
	text := a.output != "json"
	doc := jobDoc{Target: name, Ready: plan.Ready, Plan: plan}
	finish := func(code int) error {
		if !text {
			if err := json.NewEncoder(out).Encode(doc); err != nil {
				return err
			}
		}
		if code != 0 {
			return exitError{code}
		}
		return nil
	}
	switch {
	case text && !f.apply:
		_, _ = io.WriteString(out, plan.Preview)
	case text:
		printJobChecks(out, plan, (&checkReport{color: a.stdoutIsTerminal}).tag)
	}
	switch {
	case !f.apply && plan.Ready:
		if text {
			_, _ = fmt.Fprintln(out, "Run again with --apply to start the job.")
		}
		return finish(0)
	case !f.apply:
		return finish(exitCheckFailed)
	case !plan.Ready:
		if text {
			_, _ = fmt.Fprintln(out, "Nothing was started.")
		}
		return finish(exitCheckFailed)
	}
	confirm, err := a.confirmation(plan.Core(), f.confirm)
	if err != nil {
		return err
	}
	m := jobs.New(jobs.Options{})
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(closeCtx)
	}()
	job, err := plan.Start(m, confirm)
	if err != nil {
		return err
	}
	doc.Applied = true
	r := &jobReport{json: !text, out: out, errOut: cmd.ErrOrStderr(), tty: isTerminal(cmd.ErrOrStderr()), shown: map[string]int{}}
	if text {
		_, _ = fmt.Fprintf(out, "\nJob %s: %s of %d database(s) on %s. Ctrl-C cancels it: uploads are aborted, "+
			"native tasks already running keep running on the instance.\n", job.ID, job.Kind, len(job.Items), name)
	}
	final, err := followJob(ctx, m, job.ID, r.event)
	r.clearProgress()
	if err != nil {
		doc.Error = err.Error()
		_ = finish(0)
		return err
	}
	doc.Job = &final
	if text {
		printJobEnd(out, final)
	}
	return finish(jobExit(final))
}

// printJobChecks prints the pre-flight checks of every database, before the
// job starts (the dry run prints the whole plan).
func printJobChecks(w io.Writer, plan *pipeline.Plan, tag func(check.Status) string) {
	_, _ = fmt.Fprintf(w, "Pre-flight checks of the %s of %d database(s) on %s (instance %s):\n",
		plan.Kind, len(plan.Items), plan.Target, plan.Instance)
	for _, wrn := range plan.Warnings {
		_, _ = fmt.Fprintf(w, "  %s  %s\n", tag(check.Warning), wrn)
	}
	for _, it := range plan.Items {
		note := ""
		if it.Skipped {
			note = "  [skipped]"
		}
		_, _ = fmt.Fprintf(w, "%s%s\n", it.Database, note)
		for _, c := range it.Checks {
			_, _ = fmt.Fprintf(w, "  %s  %-20s %s\n", tag(c.Status), c.Label, c.Detail)
		}
	}
}

// stopTimeout bounds the wait for a cancelled job to stop (an upload aborts
// within 30 seconds).
const stopTimeout = 2 * time.Minute

// followJob passes the events of the job to show until it ends. When ctx
// ends (Ctrl-C, SIGTERM) it cancels the job, and shows how it stops.
func followJob(ctx context.Context, m *jobs.Manager, id string, show func(jobs.Event)) (jobs.Job, error) {
	done := ctx.Done()
	var stopped <-chan time.Time
	var last int64
	for {
		sub, err := m.Subscribe(id, last)
		if err != nil {
			return jobs.Job{}, err
		}
		for _, ev := range sub.Replay {
			show(ev)
			last = ev.Seq
		}
	events:
		for {
			select {
			case ev, open := <-sub.C:
				if !open {
					break events
				}
				show(ev)
				last = ev.Seq
			case <-done:
				done = nil
				stopped = time.After(stopTimeout)
				if _, err := m.Cancel(id); err != nil && !errors.Is(err, jobs.ErrEnded) {
					sub.Close()
					return jobs.Job{}, err
				}
			case <-stopped:
				sub.Close()
				return jobs.Job{}, fmt.Errorf("job %s did not stop within %s", id, stopTimeout)
			}
		}
		lagged := sub.Lagged()
		sub.Close()
		if !lagged {
			return m.Get(id)
		}
	}
}

// jobExit is the exit status of an ended job.
func jobExit(job jobs.Job) int {
	if job.State == jobs.Succeeded {
		return 0
	}
	for _, it := range job.Items {
		if it.State == jobs.Failed && it.Task != nil && (it.Task.Lifecycle == mssql.Error || it.Task.Lifecycle == mssql.Cancelled) {
			return exitTaskFailed
		}
	}
	return exitFailure
}

func printJobEnd(w io.Writer, job jobs.Job) {
	msg := ""
	if n := len(job.Events); n > 0 {
		msg = job.Events[n-1].Message
	}
	_, _ = fmt.Fprintf(w, "Job %s %s: %s\n", job.ID, job.State, msg)
	for _, it := range job.Items {
		line := it.Result
		switch {
		case it.Error != "":
			line = string(it.State) + " at " + it.Step + ": " + it.Error
		case line == "":
			line = string(it.State)
		}
		_, _ = fmt.Fprintf(w, "  %-20s %s\n", it.Name, line)
	}
}

// jobReport prints the events of a job: one line per step, task change and
// log (stdout), the transfer progress on stderr; or JSON lines on stderr.
type jobReport struct {
	json   bool
	out    io.Writer
	errOut io.Writer
	tty    bool

	mu    sync.Mutex
	dirty bool           // a progress line is on the terminal
	shown map[string]int // last tenth printed per item, without a terminal
}

var stepTags = map[jobs.EventType]string{
	jobs.EventStepWaiting: "WAIT", jobs.EventStepStarted: "RUN ", jobs.EventStepSucceeded: "OK  ",
	jobs.EventStepFailed: "FAIL", jobs.EventStepSkipped: "SKIP", jobs.EventStepCancelled: "STOP",
	jobs.EventStepInterrupted: "STOP",
}

func (r *jobReport) event(ev jobs.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.json {
		_ = json.NewEncoder(r.errOut).Encode(struct {
			Event jobs.EventType `json:"event"`
			Data  jobs.Event     `json:"data"`
		}{ev.Type, ev})
		return
	}
	if ev.Type == jobs.EventUploadProgress {
		r.progress(ev)
		return
	}
	r.clearProgressLocked()
	at := ev.Time.Local().Format("15:04:05")
	switch {
	case stepTags[ev.Type] != "":
		_, _ = fmt.Fprintf(r.out, "  [%s] %-16s %-9s %s  %s\n", at, ev.Item, ev.Step, stepTags[ev.Type], ev.Message)
	case ev.Type == jobs.EventTaskStatus && ev.Task != nil:
		_, _ = fmt.Fprintf(r.out, "  [%s] %-16s %-9s task %d %s %d%%\n", at, ev.Item, ev.Step, ev.Task.ID, ev.Task.Lifecycle, ev.Task.Percent)
	case ev.Type == jobs.EventLog:
		_, _ = fmt.Fprintf(r.out, "  [%s] %-16s %-9s %s  %s\n", at, ev.Item, ev.Step, strings.ToUpper(string(ev.Level)), ev.Message)
	}
}

func (r *jobReport) progress(ev jobs.Event) {
	p := ev.Transfer
	if p == nil {
		return
	}
	pct := 100
	if p.Total > 0 {
		pct = int(p.Bytes * 100 / p.Total)
	}
	line := fmt.Sprintf("  %s  %s %3d%%  %s of %s", ev.Item, p.Direction, pct, transfer.FormatBytes(p.Bytes), transfer.FormatBytes(p.Total))
	if p.BytesPerSecond > 0 {
		line += fmt.Sprintf("  %s/s", transfer.FormatBytes(p.BytesPerSecond))
	}
	if p.ETASeconds > 0 {
		line += "  ETA " + (time.Duration(p.ETASeconds) * time.Second).String()
	}
	if r.tty {
		_, _ = fmt.Fprintf(r.errOut, "\r\x1b[K%s", line)
		r.dirty = true
		return
	}
	// Without a terminal: a line every 10% per database.
	key := ev.Item + "/" + string(p.Direction)
	if tenth, seen := r.shown[key]; !seen || pct/10 > tenth {
		r.shown[key] = pct / 10
		_, _ = fmt.Fprintln(r.errOut, line)
	}
}

func (r *jobReport) clearProgress() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearProgressLocked()
}

func (r *jobReport) clearProgressLocked() {
	if r.dirty {
		_, _ = fmt.Fprint(r.errOut, "\r\x1b[K")
		r.dirty = false
	}
}
