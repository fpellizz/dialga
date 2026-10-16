package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func (a *app) statusCmd() *cobra.Command {
	var target string
	var f mssql.TaskFilter
	cmd := &cobra.Command{
		Use:   "status [target]",
		Short: "List the native backup/restore tasks of a target, newest first",
		Long: `List the native backup/restore tasks of a target (msdb.dbo.rds_fn_task_status),
newest first. RDS keeps 36 days of history. Read-only.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				target = args[0]
			}
			if f.Database != "" {
				if err := validate.DBName(f.Database); err != nil {
					return err
				}
			}
			name, t, tasks, closeTasks, err := a.targetTasks(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer closeTasks()
			list, err := tasks.List(cmd.Context(), f)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Target   string       `json:"target"`
					Instance string       `json:"instance"`
					Tasks    []mssql.Task `json:"tasks"`
				}{name, t.Instance, list})
			}
			return printTasks(cmd.OutOrStdout(), name, t, f, list)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	cmd.Flags().StringVar(&f.Database, "db", "", "only the tasks of this database")
	cmd.Flags().IntVar(&f.Limit, "limit", 20, "maximum number of tasks (1-100)")
	return cmd
}

func (a *app) waitCmd() *cobra.Command {
	var target string
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "wait <task-id>",
		Short: "Follow a native backup/restore task until it ends",
		Long: `Follow a native backup/restore task until it ends, printing every change of
state or percentage. Ctrl-C stops the wait, never the task. Read-only.

Exit status: 0 SUCCESS, 3 ERROR or CANCELLED, 1 error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil || id < 1 {
				return fmt.Errorf("invalid task id %q", args[0])
			}
			if interval <= 0 {
				return fmt.Errorf("--interval must be positive (got %s)", interval)
			}
			name, _, tasks, closeTasks, err := a.targetTasks(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer closeTasks()
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			text := a.output != "json"
			if text {
				_, _ = fmt.Fprintf(out, "Following task %d on %s every %s (Ctrl-C stops the wait, not the task).\n", id, name, interval)
			}
			task, err := mssql.Wait(cmd.Context(), tasks, id, mssql.WaitOptions{
				Interval: interval,
				OnChange: func(t mssql.Task) {
					if text {
						_, _ = fmt.Fprintf(out, "  [%s] task %d: %s %d%%\n", time.Now().Format("15:04:05"), t.ID, t.Lifecycle, t.Percent)
					}
				},
				OnRetry: func(err error) {
					_, _ = fmt.Fprintf(errOut, "dialga: reading the task failed, retrying: %v\n", err)
				},
			})
			if errors.Is(err, context.Canceled) {
				return fmt.Errorf("wait interrupted: task %d keeps running on the instance", id)
			}
			if err != nil {
				return err
			}
			if text {
				printTaskEnd(out, task)
			} else if err := json.NewEncoder(out).Encode(struct {
				Target string     `json:"target"`
				Task   mssql.Task `json:"task"`
			}{name, task}); err != nil {
				return err
			}
			if task.Lifecycle != mssql.Success {
				return exitError{exitTaskFailed}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second,
		"time between two reads (RDS updates the status about every 2 minutes)")
	return cmd
}

// targetTasks opens a session on the target to read its native tasks; it
// asks for the SQL password on the terminal when the configuration says so.
func (a *app) targetTasks(ctx context.Context, target string) (string, config.Target, mssql.TaskReader, func(), error) {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	name, t, err := cfg.Target(target)
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	if t.Connect.Mode == config.ModeTunnel {
		return "", config.Target{}, nil, nil, config.ErrTunnelNotYet
	}
	password, err := a.sqlPassword(t)
	if err != nil {
		return "", config.Target{}, nil, nil, fmt.Errorf("SQL password: %w", err)
	}
	tasks, closeTasks, err := a.openTasks(ctx, t, password)
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	return name, t, tasks, closeTasks, nil
}

func printTasks(w io.Writer, name string, t config.Target, f mssql.TaskFilter, list []mssql.Task) error {
	scope := ""
	if f.Database != "" {
		scope = " for " + f.Database
	}
	if len(list) == 0 {
		_, err := fmt.Fprintf(w, "No native backup/restore tasks%s on %s (RDS keeps 36 days of history).\n", scope, name)
		return err
	}
	_, _ = fmt.Fprintf(w, "Native backup/restore tasks%s on %s (instance %s), newest first:\n", scope, name, t.Instance)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  ID\tTYPE\tDATABASE\tLIFECYCLE\t%\tCREATED\tUPDATED\tINFO")
	for _, task := range list {
		_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", task.ID, task.Type, task.Database,
			task.Lifecycle, task.Percent, shortTime(task.CreatedAt), shortTime(task.UpdatedAt), oneLine(task.Info, 50))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "Full detail of a task: dialga wait <task-id>")
	return err
}

func printTaskEnd(w io.Writer, task mssql.Task) {
	if task.Lifecycle == mssql.Success {
		_, _ = fmt.Fprintf(w, "Task %d completed: %s of %s.\n", task.ID, task.Type, task.Database)
	} else {
		_, _ = fmt.Fprintf(w, "Task %d ended in %s: %s of %s.\n", task.ID, task.Lifecycle, task.Type, task.Database)
	}
	if task.Info == "" {
		return
	}
	for _, line := range strings.Split(strings.ReplaceAll(task.Info, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			_, _ = fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

// shortTime turns 2006-01-02T15:04:05 into 2006-01-02 15:04.
func shortTime(s string) string {
	if len(s) < 16 {
		return s
	}
	return strings.Replace(s[:16], "T", " ", 1)
}

// oneLine flattens text and cuts it to n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
