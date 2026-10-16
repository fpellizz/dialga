package pipeline

import (
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
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// The steps are built from parameters that can be written as JSON, so a
// resumed job can build them again from its journal.

// ItemState carries the native task of an item from the step that starts it
// to the one that follows it. A resumed job seeds it from the item's task.
type ItemState struct {
	mu   sync.Mutex
	task int
}

// SetTask records the native task of the item.
func (s *ItemState) SetTask(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task = id
}

// Task is the native task of the item, 0 before it starts one.
func (s *ItemState) Task() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task
}

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
}

// Step uploads the files one after the other, with one transfer slot. The
// job's cause decides what an interrupted upload leaves: a cancel aborts
// the multipart upload, an engine stop (jobs.ErrInterrupted, a
// transfer.ErrPaused) keeps its parts for a resume, recorded on the item.
func (p UploadParams) Step(env Env, title string) jobs.Step {
	return jobs.NewStep("upload", title, jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
		var total, done int64
		for _, f := range p.Files {
			total += f.Size
		}
		var lines []string
		verified := true
		for _, f := range p.Files {
			base := done
			up := transfer.Upload{Path: f.Path, Bucket: p.Bucket, Key: f.Key, Overwrite: f.Overwrite, Size: f.Size, ModTime: f.ModTime}
			res, err := up.Run(ctx, env.S3, transfer.Options{
				OnProgress: func(pr transfer.Progress) { r.Transfer(jobs.Upload, base+pr.Done, total) },
				Journal:    &uploadJournal{r: r},
			})
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
		}
		if len(lines) == 1 {
			return lines[0], nil
		}
		if c, ok := r.(checksummer); ok {
			c.Checksum(string(types.ChecksumAlgorithmCrc64nvme), "", verified, fmt.Sprintf("%d files, one checksum each: see the step detail", len(lines)))
		}
		return fmt.Sprintf("%d files, %s: %s", len(lines), transfer.FormatBytes(total), strings.Join(lines, "; ")), nil
	})
}

// checksummer is a Reporter that records the checksum of the item's file
// (jobs, Item.Checksum): the transfer steps fill it when it is there.
type checksummer interface {
	Checksum(algorithm, value string, verified bool, note string)
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
}

// Step runs the statement and records the task on the item and in state.
func (p TaskParams) Step(env Env, id, title string, state *ItemState) jobs.Step {
	return jobs.NewStep(id, title, jobs.ResourceRDSTask, func(ctx context.Context, r jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		task, err := sess.SQL.StartTask(ctx, p.Statement, p.Database)
		if err != nil {
			return "", err
		}
		state.SetTask(task.ID)
		r.Task(taskStatus(task))
		return fmt.Sprintf("task %d created", task.ID), nil
	})
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
}

// Step follows the task in state until it ends.
func (p WaitParams) Step(env Env, state *ItemState) jobs.Step {
	return jobs.NewStep("wait", "Wait for the "+p.What+" task", jobs.ResourceRDSTask, func(ctx context.Context, r jobs.Reporter) (string, error) {
		id := state.Task()
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
			OnChange: func(t mssql.Task) { r.Task(taskStatus(t)) },
			OnRetry:  func(err error) { r.Log(jobs.Warn, fmt.Sprintf("reading task %d failed, retrying: %v", id, err)) },
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
	})
}

// PostParams name the database of the post-restore report.
type PostParams struct {
	Database string `json:"database"`
}

// Step reads the report: a failed finding (not online, the login cannot
// enter it) fails the step, the warnings are logged on the item.
func (p PostParams) Step(env Env) jobs.Step {
	db := p.Database
	return jobs.NewStep("post", "Post-restore report of "+db+" (read-only)", jobs.NoResource, func(ctx context.Context, r jobs.Reporter) (string, error) {
		sess, closeOps, err := env.OpenOps(ctx)
		if err != nil {
			return "", err
		}
		defer closeOps()
		rep, err := sess.Post(ctx, db)
		if err != nil {
			return "", err
		}
		var failed []string
		warnings := 0
		for _, f := range rep.Findings {
			switch f.Status {
			case check.Failed:
				failed = append(failed, f.Label+": "+f.Detail)
			case check.Warning:
				warnings++
				r.Log(jobs.Warn, f.Label+": "+f.Detail)
			}
		}
		if len(failed) > 0 {
			return "", errors.New(strings.Join(failed, "; "))
		}
		detail := fmt.Sprintf("%s, compatibility level %d", rep.Database.State, rep.Database.CompatibilityLevel)
		if warnings > 0 {
			detail += fmt.Sprintf(", %d warning(s)", warnings)
		}
		r.Result(db + " restored: " + detail)
		return detail, nil
	})
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
	return jobs.NewStep("download", title, jobs.ResourceTransfer, func(ctx context.Context, r jobs.Reporter) (string, error) {
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
	})
}
