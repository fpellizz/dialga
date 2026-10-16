package pipeline

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// Env is the target a job runs on and how its steps reach it.
type Env struct {
	Name   string
	Target config.Target
	// S3 is the client of the bucket of the target (transfer.Open).
	S3 transfer.Client
	// OpenOps opens a SQL session on the target with its bucket (ops.Open):
	// the pre-flight opens one, each native step its own, since a job can
	// last hours.
	OpenOps func(context.Context) (*ops.Session, func(), error)
	// TaskInterval is the time between two reads of a native task
	// (default 30s; RDS updates the status about every 2 minutes).
	TaskInterval time.Duration
}

func (e Env) interval() time.Duration {
	if e.TaskInterval <= 0 {
		return 30 * time.Second
	}
	return e.TaskInterval
}

// Plan is a job after its pre-flight checks: what every database will do.
type Plan struct {
	Kind      jobs.Kind `json:"kind"`
	Target    string    `json:"target"`
	Instance  string    `json:"instance"`
	Protected bool      `json:"protected"`
	// Ready: every database passed its checks (warnings included), or the
	// request skips the failing ones and at least one is left.
	Ready bool `json:"ready"`
	// Confirmation is the text to retype before the job starts: the target
	// name on a protected target, otherwise what an action replaces.
	Confirmation string `json:"confirmation,omitempty"`
	// Digest identifies the actions: the same digest means the same
	// statements and calls, so what runs is what the user previewed.
	Digest   string     `json:"digest"`
	Warnings []string   `json:"warnings,omitempty"`
	Items    []ItemPlan `json:"items"`
	// Preview is the dry-run text, as the CLI prints it.
	Preview string `json:"preview"`

	bucket string
	core   core.Plan
	spec   jobs.Spec
}

// ItemPlan is one database of the job.
type ItemPlan struct {
	Database string `json:"database"`
	// File is the local backup (import; the stripes, comma-separated) or
	// the local destination (export; "" when the backup stays in S3).
	File string `json:"file"`
	// Key is the S3 key of the upload (a "*" pattern for stripes) or of the
	// export, defaults resolved: send it back to start the job previewed
	// (the default export key carries a timestamp).
	Key   string `json:"key"`
	Ready bool   `json:"ready"`
	// Skipped: the pre-flight failed and the request skips such databases.
	Skipped bool          `json:"skipped,omitempty"`
	Checks  []check.Event `json:"checks"`
	Actions []core.Action `json:"actions"`
	// Uploads are the files of an import as the upload plans them, and
	// Rejected those it refuses, with the reason.
	Uploads  []transfer.Item      `json:"uploads,omitempty"`
	Rejected []transfer.Rejection `json:"rejected,omitempty"`

	steps   []jobs.Step
	replace []string // what destructive actions replace, for the confirmation
}

func (it *ItemPlan) add(step, label string, status check.Status, detail string) {
	it.Checks = append(it.Checks, check.Event{Step: step, Label: label, Status: status, Detail: detail})
}

func (it *ItemPlan) ready() bool {
	for _, c := range it.Checks {
		if c.Status == check.Failed {
			return false
		}
	}
	return true
}

// ErrNotReady means a pre-flight check failed: nothing was started.
var ErrNotReady = errors.New("a pre-flight check failed: nothing was started")

// Core is the plan of the whole job: the actions of the databases that run.
func (p *Plan) Core() core.Plan { return p.core }

// Failed lists the failed checks, "database: label: detail".
func (p *Plan) Failed() []string {
	var out []string
	for _, it := range p.Items {
		for _, c := range it.Checks {
			if c.Status == check.Failed {
				out = append(out, fmt.Sprintf("%s: %s: %s", it.Database, c.Label, c.Detail))
			}
		}
	}
	return out
}

