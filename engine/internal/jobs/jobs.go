// Package jobs runs multi-database jobs: an import or an export of several
// databases on one target, each database (an item) going through its steps
// (upload, restore, wait, post...) under a scheduler that limits what runs
// at once, all jobs together:
//   - S3 transfers: Options.Limits[ResourceTransfer] (default 3);
//   - native RDS tasks: at most Spec.MaxTasks per instance (RDS runs 2).
//
// Items are pipelined: while database n restores, database n+1 uploads, and
// slots go to the oldest job first, then in the order of its items. A failed
// item does not stop the others. Cancel stops the queued and waiting work
// and cancels the context of the running steps; a native task already
// running on the instance is not killed by the engine (rds_cancel_task
// does that): the item records it (TaskLeftRunning).
//
// Every change is an event (job.*, step.*, upload.progress, task.status,
// log: plan section 3.4), streamed to the subscribers, and every job is
// persisted as JSON in the journal (one file per job, atomic writes). The
// history survives restarts; a job that was running when the engine
// stopped becomes interrupted and keeps what a resume needs: its last step,
// the multipart upload (upload id, parts done) and the native task ids.
package jobs

import (
	"encoding/json"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// State of a job or of an item.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	// Interrupted: the engine stopped (app closed, crash) while it ran.
	Interrupted State = "interrupted"
)

// Ended reports whether the state is final.
func (s State) Ended() bool {
	switch s {
	case Succeeded, Failed, Cancelled, Interrupted:
		return true
	}
	return false
}

// StepStatus is the state of one step of an item.
type StepStatus string

const (
	StepPending StepStatus = "pending"
	// StepWaiting: the step is next, waiting for a slot of its resource.
	StepWaiting   StepStatus = "waiting"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	// StepSkipped: an earlier step of the item failed.
	StepSkipped     StepStatus = "skipped"
	StepCancelled   StepStatus = "cancelled"
	StepInterrupted StepStatus = "interrupted"
)

// Kind of job.
type Kind string

const (
	Import Kind = "import"
	Export Kind = "export"
)

// EventType names an event; the prefix says what it is about.
type EventType string

const (
	EventJobQueued      EventType = "job.queued"
	EventJobStarted     EventType = "job.started"
	EventJobCancelling  EventType = "job.cancelling"
	EventJobSucceeded   EventType = "job.succeeded"
	EventJobFailed      EventType = "job.failed"
	EventJobCancelled   EventType = "job.cancelled"
	EventJobInterrupted EventType = "job.interrupted"
	// EventJobResumed: an interrupted job runs again (Manager.Resume).
	EventJobResumed EventType = "job.resumed"

	EventStepWaiting     EventType = "step.waiting"
	EventStepStarted     EventType = "step.started"
	EventStepSucceeded   EventType = "step.succeeded"
	EventStepFailed      EventType = "step.failed"
	EventStepSkipped     EventType = "step.skipped"
	EventStepCancelled   EventType = "step.cancelled"
	EventStepInterrupted EventType = "step.interrupted"

	// EventUploadProgress reports the bytes of a transfer, uploads and
	// downloads alike (TransferProgress.Direction). Not kept in the job's
	// event log: the item keeps the last one.
	EventUploadProgress EventType = "upload.progress"
	// EventTaskStatus reports a native RDS task. Kept in the log when the
	// task or its lifecycle changes; percentage updates are live only.
	EventTaskStatus EventType = "task.status"
	EventLog        EventType = "log"
)

// Level of a log event.
type Level string

const (
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
)

// Direction of a transfer.
type Direction string

const (
	Upload   Direction = "upload"
	Download Direction = "download"
)

// Event is one change of a job. Seq grows by one per event of the job, so a
// client that reconnects asks for the events after the last one it saw.
type Event struct {
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Type    EventType `json:"type"`
	Item    string    `json:"item,omitempty"`
	Step    string    `json:"step,omitempty"`
	Message string    `json:"message,omitempty"`
	// Level is set on log events.
	Level    Level             `json:"level,omitempty"`
	Transfer *TransferProgress `json:"transfer,omitempty"`
	Task     *TaskStatus       `json:"task,omitempty"`
}

// TransferProgress is the state of an S3 transfer. The engine computes the
// speed and the ETA from the bytes the step reports.
type TransferProgress struct {
	Direction      Direction `json:"direction"`
	Bytes          int64     `json:"bytes"`
	Total          int64     `json:"total"`
	BytesPerSecond int64     `json:"bytesPerSecond"`
	// ETASeconds is 0 while the speed is not known yet.
	ETASeconds int64 `json:"etaSeconds,omitempty"`
}

// TaskStatus is the last known state of a native RDS task.
type TaskStatus struct {
	ID        int             `json:"id"`
	Type      string          `json:"type,omitempty"`
	Lifecycle mssql.Lifecycle `json:"lifecycle"`
	Percent   int             `json:"percent"`
}

