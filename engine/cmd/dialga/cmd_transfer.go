package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

func init() { commands = append(commands, (*app).uploadCmd, (*app).downloadCmd) }

// transferFlags are the flags upload and download share.
type transferFlags struct {
	target      string
	overwrite   bool
	concurrency int
	apply       bool
	confirm     string
}

func (f *transferFlags) register(cmd *cobra.Command, replaces string) {
	cmd.Flags().StringVarP(&f.target, "target", "t", "", "target name from the configuration")
	cmd.Flags().BoolVar(&f.overwrite, "overwrite", false, "replace "+replaces)
	cmd.Flags().IntVar(&f.concurrency, "concurrency", transfer.DefaultConcurrency, "parts in flight at the same time (1-32)")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "run the transfer (without it: dry run, nothing changes)")
	cmd.Flags().StringVar(&f.confirm, "confirm", "", "the text the plan asks to retype (protected target, replaced object or file)")
}

// transferExit is the help text of the exit status of both commands.
const transferExit = `
Dry run by default: the plan shows every S3 call; --apply runs it. On a
protected target, or when something is replaced, the plan asks to retype a
text: --confirm, or the terminal asks for it.

With -o json, stdout gets one JSON document at the end and, with --apply,
stderr gets the events as JSON lines ({"event":"step"|"progress","data":…}).

Exit status: 0 done, 2 some file was rejected (the others are transferred),
1 error.`

func (a *app) uploadCmd() *cobra.Command {
	var f transferFlags
	var key, prefix string
	cmd := &cobra.Command{
		Use:   "upload <file.bak>...",
		Short: "Upload backup files to the S3 bucket of a target",
		Long: `Upload backup files to the S3 bucket of a target, by default under its
import prefix with the file name as the key. Multipart, with parts sized on
the file, retries of each part and a CRC64NVME checksum S3 verifies; the
object is read back at the end. Ctrl-C aborts the upload: nothing is left
in S3. An object that exists is never replaced without --overwrite, and
every file that cannot go is listed with the reason.
` + transferExit,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := transfer.UploadRequest{Files: args, Key: key, Overwrite: f.overwrite}
			if cmd.Flags().Changed("prefix") {
				req.Prefix = &prefix
			}
			return a.runTransfer(cmd, f, func(ctx context.Context, c transfer.Client, name string, t config.Target, o transfer.Options) (*transfer.Batch, error) {
				req.Options = o
				return transfer.PlanUploads(ctx, c, name, t, req)
			})
		},
	}
	f.register(cmd, "objects that already exist in S3")
	cmd.Flags().StringVar(&key, "key", "", "S3 key of the object (one file only)")
	cmd.Flags().StringVar(&prefix, "prefix", "", `key prefix instead of the import prefix of the target ("" is the bucket root)`)
	return cmd
}

func (a *app) downloadCmd() *cobra.Command {
	var f transferFlags
	cmd := &cobra.Command{
		Use:   "download <key|s3://bucket/key|name> [dest]",
		Short: "Download an object of the S3 bucket of a target",
		Long: `Download an object of the S3 bucket of a target: a key, an s3:// URL on the
bucket of the target, or a bare file name, looked up under the export
prefix. dest is a file or an existing folder (default: the current folder).
Parallel ranged GETs into a temporary file that becomes dest only once
complete; the CRC checksum of the object is verified when it has one, the
free space is checked first. A file that exists is never replaced without
--overwrite.
` + transferExit,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := transfer.DownloadRequest{Source: args[0], Overwrite: f.overwrite}
			if len(args) == 2 {
				req.Dest = args[1]
			}
			return a.runTransfer(cmd, f, func(ctx context.Context, c transfer.Client, name string, t config.Target, o transfer.Options) (*transfer.Batch, error) {
				req.Options = o
				return transfer.PlanDownload(ctx, c, name, t, req)
			})
		},
	}
	f.register(cmd, "the local file if it exists")
	return cmd
}

type planTransfer func(context.Context, transfer.Client, string, config.Target, transfer.Options) (*transfer.Batch, error)