// Start checks that the plan is ready and confirmed, then starts its job.
func (p *Plan) Start(m *jobs.Manager, confirm string) (jobs.Job, error) {
	if !p.Ready {
		return jobs.Job{}, ErrNotReady
	}
	if want := p.Confirmation; want != "" && confirm != want {
		return jobs.Job{}, fmt.Errorf("%w (expected %q): nothing was started", core.ErrNotConfirmed, want)
	}
	return m.Start(p.spec)
}

// Build runs the pre-flight checks of every database and plans the job. It
// only reads: S3 (HeadObject), the local files and SQL Server.
func Build(ctx context.Context, env Env, req Request) (*Plan, error) {
	if err := req.Validate(env.Target); err != nil {
		return nil, err
	}
	sess, closeOps, err := env.OpenOps(ctx)
	if err != nil {
		return nil, err
	}
	defer closeOps()
	p := &Plan{Kind: req.Kind, Target: env.Name, Instance: env.Target.Instance, Protected: env.Target.Protected,
		Items: []ItemPlan{}, bucket: env.Target.Bucket}
	b := &builder{env: env, sess: sess, plan: p, prefix: req.Prefix, keys: map[string]string{}, files: map[string]string{}}
	for _, it := range req.Import {
		if err := b.importItem(ctx, it); err != nil {
			return nil, err
		}
	}
	for _, it := range req.Export {
		if err := b.exportItem(ctx, it); err != nil {
			return nil, err
		}
	}
	p.finish(req.SkipFailing, env.Target)
	return p, nil
}

// builder plans the items of one job.
type builder struct {
	env    Env
	sess   *ops.Session
	plan   *Plan
	prefix *string
	// keys and files seen so far, to the database that uses them.
	keys, files map[string]string
}

func (b *builder) where(key string) string { return "s3://" + b.env.Target.Bucket + "/" + key }

