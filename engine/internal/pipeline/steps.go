package pipeline

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// The steps are built from parameters that can be written as JSON, so a
// resumed job can build them again from its journal.

func taskStatus(t mssql.Task) jobs.TaskStatus {
	return jobs.TaskStatus{ID: t.ID, Type: t.Type, Lifecycle: t.Lifecycle, Percent: t.Percent}
}

// UploadParams are the files of the upload step, as the plan saw them.
type UploadParams struct {
	Bucket string       `json:"bucket"`
	Files  []UploadFile `json:"files"`
}

// UploadFile is one file of an upload: a .bak, or a stripe of one.
type UploadFile struct {
	Path    string    `json:"path"`
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	// Overwrite: the plan saw an object at the key and the user asked to
	// replace it.
	Overwrite bool `json:"overwrite"`
	// Done: the file was uploaded (the step checkpoints it), so a resume
	// skips it.
	Done bool `json:"done,omitempty"`
}

// Step uploads the files one after the other, with one transfer slot. The
// job's cause decides what an interrupted upload leaves: a cancel aborts
// the multipart upload, an engine stop (jobs.ErrInterrupted, a
// transfer.ErrPaused) keeps its parts for a resume, recorded on the item.
func (p UploadParams) Step(env Env, title string) jobs.Step { return p.step(env, title, nil) }

// step is Step, or, with from (the multipart upload the journal kept), the
// resume of it: the file of that upload goes on from the parts S3 has, and
// the files marked Done are skipped.
func (p UploadParams) step(env Env, title string, from *jobs.UploadState) jobs.Step {
	run := func(ctx context.Context, r jobs.Reporter) (string, error) {
		var total, done int64
		for _, f := range p.Files {
			total += f.Size
		}
		files := slices.Clone(p.Files)
		var lines []string
		verified := true
		for i, f := range files {
			if f.Done {
				done += f.Size
				lines = append(lines, filepath.Base(f.Path)+" was uploaded before the interrupt")
				continue
			}
			base := done
			up := transfer.Upload{Path: f.Path, Bucket: p.Bucket, Key: f.Key, Overwrite: f.Overwrite, Size: f.Size, ModTime: f.ModTime}
			opts := transfer.Options{
				OnProgress: func(pr transfer.Progress) { r.Transfer(jobs.Upload, base+pr.Done, total) },
				Journal:    &uploadJournal{r: r},
			}
			var res transfer.Result
			var err error
			if from != nil && from.UploadID != "" && from.Bucket == p.Bucket && from.Key == f.Key {
				res, err = up.Resume(ctx, env.S3, from.UploadID, opts)
			} else {
				res, err = up.Run(ctx, env.S3, opts)
			}
			if err != nil {
				if len(p.Files) > 1 {
					err = fmt.Errorf("%s: %w", filepath.Base(f.Path), err)
				}
				return "", err
			}
			done += f.Size
			lines = append(lines, res.Summary())
			verified = verified && res.Verified
			if len(p.Files) == 1 {
				reportChecksum(r, res)
			}
			files[i].Done = true
			if err := r.Checkpoint(UploadParams{Bucket: p.Bucket, Files: files}); err != nil {
				return "", err
			}
		}
		if len(lines) == 1 {
			return lines[0], nil
		}
		if c, ok := r.(checksummer); ok {
			c.Checksum(string(types.ChecksumAlgorithmCrc64nvme), "", verified, fmt.Sprintf("%d files, one checksum each: see the step detail", len(lines)))
		}
		return fmt.Sprintf("%d files, %s: %s", len(lines), transfer.FormatBytes(total), strings.Join(lines, "; ")), nil
	}
	return jobs.WithKind(jobs.NewStep("upload", title, jobs.ResourceTransfer, run), KindUpload, p)
}

// checksummer is a Reporter that records the checksum of the item's file
// (jobs, Item.Checksum): the transfer steps fill it when it is there.
type checksummer interface {
	Checksum(algorithm, value string, verified bool, note string)
}