func (a *app) runTransfer(cmd *cobra.Command, f transferFlags, build planTransfer) error {
	if f.concurrency < 1 || f.concurrency > transfer.MaxConcurrency {
		return fmt.Errorf("--concurrency must be between 1 and %d (got %d)", transfer.MaxConcurrency, f.concurrency)
	}
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	name, t, err := cfg.Target(f.target)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	client, err := a.openS3(ctx, t)
	if err != nil {
		return err
	}
	r := &transferReport{
		target: name, bucket: t.Bucket, json: a.output == "json", out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(),
		tty: isTerminal(cmd.ErrOrStderr()), shown: map[string]int{},
	}
	b, err := build(ctx, client, name, t, transfer.Options{Concurrency: f.concurrency, OnProgress: r.progress})
	if err != nil {
		return err
	}
	if !f.apply {
		if err := r.dryRun(b); err != nil {
			return err
		}
		if len(b.Rejected) > 0 {
			return exitError{exitCheckFailed}
		}
		return nil
	}
	confirmed, err := a.transferConfirmation(ctx, cmd.ErrOrStderr(), b, f.confirm)
	if err != nil {
		return err
	}
	if want := b.Confirmation; want != "" && confirmed != want {
		return fmt.Errorf("%w (expected %q): nothing was transferred", core.ErrNotConfirmed, want)
	}
	runErr := r.run(ctx, b, confirmed)
	if err := r.finish(b, runErr); err != nil {
		return err
	}
	switch {
	case runErr != nil:
		return exitError{exitFailure}
	case len(b.Rejected) > 0:
		return exitError{exitCheckFailed}
	}
	return nil
}

// transferConfirmation is the text that confirms the plan: --confirm, or
// what the user types on the terminal.
func (a *app) transferConfirmation(ctx context.Context, w io.Writer, b *transfer.Batch, given string) (string, error) {
	want := b.Confirmation
	if want == "" || given != "" {
		return given, nil
	}
	f, ok := a.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", fmt.Errorf("this %s asks to retype %q: pass --confirm %s", b.Plan.Operation, want, want)
	}
	_, _ = fmt.Fprintf(w, "Type %q to confirm the %s: ", want, b.Plan.Operation)
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(a.stdin).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case s := <-line:
		return s, nil
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// transferReport prints a transfer: the plan in a dry run, then the steps
// and the progress (stderr), or JSON.
type transferReport struct {
	target string
	bucket string
	json   bool
	out    io.Writer
	errOut io.Writer
	tty    bool

	mu    sync.Mutex
	dirty bool           // a progress line is on the terminal
	shown map[string]int // last tenth printed per step, without a terminal
	steps []core.Event
}