func (b *builder) importItem(ctx context.Context, in ImportItem) error {
	t := b.env.Target
	it := ItemPlan{Database: in.Database, File: strings.Join(in.Files, ", "), Checks: []check.Event{}, Actions: []core.Action{}}
	batch, err := transfer.PlanUploads(ctx, b.env.S3, b.env.Name, t, transfer.UploadRequest{
		Files: in.Files, Key: in.Key, Prefix: b.prefix, Overwrite: in.Overwrite,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", in.Database, err)
	}
	it.Uploads, it.Rejected = batch.Items, batch.Rejected
	const step, label = "upload", "Upload to S3"
	var keys []string
	for _, rej := range batch.Rejected {
		keys = append(keys, rej.Key)
		what := rej.Reason
		if len(in.Files) > 1 {
			what = filepath.Base(rej.Path) + ": " + what
		}
		it.add(step, label, check.Failed, what)
	}
	upload := UploadParams{Bucket: t.Bucket}
	var size int64
	for _, item := range batch.Items {
		keys = append(keys, item.Key)
		size += item.Size
		st, err := os.Stat(item.Path)
		if err != nil {
			it.add(step, label, check.Failed, "cannot read "+item.Path+": "+err.Error())
			continue
		}
		upload.Files = append(upload.Files, UploadFile{Path: item.Path, Key: item.Key, Size: st.Size(), ModTime: st.ModTime(), Overwrite: item.Replaces})
		if item.Replaces {
			it.add(step, label, check.Warning, b.where(item.Key)+" exists: the upload replaces it")
			it.replace = append(it.replace, item.Key)
		}
		if other, dup := b.keys[item.Key]; dup {
			it.add(step, label, check.Failed, fmt.Sprintf("%s is the key of %s too", b.where(item.Key), other))
		}
		b.keys[item.Key] = in.Database
	}
	for _, w := range batch.Warnings {
		if !slices.Contains(b.plan.Warnings, w) {
			b.plan.Warnings = append(b.plan.Warnings, w)
		}
	}
	slices.Sort(keys)
	it.Key = keys[0]
	if len(keys) > 1 {
		it.Key = stripePattern(keys)
	}
	if len(batch.Items) > 0 {
		if len(batch.Rejected) == 0 {
			it.add(step, label, check.OK, fmt.Sprintf("%d file(s), %s, to %s", len(batch.Items), transfer.FormatBytes(size), b.where(it.Key)))
		}
		it.Actions = append(it.Actions, uploadAction(batch))
	}
	if len(keys) > 1 {
		if err := b.checkStripes(ctx, &it, keys); err != nil {
			return fmt.Errorf("%s: %w", in.Database, err)
		}
	}

	opts := mssql.RestoreOptions{Differential: in.Differential, NoRecovery: in.NoRecovery}
	prep, err := b.sess.Restore(ctx, in.Database, "s3://"+t.Bucket+"/"+it.Key, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", in.Database, err)
	}
	for _, c := range prep.Checks {
		if c.Step != "source" { // the upload puts the backup there
			it.Checks = append(it.Checks, c)
		}
	}
	restore := prep.Plan.Actions[0]
	restore.Run = nil
	wait := WaitParams{What: "restore"}
	if in.NoRecovery {
		wait.Result = in.Database + " stays RESTORING: restore a differential backup, or finish it with dialga finish " + in.Database
	}
	state := &ItemState{}
	it.Actions = append(it.Actions, restore, waitAction("restore", b.env.interval()))
	if len(upload.Files) > 0 {
		it.steps = append(it.steps, upload.Step(b.env, it.Actions[0].Title))
	}
	it.steps = append(it.steps,
		TaskParams{Statement: restore.Statement, Database: in.Database}.Step(b.env, "restore", restore.Title, state),
		wait.Step(b.env, state))
	if !in.NoRecovery {
		post := PostParams{Database: in.Database}
		it.Actions = append(it.Actions, core.Action{
			ID: "post", Title: "Post-restore report of " + in.Database + " (read-only)", Kind: core.KindSQL, Risk: core.Change,
			Statement: "read-only: state, files, login access, orphaned users, Windows users, objects",
		})
		it.steps = append(it.steps, post.Step(b.env))
	}
	b.plan.Items = append(b.plan.Items, it)
	return nil
}

// uploadAction is the one action of the files of an item.
func uploadAction(batch *transfer.Batch) core.Action {
	a := batch.Plan.Actions[0]
	a.ID, a.Run = "upload", nil
	if n := len(batch.Plan.Actions); n > 1 {
		var size int64
		var statements []string
		for i, x := range batch.Plan.Actions {
			size += batch.Items[i].Size
			statements = append(statements, x.Statement)
			if x.Risk == core.Destructive {
				a.Risk = core.Destructive
			}
		}
		a.Title = fmt.Sprintf("Upload %d files of one backup (%s)", n, transfer.FormatBytes(size))
		a.Statement = strings.Join(statements, "\n")
	}
	return a
}

// stripePattern is the key RDS restores the stripes of one backup from: what
// the keys have in common at the start and at the end, "*" in between.
func stripePattern(keys []string) string {
	first := keys[0]
	shortest := len(first)
	pre := len(first)
	for _, k := range keys[1:] {
		shortest = min(shortest, len(k))
		n := 0
		for n < pre && n < len(k) && k[n] == first[n] {
			n++
		}
		pre = n
	}
	suf := shortest - pre
	for _, k := range keys[1:] {
		n := 0
		for n < suf && k[len(k)-1-n] == first[len(first)-1-n] {
			n++
		}
		suf = n
	}
	return first[:pre] + "*" + first[len(first)-suf:]
}

// checkStripes fails the item when the pattern of its stripes matches other
// objects in the bucket: RDS would restore them with the backup.
func (b *builder) checkStripes(ctx context.Context, it *ItemPlan, keys []string) error {
	const step, label = "stripes", "Backup files"
	pre, suf, _ := strings.Cut(it.Key, "*")
	existing, err := b.sess.Bucket.Keys(ctx, pre)
	if err != nil {
		return err
	}
	var others []string
	for _, k := range existing {
		if len(k) >= len(pre)+len(suf) && strings.HasPrefix(k, pre) && strings.HasSuffix(k, suf) && !slices.Contains(keys, k) {
			others = append(others, k)
		}
	}
	if len(others) > 0 {
		it.add(step, label, check.Failed, fmt.Sprintf("%s also matches %s: RDS would restore it with this backup; use another prefix",
			b.where(it.Key), strings.Join(others, ", ")))
		return nil
	}
	it.add(step, label, check.OK, fmt.Sprintf("%d stripes restored together from %s", len(keys), b.where(it.Key)))
	return nil
}

func (b *builder) exportItem(ctx context.Context, in ExportItem) error {
	it := ItemPlan{Database: in.Database, Checks: []check.Event{}, Actions: []core.Action{}}
	prep, err := b.sess.Export(ctx, in.Database, in.Key)
	if err != nil {
		return fmt.Errorf("%s: %w", in.Database, err)
	}
	it.Key = prep.Params.Key
	it.Checks = append(it.Checks, prep.Checks...)
	if other, dup := b.keys[it.Key]; dup {
		it.add("destination", "Destination in S3", check.Failed, fmt.Sprintf("%s is the key of %s too", b.where(it.Key), other))
	}
	b.keys[it.Key] = in.Database

	export := prep.Plan.Actions[0]
	export.Run = nil
	state := &ItemState{}
	it.Actions = append(it.Actions, export, waitAction("backup", b.env.interval()))
	wait := WaitParams{What: "backup"}
	if in.Destination == "" {
		wait.Result = in.Database + " exported to " + b.where(it.Key)
	}
	it.steps = []jobs.Step{
		TaskParams{Statement: export.Statement, Database: in.Database}.Step(b.env, "export", export.Title, state),
		wait.Step(b.env, state),
	}
	if in.Destination != "" {
		dl, err := b.localFile(&it, in)
		if err != nil {
			return fmt.Errorf("%s: %w", in.Database, err)
		}
		risk := core.Change
		if len(it.replace) > 0 {
			risk = core.Destructive
		}
		download := core.Action{
			ID: "download", Title: fmt.Sprintf("Download %s", b.where(it.Key)), Kind: core.KindAWS, Risk: risk,
			Statement: fmt.Sprintf("GET %s · ranged GETs, checksum verified when the object has one\nto %s through a temporary file",
				b.where(it.Key), dl.Path),
		}
		it.Actions = append(it.Actions, download)
		it.steps = append(it.steps, dl.Step(b.env, download.Title))
	}
	b.plan.Items = append(b.plan.Items, it)
	return nil
}

// localFile checks the local destination of an export.
func (b *builder) localFile(it *ItemPlan, in ExportItem) (DownloadParams, error) {
	const step, label = "local", "Local file"
	path, err := transfer.LocalDestination(in.Destination, it.Key)
	if err != nil {
		return DownloadParams{}, err
	}
	it.File = path
	switch st, err := os.Lstat(path); {
	case errors.Is(err, fs.ErrNotExist):
		it.add(step, label, check.OK, path+" is free (free space is checked once the backup is done)")
	case err != nil:
		it.add(step, label, check.Failed, err.Error())
	case !st.Mode().IsRegular():
		it.add(step, label, check.Failed, path+" exists and is not a regular file")
	case !in.Overwrite:
		it.add(step, label, check.Failed, fmt.Sprintf("%s already exists (%s): choose overwrite to replace it", path, transfer.FormatBytes(st.Size())))
	default:
		it.add(step, label, check.Warning, path+" exists: the download replaces it")
		it.replace = append(it.replace, filepath.Base(path))
	}
	if other, dup := b.files[path]; dup {
		it.add(step, label, check.Failed, fmt.Sprintf("%s is the destination of %s too", path, other))
	}
	b.files[path] = in.Database
	return DownloadParams{Database: in.Database, Bucket: b.env.Target.Bucket, Key: it.Key, Path: path, Overwrite: in.Overwrite}, nil
}

func waitAction(what string, every time.Duration) core.Action {
	return core.Action{
		ID: "wait", Title: "Wait for the " + what + " task", Kind: core.KindSQL, Risk: core.Change,
		Statement: fmt.Sprintf("SELECT * FROM msdb.dbo.rds_fn_task_status(NULL, <task id>) every %s until the task ends", every),
	}
}

// finish decides which items run and computes what the whole plan asks.
func (p *Plan) finish(skipFailing bool, target config.Target) {
	spec := jobs.Spec{
		Kind: p.Kind, Target: p.Target, Instance: p.Instance, MaxTasks: target.MaxTasks,
		// The RDS slots are per instance, whatever the target that names it.
		InstanceKey: strings.Join([]string{target.AccountID, target.Region, target.Instance}, "/"),
	}
	p.core = core.Plan{Operation: string(p.Kind), Target: p.Target, Instance: p.Instance, Protected: p.Protected, Actions: []core.Action{}}
	var replaced []string
	failing := 0
	for i := range p.Items {
		it := &p.Items[i]
		it.Ready = it.ready()
		if !it.Ready {
			failing++
			it.Skipped = skipFailing
			continue
		}
		for _, a := range it.Actions {
			a.ID = it.Database + "/" + a.ID
			p.core.Actions = append(p.core.Actions, a)
		}
		replaced = append(replaced, it.replace...)
		spec.Items = append(spec.Items, jobs.ItemSpec{Name: it.Database, Steps: it.steps})
	}
	switch len(replaced) {
	case 0:
	case 1:
		p.core.Subject = replaced[0]
	default:
		p.core.Subject = fmt.Sprintf("replace %d files", len(replaced))
	}
	p.Ready = len(spec.Items) > 0 && (failing == 0 || skipFailing)
	p.Confirmation = p.core.Confirmation()
	data, _ := json.Marshal(p.core)
	sum := sha256.Sum256(data)
	p.Digest = hex.EncodeToString(sum[:16])
	p.spec = spec
	p.Preview = p.preview()
}

// preview is the dry-run text: per database its checks and its actions.
func (p *Plan) preview() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dry run: %s of %d database(s) on %s (instance %s). Nothing is changed.\n", p.Kind, len(p.Items), p.Target, p.Instance)
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "WARN  %s\n", w)
	}
	tags := map[check.Status]string{check.OK: "OK  ", check.Warning: "WARN", check.Failed: "FAIL", check.Skipped: "SKIP"}
	for _, it := range p.Items {
		from, to := it.File, "s3://"+p.bucket+"/"+it.Key
		if p.Kind == jobs.Export {
			from, to = to, cmp.Or(it.File, "(stays in S3)")
		}
		note := ""
		switch {
		case it.Skipped:
			note = "  [skipped: a pre-flight check failed]"
		case !it.Ready:
			note = "  [a pre-flight check failed]"
		}
		fmt.Fprintf(&b, "\n%s: %s -> %s%s\n", it.Database, from, to, note)
		for _, c := range it.Checks {
			fmt.Fprintf(&b, "  %s  %-20s %s\n", tags[c.Status], c.Label, c.Detail)
		}
		for i, a := range it.Actions {
			tag := ""
			if a.Risk == core.Destructive {
				tag = "  [destructive]"
			}
			fmt.Fprintf(&b, "  %d. %s%s\n", i+1, a.Title, tag)
			for _, line := range strings.Split(a.Statement, "\n") {
				fmt.Fprintf(&b, "       %s\n", line)
			}
		}
	}
	switch {
	case !p.Ready:
		b.WriteString("\nSome pre-flight checks failed: the job cannot start.\n")
	case p.Confirmation != "":
		fmt.Fprintf(&b, "\nStarting it asks to retype %q.\n", p.Confirmation)
	}
	return b.String()
}