// replacer is a Reporter that records the outcome of a replace on the item
// (jobs.Item.Replace).
type replacer interface {
	Replace(core.ReplaceOutcome)
}

func reportReplace(r jobs.Reporter, o *core.ReplaceOutcome) {
	if rr, ok := r.(replacer); ok && o != nil {
		rr.Replace(*o)
	}
}

func reportChecksum(r jobs.Reporter, res transfer.Result) {
	if c, ok := r.(checksummer); ok {
		c.Checksum(res.ChecksumAlgorithm, res.Checksum, res.Verified, res.Note)
	}
}

// uploadJournal records the multipart upload on the item, for a resume.
type uploadJournal struct {
	r     jobs.Reporter
	mu    sync.Mutex
	state jobs.UploadState
}

func (j *uploadJournal) UploadStarted(s transfer.UploadState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = jobs.UploadState{Bucket: s.Bucket, Key: s.Key, UploadID: s.UploadID, Size: s.Size, PartSize: s.PartSize, PartsDone: []int{}}
	j.r.Upload(j.state)
}

func (j *uploadJournal) PartDone(_ string, p transfer.PartState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	i, _ := slices.BinarySearch(j.state.PartsDone, p.Number)
	j.state.PartsDone = slices.Insert(j.state.PartsDone, i, p.Number)
	j.r.Upload(j.state)
}

func (j *uploadJournal) UploadEnded(string, error) {}

// TaskParams are a statement that queues a native task (a restore, a
// backup) on a database.
type TaskParams struct {
	Statement string `json:"statement"`
	Database  string `json:"database"`
	// Key and the options are what Statement was built from, so a resume
	// can build it again and refuse a journal that was edited.
	Key          string `json:"key,omitempty"`
	Differential bool   `json:"differential,omitempty"`
	NoRecovery   bool   `json:"noRecovery,omitempty"`
}

// expected is the statement of the step id ("restore", "export", or "backup",
// the safety backup of a replace) for
// params, built the way the plan builds it, on bucket.
func (p TaskParams) expected(id, bucket string) (string, error) {
	arn := mssql.S3ARN(bucket, p.Key)
	switch id {
	case "restore":
		return mssql.RestoreDatabase(p.Database, arn, mssql.RestoreOptions{Differential: p.Differential, NoRecovery: p.NoRecovery}), nil
	case "export", ops.StepBackup:
		return mssql.BackupDatabase(p.Database, arn), nil
	}
	return "", fmt.Errorf("unknown task step %q", id)
}

// Step runs the statement and records the task on the item.
func (p TaskParams) Step(env Env, id, title string) jobs.Step {
	return jobs.WithKind(jobs.NewStep(id, title, jobs.ResourceRDSTask, func(ctx context.Context, r jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		task, err := sess.SQL.StartTask(ctx, p.Statement, p.Database)
		if err != nil {
			return "", err
		}
		r.Task(taskStatus(task))
		return fmt.Sprintf("task %d created", task.ID), nil
	}), KindTask, p)
}

// TaskError is a native task that ended in ERROR or CANCELLED.
type TaskError struct{ Task mssql.Task }

