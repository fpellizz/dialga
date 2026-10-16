package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/jobs"
)

// The jobs of the command line go in the same journal as those of the app
// (jobs.DefaultDir), with origin cli, so the Activity page of the app lists
// them, also while it is open: each process writes only its own job files.

// openCLIJobs opens the journal for a run of this command. Without a usable
// state directory the job lives in memory only, as before: that is a
// warning, never a reason not to run.
func openCLIJobs(errOut io.Writer) *jobs.Manager {
	opts := jobs.Options{
		Origin: jobs.OriginCLI,
		Log:    slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	dir, err := jobs.DefaultDir()
	if err == nil {
		var m *jobs.Manager
		if m, err = jobs.Open(dir, opts); err == nil {
			return m
		}
	}
	_, _ = fmt.Fprintf(errOut, "dialga: job journal unavailable (%v): this job is not kept in Activity\n", err)
	return jobs.New(opts)
}

func closeJobs(m *jobs.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = m.Close(ctx)
}

// refusal is a run with --apply that was turned down before it started.
type refusal struct {
	kind      jobs.Kind
	operation string
	target    string
	instance  string
	items     []string
	status    int
	reason    string
	checks    []check.Event
}

// recordRefusal leaves the failed job of a refused --apply in the journal,
// like the app does for a refused Run: pre-flight failed (412), not
// confirmed (422). A dry run never gets here.
func (a *app) recordRefusal(errOut io.Writer, r refusal) {
	m := openCLIJobs(errOut)
	defer closeJobs(m)
	f := jobs.Finished{
		Kind: r.kind, Operation: r.operation, Target: r.target, Instance: r.instance, State: jobs.Failed,
		Refusal: &jobs.Refusal{Status: r.status, Reason: r.reason, Checks: r.checks},
	}
	for _, name := range r.items {
		f.Items = append(f.Items, jobs.FinishedItem{Name: name, Error: r.reason})
	}
	if _, err := m.Record(f); err != nil && !errors.Is(err, jobs.ErrClosed) {
		_, _ = fmt.Fprintf(errOut, "dialga: the refusal was not recorded: %v\n", err)
	}
}

// nonOKChecks keeps the checks that failed or warn.
func nonOKChecks(checks []check.Event) []check.Event {
	var out []check.Event
	for _, c := range checks {
		if c.Status != check.OK {
			out = append(out, c)
		}
	}
	return out
}
