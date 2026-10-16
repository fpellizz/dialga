package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/api"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/pipeline"
)

// pipelineJobEnv is how the resumed steps reach the target: the pipeline's
// environment, built like the one of dialga import.
// A tunnel-mode target gets the job's one tunnel (jobOpenOps); closeEnv
// ends it once the resumed job has ended.
func (a *app) pipelineJobEnv(ctx context.Context, name string, t config.Target, password string) (env any, closeEnv func(), err error) {
	client, err := a.openS3(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	openOps, closeOps, err := a.jobOpenOps(ctx, name, t, password)
	if err != nil {
		return nil, nil, err
	}
	return pipeline.Env{Name: name, Target: t, S3: client, TaskInterval: a.jobInterval, OpenOps: openOps}, closeOps, nil
}

func (a *app) jobsResumeCmd(dir *string) *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "resume <job-id>",
		Short: "Resume an interrupted job in this terminal",
		Long: `Resume an interrupted job: the databases that ended and the steps that
succeeded are kept, the others run again from what the journal kept (an upload
continues its multipart upload, a native task already queued is followed, not
queued again). The job keeps its id and runs in this process until it ends,
printing its events.

The journal belongs to one engine at a time: while the desktop app runs,
resume the job from its Activity page instead. A protected target asks to
retype its name (or --confirm <target>). Ctrl-C interrupts the job again: it
can be resumed later.

Exit status: 0 the job succeeded, 3 it ended failed or cancelled, 1 error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !jobs.ValidID(id) {
				return fmt.Errorf("invalid job id %q: dialga jobs lists them", id)
			}
			return a.resumeJob(cmd, *dir, id, confirm)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the target name, when the target is protected")
	return cmd
}

func (a *app) resumeJob(cmd *cobra.Command, dir, id, confirm string) error {
	if dir == "" {
		d, err := jobs.DefaultDir()
		if err != nil {
			return err
		}
		dir = d
	}
	m, err := jobs.Open(dir, jobs.Options{
		Registry: a.jobRegistry,
		Log:      slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if errors.Is(err, jobs.ErrLocked) {
		return fmt.Errorf("%w: resume the job from the Activity page of the desktop app, or close it and run this again", err)
	}
	if err != nil {
		return err
	}
	// Ctrl-C or the end: the engine of this process stops; a job still
	// running is written as interrupted.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	}()

	job, err := m.Get(id)
	if err != nil {
		return fmt.Errorf("job %s not found in %s", id, dir)
	}
	if job.State != jobs.Interrupted {
		return fmt.Errorf("job %s is %s: only an interrupted job can be resumed", id, job.State)
	}
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	name, t, err := cfg.Target(job.Target)
	if err != nil {
		return fmt.Errorf("the target %s of job %s is not in the configuration any more", job.Target, id)
	}
	if t.Instance != job.Instance {
		return fmt.Errorf("target %s now points at instance %s, the job ran on %s: not resumed", name, t.Instance, job.Instance)
	}
	if a.jobEnv == nil {
		return errors.New("resuming needs the import/export pipeline, which this build of dialga does not have")
	}
	if t.Protected {
		if confirm == "" {
			answer, err := a.readConfirm(fmt.Sprintf("Retype %q to resume the %s on %s: ", name, job.Kind, name))
			if err != nil {
				return fmt.Errorf("target %s is protected: run this on a terminal or pass --confirm %s (%w)", name, name, err)
			}
			confirm = answer
		}
		if confirm != name {
			return fmt.Errorf("%w (expected %q): nothing was resumed", core.ErrNotConfirmed, name)
		}
	}
	password, err := a.sqlPassword(name, t)
	if err != nil {
		return fmt.Errorf("SQL password: %w", err)
	}
	env, closeEnv, err := a.jobEnv(cmd.Context(), name, t, password)
	if err != nil {
		return err
	}
	defer closeEnv()
	snap, err := m.Resume(id, jobs.ResumeOptions{Env: env, InstanceKey: api.InstanceKey(t)})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	text := a.output != "json"
	p := &eventPrinter{w: out, deciles: map[string]int64{}}
	after := snap.Events[len(snap.Events)-1].Seq - 1 // from job.resumed
	ended, err := followResumed(cmd.Context(), m, id, after, func(ev jobs.Event) {
		if text {
			p.print(ev)
		}
	})
	if err != nil {
		return err
	}
	if !ended {
		// Ctrl-C: Close interrupts the job, so the next resume continues it.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
		final, _ := m.Get(id)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: job %s %s: dialga jobs resume %s continues it\n", id, final.State, id)
		return exitError{exitFailure}
	}
	final, err := m.Get(id)
	if err != nil {
		return err
	}
	if text {
		_, _ = fmt.Fprintf(out, "Job %s %s. Detail: dialga jobs show %s\n", id, final.State, id)
	} else if err := json.NewEncoder(out).Encode(final); err != nil {
		return err
	}
	if final.State != jobs.Succeeded {
		return exitError{exitTaskFailed}
	}
	return nil
}

// followResumed calls onEvent for the events after seq after until the job
// ends (true) or ctx does (false); a subscriber that fell behind subscribes
// again from its last event.
func followResumed(ctx context.Context, m *jobs.Manager, id string, after int64, onEvent func(jobs.Event)) (bool, error) {
	for {
		sub, err := m.Subscribe(id, after)
		if err != nil {
			return false, err
		}
		final := false
		handle := func(ev jobs.Event) {
			if ev.Seq <= after {
				return
			}
			after = ev.Seq
			onEvent(ev)
			final = final || isFinal(ev.Type)
		}
		for _, ev := range sub.Replay {
			handle(ev)
		}
	live:
		for {
			select {
			case ev, open := <-sub.C:
				if !open {
					break live
				}
				handle(ev)
			case <-ctx.Done():
				sub.Close()
				return false, nil
			}
		}
		if final || !sub.Lagged() {
			return true, nil
		}
	}
}

func isFinal(t jobs.EventType) bool {
	switch t {
	case jobs.EventJobSucceeded, jobs.EventJobFailed, jobs.EventJobCancelled, jobs.EventJobInterrupted:
		return true
	}
	return false
}

// eventPrinter prints the events a person follows: the logged ones, and
// the transfer progress at every 10% of each database.
type eventPrinter struct {
	w       io.Writer
	deciles map[string]int64 // last 10% mark printed, by item
}

func (p *eventPrinter) print(ev jobs.Event) {
	where := ev.Item
	if ev.Step != "" {
		where += "/" + ev.Step
	}
	msg := ev.Message
	switch {
	case ev.Type == jobs.EventUploadProgress && ev.Transfer != nil:
		tr := ev.Transfer
		if tr.Total <= 0 {
			return
		}
		decile := tr.Bytes * 10 / tr.Total
		if last, seen := p.deciles[ev.Item]; seen && decile <= last {
			return
		}
		p.deciles[ev.Item] = decile
		msg = fmt.Sprintf("%s of %s (%s)", sizeBytes(tr.Bytes), sizeBytes(tr.Total), tr.Direction)
	case ev.Task != nil:
		msg = strings.TrimSpace(fmt.Sprintf("task %d %s %d%% %s", ev.Task.ID, ev.Task.Lifecycle, ev.Task.Percent, msg))
	case ev.Level == jobs.Warn || ev.Level == jobs.Error:
		msg = fmt.Sprintf("%s: %s", ev.Level, msg)
	}
	_, _ = fmt.Fprintf(p.w, "  [%s] %-16s %-22s %s\n", ev.Time.Local().Format(time.TimeOnly), ev.Type, where, oneLine(msg, 120))
}
