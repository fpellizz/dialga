package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/jobs"
)

func init() { commands = append(commands, (*app).jobsCmd) }

func (a *app) jobsCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List the import/export jobs, newest first",
		Long: `List the import/export jobs of the journal, the running ones and the history
(the last 200, 30 days at most), newest first. Read-only: it reads the journal
files directly, with or without the desktop app running.

Journal: $XDG_STATE_HOME/dialga/jobs, or ~/.local/state/dialga/jobs (Linux);
<user config dir>/dialga/jobs elsewhere.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, list, err := readJobs(dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if a.output == "json" {
				sums := make([]jobs.Summary, 0, len(list))
				for _, j := range list {
					sums = append(sums, j.Summary())
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Journal string         `json:"journal"`
					Jobs    []jobs.Summary `json:"jobs"`
				}{d, sums})
			}
			return printJobs(cmd.OutOrStdout(), d, list)
		},
	}
	cmd.PersistentFlags().StringVar(&dir, "dir", "", "journal directory (default: the engine's)")
	cmd.AddCommand(a.jobsShowCmd(&dir))
	return cmd
}

func (a *app) jobsShowCmd(dir *string) *cobra.Command {
	var events int
	cmd := &cobra.Command{
		Use:   "show <job-id>",
		Short: "Show a job: its databases, their steps and the last events",
		Long: `Show a job of the journal: its databases with their steps, progress, native
tasks and what a resume needs, then the last events. Read-only. JSON prints the
whole job, every logged event included.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !jobs.ValidID(id) {
				return fmt.Errorf("invalid job id %q: dialga jobs lists them", id)
			}
			if events < 0 {
				return fmt.Errorf("--events must be 0 (all) or more (got %d)", events)
			}
			d, list, err := readJobs(*dir, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			i := slices.IndexFunc(list, func(j jobs.Job) bool { return j.ID == id })
			if i < 0 {
				return fmt.Errorf("job %s not found in %s (removed after 30 days or 200 jobs)", id, d)
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(list[i])
			}
			return printJob(cmd.OutOrStdout(), list[i], events)
		},
	}
	cmd.Flags().IntVar(&events, "events", 20, "number of last events to print (0: all)")
	return cmd
}