// transferDoc is the JSON document of upload and download.
type transferDoc struct {
	Target  string `json:"target"`
	Applied bool   `json:"applied"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	*transfer.Batch
	Steps   []core.Event      `json:"steps,omitempty"`
	Results []transfer.Result `json:"results,omitempty"`
}

var transferVerbs = map[string][2]string{
	"upload":   {"Uploading", "Uploaded"},
	"download": {"Downloading", "Downloaded"},
}

func (r *transferReport) dryRun(b *transfer.Batch) error {
	if r.json {
		return json.NewEncoder(r.out).Encode(transferDoc{Target: r.target, OK: len(b.Rejected) == 0, Batch: b})
	}
	if err := b.Plan.Preview(r.out); err != nil {
		return err
	}
	r.notes(b)
	msg := fmt.Sprintf("Run again with --apply to %s.", b.Plan.Operation)
	if len(b.Plan.Actions) == 0 {
		msg = fmt.Sprintf("Nothing to %s.", b.Plan.Operation)
	}
	_, err := fmt.Fprintln(r.out, msg)
	return err
}

// notes prints the rejected files and the warnings.
func (r *transferReport) notes(b *transfer.Batch) {
	for _, rej := range b.Rejected {
		_, _ = fmt.Fprintf(r.out, "  REJECTED  %s: %s\n", rej.Path, rej.Reason)
	}
	for _, w := range b.Warnings {
		_, _ = fmt.Fprintf(r.out, "  WARNING   %s\n", w)
	}
}

func (r *transferReport) run(ctx context.Context, b *transfer.Batch, confirmed string) error {
	if !r.json {
		where := fmt.Sprintf("to s3://%s (target %s; Ctrl-C aborts, nothing is left in S3)", r.bucket, r.target)
		if b.Plan.Operation == "download" {
			where = fmt.Sprintf("from s3://%s (target %s; Ctrl-C stops it and keeps nothing)", r.bucket, r.target)
		}
		_, _ = fmt.Fprintf(r.out, "%s %d file(s) %s.\n", transferVerbs[b.Plan.Operation][0], len(b.Plan.Actions), where)
		r.notes(b)
	}
	return b.Execute(ctx, confirmed, r.step)
}

func (r *transferReport) step(e core.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.json {
		r.line("step", e)
		if e.Status != core.Running {
			r.steps = append(r.steps, e)
		}
		return
	}
	if e.Status == core.Running {
		return
	}
	r.clearProgress()
	tag := map[core.Status]string{core.OK: "OK  ", core.Failed: "FAIL", core.Skipped: "SKIP"}[e.Status]
	_, _ = fmt.Fprintf(r.out, "  %s  %-40s %s\n", tag, e.Label, e.Detail)
}

func (r *transferReport) progress(p transfer.Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.json {
		r.line("progress", p)
		return
	}
	pct := 100
	if p.Total > 0 {
		pct = int(p.Done * 100 / p.Total)
	}
	line := fmt.Sprintf("  %s  %3d%%  %s of %s", filepath.Base(p.Path), pct,
		transfer.FormatBytes(p.Done), transfer.FormatBytes(p.Total))
	if p.BytesPerSecond > 0 {
		line += fmt.Sprintf("  %s/s", transfer.FormatBytes(int64(p.BytesPerSecond)))
	}
	if p.ETASeconds > 0 {
		line += "  ETA " + (time.Duration(p.ETASeconds) * time.Second).String()
	}
	if r.tty {
		_, _ = fmt.Fprintf(r.errOut, "\r\x1b[K%s", line)
		r.dirty = true
		return
	}
	// Without a terminal: a line every 10%.
	if tenth, seen := r.shown[p.Step]; !seen || pct/10 > tenth {
		r.shown[p.Step] = pct / 10
		_, _ = fmt.Fprintln(r.errOut, line)
	}
}

func (r *transferReport) clearProgress() {
	if r.dirty {
		_, _ = fmt.Fprint(r.errOut, "\r\x1b[K")
		r.dirty = false
	}
}

// line writes one JSON event on stderr.
func (r *transferReport) line(event string, data any) {
	_ = json.NewEncoder(r.errOut).Encode(struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{event, data})
}

func (r *transferReport) finish(b *transfer.Batch, runErr error) error {
	results := b.Results()
	if r.json {
		doc := transferDoc{Target: r.target, Applied: true, OK: runErr == nil && len(b.Rejected) == 0,
			Batch: b, Steps: r.steps, Results: results}
		if runErr != nil {
			doc.Error = runErr.Error()
		}
		return json.NewEncoder(r.out).Encode(doc)
	}
	r.mu.Lock()
	r.clearProgress()
	r.mu.Unlock()
	verb := transferVerbs[b.Plan.Operation][1]
	msg := fmt.Sprintf("%s %d file(s).", verb, len(results))
	switch {
	case runErr != nil && errors.Is(runErr, context.Canceled):
		msg = fmt.Sprintf("Cancelled: %d of %d file(s) %s.", len(results), len(b.Plan.Actions), strings.ToLower(verb))
	case runErr != nil:
		msg = fmt.Sprintf("Failed: %d of %d file(s) %s.", len(results), len(b.Plan.Actions), strings.ToLower(verb))
	case len(b.Rejected) > 0:
		msg = fmt.Sprintf("%s %d file(s); %d rejected.", verb, len(results), len(b.Rejected))
	}
	_, err := fmt.Fprintln(r.out, msg)
	return err
}
