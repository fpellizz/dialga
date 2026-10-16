package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/inspect"
)

func init() { commands = append(commands, (*app).inspectCmd) }

// inspectFunc runs an inspection (inspect.Run; tests replace it).
type inspectFunc func(context.Context, inspect.Request, check.Emitter) (*inspect.Result, error)

func (a *app) inspectCmd() *cobra.Command {
	var target, image string
	cmd := &cobra.Command{
		Use:   "inspect <file.bak>...",
		Short: "Read .bak files locally and judge them against a target",
		Long: `Read SQL Server native backups (.bak) in a throwaway local SQL Server
(podman, or docker; DIALGA_CONTAINER_RUNTIME forces one) before uploading
them: RESTORE LABELONLY, HEADERONLY and FILELISTONLY. Several files are
several backups, or the stripes of one: they are grouped by media set.

With -t, every backup is judged against the engine of the target (version,
encryption, TDE, FILESTREAM) and the restored files against its free
storage (warning above 80%). Without -t, only the headers are read.
Nothing leaves the workstation; AWS is only read. The image is never pulled
implicitly ($DIALGA_INSPECT_IMAGE replaces the default).

Exit status: 0 restorable (or headers read, without -t), 2 not restorable
or not enough space, 1 error (also a file that cannot be read).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := inspect.Request{Paths: args, Image: image}
			var t string
			if target != "" {
				cfg, _, err := a.loadConfig()
				if err != nil {
					return err
				}
				name, tgt, err := cfg.Target(target)
				if err != nil {
					return err
				}
				req.Target, t = inspect.TargetFor(name, tgt), name
			}
			run := a.runInspect
			if run == nil {
				run = inspect.Run
			}
			report := &inspectReport{target: t, json: a.output == "json", w: cmd.OutOrStdout(), color: a.stdoutIsTerminal}
			if !report.json {
				_, _ = fmt.Fprintf(report.w, "Inspecting %d file(s)%s\n", len(args), report.against())
			}
			res, err := run(cmd.Context(), req, report.event)
			if ferr := report.finish(res, err); ferr != nil {
				return ferr
			}
			if err != nil {
				return err
			}
			switch res.Outcome {
			case inspect.NotRestorable:
				return exitError{exitCheckFailed}
			case inspect.Unreadable:
				return exitError{exitFailure}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target to judge the backups against (default: headers only)")
	cmd.Flags().StringVar(&image, "image", "", "SQL Server image that reads the backups (default "+inspect.DefaultImage+")")
	return cmd
}

// inspectReport prints the steps as they end, then every backup (text), or
// one document at the end (json).
type inspectReport struct {
	target string
	json   bool
	w      io.Writer
	color  bool
	steps  []check.Event
}

func (r *inspectReport) against() string {
	if r.target == "" {
		return " (headers only: -t judges them against a target)"
	}
	return " against " + r.target
}

func (r *inspectReport) event(e check.Event) {
	if e.Status == check.Running {
		return
	}
	r.steps = append(r.steps, e)
	if !r.json {
		_, _ = fmt.Fprintf(r.w, "  %s  %-22s %s\n", tag(e.Status, r.color), e.Label, e.Detail)
	}
}

func (r *inspectReport) finish(res *inspect.Result, runErr error) error {
	if r.json {
		doc := struct {
			Target string          `json:"target,omitempty"`
			OK     bool            `json:"ok"`
			Steps  []check.Event   `json:"steps"`
			Result *inspect.Result `json:"result,omitempty"`
			Error  string          `json:"error,omitempty"`
		}{Target: r.target, Steps: r.steps, Result: res}
		if runErr != nil {
			doc.Error = runErr.Error()
		} else {
			doc.OK = res.Outcome == inspect.Restorable || res.Outcome == inspect.Read
		}
		return json.NewEncoder(r.w).Encode(doc)
	}
	if runErr != nil {
		return nil // execute prints it
	}
	for _, b := range res.Backups {
		r.backup(b)
	}
	_, _ = fmt.Fprintln(r.w)
	var msg string
	switch res.Outcome {
	case inspect.Restorable:
		msg = "Restorable on " + r.target + "."
	case inspect.NotRestorable:
		msg = "Not restorable on " + r.target + " as it is: see FAIL above."
	case inspect.Unreadable:
		msg = "Some files could not be read: see FAIL above."
	default:
		msg = "Headers read."
	}
	_, err := fmt.Fprintln(r.w, msg)
	return err
}

func (r *inspectReport) backup(b inspect.Backup) {
	w := r.w
	_, _ = fmt.Fprintf(w, "\n%s\n", strings.Join(b.Files, "\n"))
	line := func(k, v string) { _, _ = fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("    %-20s %s", k, v), " ")) }
	if b.Error != "" {
		_, _ = fmt.Fprintf(w, "  %s  %s\n", tag(check.Failed, r.color), b.Error)
	}
	if h := b.Header; h != nil {
		line("Database", fmt.Sprintf("%s (server %s)", h.DatabaseName, h.ServerName))
		line("Source", fmt.Sprintf("%s (%s) · compatibility level %d", h.Product, h.Version, h.CompatibilityLevel))
		line("Collation", fmt.Sprintf("%s · recovery %s", h.Collation, h.RecoveryModel))
		props := []string{h.BackupType}
		for _, p := range []struct {
			on   bool
			name string
		}{{h.Compressed, "compressed"}, {h.Checksums, "checksums"}, {h.CopyOnly, "copy-only"}, {h.Encryption != "", "encrypted"}} {
			if p.on {
				props = append(props, p.name)
			}
		}
		line("Backup", strings.Join(append(props, h.StartedAt), " · "))
		size := inspect.FormatBytes(h.BackupSize)
		if h.Compressed {
			size += fmt.Sprintf(" (%s compressed)", inspect.FormatBytes(h.CompressedSize))
		}
		line("Backup size", size)
		if b.BackupSets > 1 {
			line("Backup sets", fmt.Sprintf("%d (the first one is read)", b.BackupSets))
		}
	}
	for i, f := range b.DatabaseFiles {
		k := ""
		if i == 0 {
			k = "Files"
		}
		line(k, fmt.Sprintf("%s  %-30s %10s  %s", f.Type, f.LogicalName, inspect.FormatBytes(f.SizeBytes), f.FileGroup))
	}
	if len(b.DatabaseFiles) > 0 {
		line("Needed on restore", inspect.FormatBytes(b.RequiredBytes))
	}
	for _, f := range b.Findings {
		_, _ = fmt.Fprintf(w, "  %s  %-11s %s\n", tag(f.Status, r.color), f.Check, f.Message)
	}
	if b.Remedy != "" {
		_, _ = fmt.Fprintf(w, "    What to do: %s\n", b.Remedy)
	}
}

// tag is the status column of the text reports.
func tag(s check.Status, color bool) string {
	return (&checkReport{color: color}).tag(s)
}