// readJobs reads the journal in dir (default jobs.DefaultDir), newest
// first; unreadable files are reported on errOut and skipped.
func readJobs(dir string, errOut io.Writer) (string, []jobs.Job, error) {
	if dir == "" {
		d, err := jobs.DefaultDir()
		if err != nil {
			return "", nil, err
		}
		dir = d
	}
	list, errs := jobs.ReadJournal(dir)
	for _, err := range errs {
		_, _ = fmt.Fprintf(errOut, "dialga: journal file skipped: %v\n", err)
	}
	slices.SortFunc(list, func(a, b jobs.Job) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return dir, list, nil
}

func printJobs(w io.Writer, dir string, list []jobs.Job) error {
	if len(list) == 0 {
		_, err := fmt.Fprintf(w, "No jobs in %s yet: the imports and exports of the desktop app appear here.\n", dir)
		return err
	}
	_, _ = fmt.Fprintf(w, "Jobs in %s, newest first:\n", dir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  ID\tSTATE\tKIND\tTARGET\tDATABASES\tCREATED\tENDED")
	unfinished := false
	for _, j := range list {
		s := j.Summary()
		dbs := fmt.Sprintf("%d/%d ok", s.Succeeded, len(s.Databases))
		if s.Failed > 0 {
			dbs += fmt.Sprintf(", %d failed", s.Failed)
		}
		state := string(s.State)
		if s.CancelRequested && !s.State.Ended() {
			state += " (cancelling)"
		}
		unfinished = unfinished || !s.State.Ended()
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, state, s.Kind, s.Target, dbs,
			localTime(&s.CreatedAt, time.DateTime[:16]), localTime(s.EndedAt, time.DateTime[:16]))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if unfinished {
		_, _ = fmt.Fprintln(w, "Queued or running jobs were interrupted if no Dialga engine is running: the next one marks them.")
	}
	_, err := fmt.Fprintln(w, "Detail of a job: dialga jobs show <id>")
	return err
}

func printJob(w io.Writer, j jobs.Job, last int) error {
	_, _ = fmt.Fprintf(w, "Job %s: %s on %s (instance %s)\n", j.ID, j.Kind, j.Target, j.Instance)
	state := string(j.State)
	switch {
	case j.State == jobs.Interrupted:
		state += " (the engine stopped while it ran; resuming comes in a later version)"
	case j.CancelRequested && !j.State.Ended():
		state += " (cancel requested)"
	}
	_, _ = fmt.Fprintf(w, "  State    %s\n", state)
	_, _ = fmt.Fprintf(w, "  Created  %s\n", localTime(&j.CreatedAt, time.DateTime))
	if j.EndedAt != nil {
		_, _ = fmt.Fprintf(w, "  Ended    %s\n", localTime(j.EndedAt, time.DateTime))
	}
	_, _ = fmt.Fprintf(w, "  Native tasks at once on the instance: %d\n", j.MaxTasks)

	_, _ = fmt.Fprintln(w, "\nDatabases:")
	for _, it := range j.Items {
		printItem(w, j.Target, it)
	}

	events := j.Events
	switch {
	case len(events) == 0:
		return nil
	case last > 0 && len(events) > last:
		_, _ = fmt.Fprintf(w, "\nLast %d of %d events:\n", last, len(events))
		events = events[len(events)-last:]
	default:
		_, _ = fmt.Fprintf(w, "\nEvents (%d):\n", len(events))
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, ev := range events {
		where := ev.Item
		if ev.Step != "" {
			where += "/" + ev.Step
		}
		msg := oneLine(ev.Message, 100)
		if ev.Level != "" && ev.Level != jobs.Info {
			msg = strings.ToUpper(string(ev.Level)) + ": " + msg
		}
		if ev.Task != nil {
			msg = strings.TrimSpace(fmt.Sprintf("task %d %s %d%% %s", ev.Task.ID, ev.Task.Lifecycle, ev.Task.Percent, msg))
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", localTime(&ev.Time, time.TimeOnly), ev.Type, where, msg)
	}
	return tw.Flush()
}

func printItem(w io.Writer, target string, it jobs.Item) {
	head := fmt.Sprintf("  %s  %s", it.Name, it.State)
	if it.Step != "" && (!it.State.Ended() || it.State == jobs.Interrupted || it.State == jobs.Failed) {
		head += fmt.Sprintf(" at step %s", it.Step)
		if it.Percent > 0 && it.Percent < 100 {
			head += fmt.Sprintf(" (%d%%)", it.Percent)
		}
	}
	_, _ = fmt.Fprintln(w, head)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, st := range it.Steps {
		detail := oneLine(st.Detail, 80)
		_, _ = fmt.Fprintf(tw, "    %d. %s\t%s\t%s\t%s\n", i+1, st.ID, st.Title, st.Status, detail)
	}
	_ = tw.Flush()
	if tr := it.Transfer; tr != nil && !it.State.Ended() {
		_, _ = fmt.Fprintf(w, "    Transfer: %s of %s (%s)\n", sizeBytes(tr.Bytes), sizeBytes(tr.Total), tr.Direction)
	}
	if up := it.Upload; up != nil {
		parts := fmt.Sprintf("%d parts done", len(up.PartsDone))
		if up.Size > 0 && up.PartSize > 0 {
			parts = fmt.Sprintf("%d of %d parts done", len(up.PartsDone), (up.Size+up.PartSize-1)/up.PartSize)
		}
		_, _ = fmt.Fprintf(w, "    Multipart upload: s3://%s/%s, upload id %s, %s\n", up.Bucket, up.Key, up.UploadID, parts)
	}
	if len(it.TaskIDs) > 0 {
		ids := make([]string, len(it.TaskIDs))
		for i, id := range it.TaskIDs {
			ids[i] = fmt.Sprint(id)
		}
		line := "    Native tasks: " + strings.Join(ids, ", ")
		if t := it.Task; t != nil {
			last := strings.Join(slices.DeleteFunc([]string{fmt.Sprint(t.ID), t.Type, string(t.Lifecycle)},
				func(s string) bool { return s == "" }), " ")
			line += fmt.Sprintf("; last %s %d%%", last, t.Percent)
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if it.TaskLeftRunning && it.Task != nil {
		_, _ = fmt.Fprintf(w, "    Task %d was still running on the instance when the job stopped: dialga wait %d -t %s\n", it.Task.ID, it.Task.ID, target)
	}
	if it.Error != "" {
		_, _ = fmt.Fprintf(w, "    Error: %s\n", it.Error)
	}
	if it.Result != "" {
		_, _ = fmt.Fprintf(w, "    Result: %s\n", it.Result)
	}
}

// localTime prints t in the local zone with layout, "-" when unset.
func localTime(t *time.Time, layout string) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format(layout)
}

func sizeBytes(b int64) string { return sizeMiB(float64(b) / (1 << 20)) }