func (e *TaskError) Error() string {
	msg := fmt.Sprintf("task %d ended in %s", e.Task.ID, e.Task.Lifecycle)
	if line := lastLine(e.Task.Info); line != "" {
		msg += ": " + line
	}
	return msg
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// WaitParams follow the native task of an item: What names it ("restore",
// "backup"); Result, when set, is the result line of the item once the task
// succeeded.
type WaitParams struct {
	What   string `json:"what"`
	Result string `json:"result,omitempty"`
	// ID is the step id when an item waits twice (a replace backs up, then
	// restores); default "wait".
	ID string `json:"id,omitempty"`
}

// Step follows the last native task of the item until it ends: the one its
// restore or export queued, in a resumed job too.
func (p WaitParams) Step(env Env) jobs.Step {
	return jobs.WithKind(jobs.NewStep(cmp.Or(p.ID, "wait"), "Wait for the "+p.What+" task", jobs.ResourceRDSTask, func(ctx context.Context, r jobs.Reporter) (string, error) {
		id := r.LastTaskID()
		if id == 0 {
			return "", errors.New("no native task to follow")
		}
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		task, err := mssql.Wait(ctx, sess.SQL, id, mssql.WaitOptions{
			Interval: env.interval(),
			OnChange: func(t mssql.Task) {
				// First: the event of the job tells the caller to read it.
				if env.OnTask != nil {
					env.OnTask(t)
				}
				r.Task(taskStatus(t))
			},
			OnRetry: func(err error) { r.Log(jobs.Warn, fmt.Sprintf("reading task %d failed, retrying: %v", id, err)) },
		})
		if err != nil {
			return "", err
		}
		if task.Lifecycle != mssql.Success {
			return "", &TaskError{Task: task}
		}
		if p.Result != "" {
			r.Result(p.Result)
		}
		return fmt.Sprintf("task %d completed", id), nil
	}), KindWait, p)
}

// SQLParams are a statement that queues no task: drop, close to other
// sessions (single-user), open again (multi-user) or rename a database, in a
// replace; finish or cancel in a single operation.
// The steps look at the instance before they act, so a resumed job does not
// repeat what was done (ops.Session.DropExisting, SingleUser, MultiUser,
// Rename).
type SQLParams struct {
	// Op is ops.StepDrop, StepSingleUser, StepMultiUser or StepRename.
	Op        string `json:"op"`
	Statement string `json:"statement"`
	Database  string `json:"database"`
	// To is the new name of a rename; Fallback the database to rename back
	// to To when it fails (see ops.Session.Rename).
	To       string `json:"to,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	// Outcome is what the item has kept once the step succeeded; it only
	// reaches the item (jobs.Item.Replace), it changes no statement.
	Outcome *core.ReplaceOutcome `json:"outcome,omitempty"`
	// TaskID is the native task a cancel (ops.StepCancel) is for.
	TaskID int `json:"taskId,omitempty"`
	// Result, when set, is the result line of the item once the step
	// succeeded (a single operation that ends with it).
	Result string `json:"result,omitempty"`
}

// expected is the statement the parameters build, for a resume to compare.
func (p SQLParams) expected() (string, error) {
	switch p.Op {
	case ops.StepDrop:
		return mssql.DropDatabase(p.Database), nil
	case ops.StepSingleUser:
		return mssql.SetSingleUser(p.Database), nil
	case ops.StepMultiUser:
		return mssql.SetMultiUser(p.Database), nil
	case ops.StepRename:
		return mssql.RenameDatabase(p.Database, p.To), nil
	case ops.StepFinish:
		return mssql.FinishRestore(p.Database), nil
	case ops.StepCancel:
		return mssql.CancelTask(p.TaskID), nil
	}
	return "", fmt.Errorf("unknown statement step %q", p.Op)
}

// Step runs the statement on a session of its own.
func (p SQLParams) Step(env Env, id, title string) jobs.Step {
	return jobs.WithKind(jobs.NewStep(id, title, jobs.NoResource, func(ctx context.Context, r jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		var detail string
		switch p.Op {
		case ops.StepDrop:
			detail, err = sess.DropExisting(ctx, p.Database)
		case ops.StepSingleUser:
			detail, err = sess.SingleUser(ctx, p.Database)
		case ops.StepMultiUser:
			detail, err = sess.MultiUser(ctx, p.Database)
		case ops.StepRename:
			detail, err = sess.Rename(ctx, p.Database, p.To, p.Fallback)
		case ops.StepFinish:
			detail, err = sess.FinishRestore(ctx, p.Database)
		case ops.StepCancel:
			detail, err = sess.CancelTask(ctx, p.TaskID)
		default:
			err = fmt.Errorf("unknown statement step %q", p.Op)
		}
		if err == nil {
			reportReplace(r, p.Outcome)
			if p.Result != "" {
				r.Result(p.Result)
			}
		}
		return detail, err
	}), KindSQL, p)
}

// AccessParams are one statement of an access operation (grant, revoke,
// fix-orphan). Statement is the masked one, as the plan shows it: the
// password of a new login is given by Env.LoginPassword and never recorded.
type AccessParams struct {
	ops.AccessStep
	Statement string `json:"statement"`
}

// Step runs the statement on a session of its own.
func (p AccessParams) Step(env Env, id, title string) jobs.Step {
	return jobs.WithKind(jobs.NewStep(id, title, jobs.NoResource, func(ctx context.Context, _ jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		return sess.RunAccessStep(ctx, p.AccessStep, env.LoginPassword)
	}), KindAccess, p)
}

// PostParams name the database of the post-restore report.
type PostParams struct {
	Database string `json:"database"`
	// Note, for a replace, is what it kept (the old database, the backup):
	// it goes in the result of the item.
	Note string `json:"note,omitempty"`
	// Outcome is what the replace kept, for jobs.Item.Replace.
	Outcome *core.ReplaceOutcome `json:"outcome,omitempty"`
}

// Step reads the report: a failed finding (not online, the login cannot
// enter it) fails the step, the warnings are logged on the item.
func (p PostParams) Step(env Env) jobs.Step {
	db := p.Database
	return jobs.WithKind(jobs.NewStep("post", "Post-restore report of "+db+" (read-only)", jobs.NoResource, func(ctx context.Context, r jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		rep, err := sess.Post(ctx, db)
		if err != nil {
			return "", err
		}
		detail, failed, warnings := ReportVerdict(rep)
		for _, w := range warnings {
			r.Log(jobs.Warn, w)
		}
		if len(failed) > 0 {
			return "", errors.New(strings.Join(failed, "; "))
		}
		result := db + " restored: " + detail
		if p.Note != "" {
			result += ". " + p.Note
		}
		reportReplace(r, p.Outcome)
		r.Result(result)
		return detail, nil
	}), KindPost, p)
}

// DownloadParams are the backup of an export and where it goes.
type DownloadParams struct {
	Database  string `json:"database"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite"`
}

// Step downloads the backup.
func (p DownloadParams) Step(env Env, title string) jobs.Step {
	return jobs.WithKind(jobs.NewStep("download", title, jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
		d := transfer.Download{Bucket: p.Bucket, Key: p.Key, Path: p.Path, Overwrite: p.Overwrite}
		res, err := d.Run(ctx, env.S3, transfer.Options{
			OnProgress: func(pr transfer.Progress) { r.Transfer(jobs.Download, pr.Done, pr.Total) },
		})
		if err != nil {
			return "", err
		}
		reportChecksum(r, res)
		r.Result(fmt.Sprintf("%s exported to %s", p.Database, p.Path))
		return res.Summary(), nil
	}), KindDownload, p)
}

// ReportVerdict reads the findings of a post-restore report: the failed ones
// and the warnings, as "label: detail", and the line that sums it up.
func ReportVerdict(rep ops.Report) (detail string, failed, warnings []string) {
	for _, f := range rep.Findings {
		switch f.Status {
		case check.Failed:
			failed = append(failed, f.Label+": "+f.Detail)
		case check.Warning:
			warnings = append(warnings, f.Label+": "+f.Detail)
		}
	}
	detail = fmt.Sprintf("%s, compatibility level %d", rep.Database.State, rep.Database.CompatibilityLevel)
	if n := len(warnings); n > 0 {
		detail += fmt.Sprintf(", %d warning(s)", n)
	}
	return detail, failed, warnings
}