// UploadState is what resuming a multipart upload needs. PartsDone is a
// hint for the GUI and for planning: on resume, ListParts on S3 is the truth.
type UploadState struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	UploadID  string `json:"uploadId"`
	Size      int64  `json:"size"`
	PartSize  int64  `json:"partSize"`
	PartsDone []int  `json:"partsDone"`
}

// ChecksumInfo is the checksum of the item's file, as the transfer step saw
// it (transfer.Result): Value is the base64 of the whole object, Verified
// says the one computed here matches the one S3 keeps.
type ChecksumInfo struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value,omitempty"`
	Verified  bool   `json:"verified"`
	Note      string `json:"note,omitempty"`
}

// StepInfo is one step of an item as the GUI and the journal see it.
type StepInfo struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Resource Resource   `json:"resource,omitempty"`
	Status   StepStatus `json:"status"`
	// Kind and Params rebuild the step on a resume (Resumable): the factory
	// registered for Kind gets Params back. Never secrets: they are in the
	// journal and in the API.
	Kind   string          `json:"kind,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	// TaskID is the native task the step reported (Reporter.Task).
	TaskID    int        `json:"taskId,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// Detail is the result line of a succeeded step or the error of a
	// failed one.
	Detail string `json:"detail,omitempty"`
}

// Item is one database of a job.
type Item struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	// Step is the current step, or the last one the item reached.
	Step string `json:"step,omitempty"`
	// Percent is the progress of the current step (0-100).
	Percent  int               `json:"percent"`
	Steps    []StepInfo        `json:"steps"`
	Transfer *TransferProgress `json:"transfer,omitempty"`
	// Task is the last native task of the item; TaskIDs lists every task
	// it started or followed, oldest first (a resume must not repeat them).
	Task    *TaskStatus `json:"task,omitempty"`
	TaskIDs []int       `json:"taskIds,omitempty"`
	// TaskLeftRunning: the item stopped (cancel, interrupt) while its
	// native task was still running on the instance.
	TaskLeftRunning bool         `json:"taskLeftRunning,omitempty"`
	Upload          *UploadState `json:"upload,omitempty"`
	// Checksum is that of the file the item uploaded or downloaded.
	Checksum *ChecksumInfo `json:"checksum,omitempty"`
	Error    string        `json:"error,omitempty"`
	Result   string        `json:"result,omitempty"`
}

// Job is one execution on a target, with its items and its event log.
type Job struct {
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Target   string `json:"target"`
	Instance string `json:"instance"`
	// InstanceKey is account/region/instance of the RDS instance (empty in
	// journals of older versions): a resume refuses another one.
	InstanceKey string `json:"instanceKey,omitempty"`
	// MaxTasks is the native tasks the job runs at once on the instance.
	MaxTasks        int        `json:"maxTasks"`
	State           State      `json:"state"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	CancelRequested bool       `json:"cancelRequested,omitempty"`
	Items           []Item     `json:"items"`
	// Events is the log: every event but the transfer progress and the
	// task percentages (see EventUploadProgress, EventTaskStatus).
	Events []Event `json:"events"`
}

// Summary is a job in a list, without steps and events.
type Summary struct {
	ID              string     `json:"id"`
	Kind            Kind       `json:"kind"`
	Target          string     `json:"target"`
	Instance        string     `json:"instance"`
	State           State      `json:"state"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	CancelRequested bool       `json:"cancelRequested,omitempty"`
	Databases       []string   `json:"databases"`
	Succeeded       int        `json:"succeeded"`
	Failed          int        `json:"failed"`
}

// Summary returns the list view of the job.
func (j Job) Summary() Summary {
	s := Summary{
		ID: j.ID, Kind: j.Kind, Target: j.Target, Instance: j.Instance, State: j.State,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, EndedAt: j.EndedAt,
		CancelRequested: j.CancelRequested, Databases: make([]string, 0, len(j.Items)),
	}
	for _, it := range j.Items {
		s.Databases = append(s.Databases, it.Name)
		switch it.State {
		case Succeeded:
			s.Succeeded++
		case Failed:
			s.Failed++
		}
	}
	return s
}

// clone copies what the manager changes in place, so a snapshot can be read
// while the job goes on. Events and the structs behind pointers are never
// changed once set: they are shared.
func (j Job) clone(events bool) Job {
	c := j
	c.Items = make([]Item, len(j.Items))
	for i, it := range j.Items {
		it.Steps = append([]StepInfo(nil), it.Steps...)
		it.TaskIDs = append([]int(nil), it.TaskIDs...)
		c.Items[i] = it
	}
	if events {
		c.Events = append([]Event(nil), j.Events...)
	} else {
		c.Events = nil
	}
	return c
}
