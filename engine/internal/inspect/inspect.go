// Package inspect reads SQL Server native backups (.bak) on the workstation,
// before they are uploaded, and judges them against the engine of a target.
// It is the Go counterpart of mssql-bak-inspect.sh.
//
// A throwaway SQL Server runs in a local container (podman preferred, docker
// otherwise) with the files mounted read-only; RESTORE LABELONLY groups the
// files into backups (one file, or the stripes of one backup), HEADERONLY and
// FILELISTONLY read them. The container is removed at the end, also on
// cancel or timeout. Nothing leaves the workstation: AWS is only read, to
// learn the engine version and the free storage of the target.
//
// A backup is restorable when its SQL Server major version is not newer than
// the target's and it is neither encrypted, nor TDE, nor FILESTREAM; the
// restored files must fit in the free storage (warning above 80%).
package inspect

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/check"
)

// Outcome of a backup, or of the whole inspection.
type Outcome string

const (
	// Restorable: the target can restore it natively (rds_restore_database).
	Restorable Outcome = "restorable"
	// NotRestorable: the version, the encryption or the space rule it out.
	NotRestorable Outcome = "not-restorable"
	// Read: headers read, no target to judge them against.
	Read Outcome = "read"
	// Unreadable: not a backup this SQL Server can read (or a stripe is missing).
	Unreadable Outcome = "error"
)

// Request is one inspection.
type Request struct {
	// Paths are the .bak files: independent backups, or the stripes of one
	// backup, in any order.
	Paths []string
	// Image is the SQL Server image that reads them ($DIALGA_INSPECT_IMAGE,
	// then DefaultImage, when empty). It must be at least as new as the
	// backups; it is never pulled implicitly.
	Image string
	// Target looks up the engine to judge the backups against; nil reads
	// the headers only.
	Target func(context.Context) (Target, error)
}

// Target is the engine the backups are judged against.
type Target struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Engine   string `json:"engine"`
	// EngineVersion as RDS reports it: 14.00.3540.1.v1.
	EngineVersion string `json:"engineVersion"`
	// Major is the SQL Server major version: 14 = SQL Server 2017.
	Major int `json:"major"`
	// FreeBytes is the free storage from CloudWatch; nil when unknown.
	FreeBytes *int64 `json:"freeBytes,omitempty"`
}

// Result is what an inspection found.
type Result struct {
	Outcome Outcome `json:"outcome"`
	// Runtime and Image ran the throwaway SQL Server; Server is its version.
	Runtime string  `json:"runtime"`
	Image   string  `json:"image"`
	Server  string  `json:"server"`
	Target  *Target `json:"target,omitempty"`
	// Backups in the order of their first file.
	Backups []Backup `json:"backups"`
	// RequiredBytes is what all the restored databases take: the sum of
	// their files, not the size of the .bak files.
	RequiredBytes int64 `json:"requiredBytes"`
	// Space compares RequiredBytes with the free storage of the target.
	Space *Finding `json:"space,omitempty"`
}

// Backup is one backup: a .bak file, or the stripes of one backup.
type Backup struct {
	// Files are the paths, in stripe order.
	Files   []string `json:"files"`
	Outcome Outcome  `json:"outcome"`
	// Error says why the backup could not be read.
	Error         string         `json:"error,omitempty"`
	Header        *Header        `json:"header,omitempty"`
	DatabaseFiles []DatabaseFile `json:"databaseFiles,omitempty"`
	// RequiredBytes is the sum of the database files: what a restore takes.
	RequiredBytes int64 `json:"requiredBytes"`
	// BackupSets in the media: RDS restores the first one.
	BackupSets int       `json:"backupSets"`
	Findings   []Finding `json:"findings,omitempty"`
	// Remedy is what to do when the backup is not restorable or unreadable.
	Remedy string `json:"remedy,omitempty"`

	disks []string // the files inside the container
}

// Header is the first backup set of RESTORE HEADERONLY.
type Header struct {
	DatabaseName string `json:"databaseName"`
	ServerName   string `json:"serverName"`
	// BackupType: full, differential, log, file, file differential,
	// partial or partial differential.
	BackupType string `json:"backupType"`
	// Version of the source SQL Server (major.minor.build) and its name.
	Version            string `json:"version"`
	Major              int    `json:"major"`
	Product            string `json:"product"`
	CompatibilityLevel int    `json:"compatibilityLevel"`
	Collation          string `json:"collation"`
	RecoveryModel      string `json:"recoveryModel"`
	Compressed         bool   `json:"compressed"`
	Checksums          bool   `json:"checksums"`
	CopyOnly           bool   `json:"copyOnly"`
	// Encryption is the backup encryption algorithm (AES_256); empty when
	// the backup is not encrypted.
	Encryption     string `json:"encryption,omitempty"`
	BackupSize     int64  `json:"backupSize"`
	CompressedSize int64  `json:"compressedSize"`
	// StartedAt and FinishedAt are the source server's wall clock, without
	// a zone, formatted as 2006-01-02T15:04:05.
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// DatabaseFile is a row of RESTORE FILELISTONLY.
type DatabaseFile struct {
	LogicalName  string `json:"logicalName"`
	PhysicalName string `json:"physicalName"`
	// Type: D data, L log, S FILESTREAM or In-Memory OLTP, F full-text catalog.
	Type      string `json:"type"`
	FileGroup string `json:"fileGroup,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	// TDE: the file is encrypted with Transparent Data Encryption.
	TDE bool `json:"tde"`
}

// Finding is one rule of the verdict.
type Finding struct {
	// Check: version, type, encryption, tde, filestream, sets or space.
	Check string `json:"check"`
	// Status: ok, warning (worth reading, not blocking) or failed.
	Status  check.Status `json:"status"`
	Message string       `json:"message"`
}

// DefaultImage reads backups up to SQL Server 2022; 2025-latest reads all.
const DefaultImage = "mcr.microsoft.com/mssql/server:2022-latest"

// Environment variables.
const (
	// EnvImage replaces DefaultImage.
	EnvImage = "DIALGA_INSPECT_IMAGE"
	// EnvRuntime forces the container runtime: podman or docker.
	EnvRuntime = "DIALGA_CONTAINER_RUNTIME"
)

// MaxFiles caps the files of one inspection.
const MaxFiles = 64

type step struct {
	id, label string
}

var (
	stepTarget    = step{"target", "Target engine"}
	stepRuntime   = step{"runtime", "Container runtime"}
	stepImage     = step{"image", "SQL Server image"}
	stepContainer = step{"container", "Throwaway SQL Server"}
	stepReady     = step{"ready", "SQL Server ready"}
	stepMedia     = step{"media", "Backup media"}
	stepHeader    = step{"header", "Backup headers"}
	stepFiles     = step{"files", "Database files"}
	stepVerdict   = step{"verdict", "Verdict"}
	stepSpace     = step{"space", "Space on the target"}
	stepCleanup   = step{"cleanup", "Cleanup"}
)

// reporter emits the events of a run. Without a target there is no target,
// verdict or space step.
type reporter struct {
	emit   check.Emitter
	judged bool
}

func (r *reporter) start(s step) {
	r.emit(check.Event{Step: s.id, Label: s.label, Status: check.Running})
}

func (r *reporter) end(s step, st check.Status, detail string) {
	r.emit(check.Event{Step: s.id, Label: s.label, Status: st, Detail: detail})
}

// fail ends s as failed, skips the steps after it and returns err.
func (r *reporter) fail(s step, err error) error {
	r.end(s, check.Failed, err.Error())
	steps := []step{stepRuntime, stepImage, stepContainer, stepReady, stepMedia, stepHeader, stepFiles}
	if r.judged {
		steps = append([]step{stepTarget}, append(steps, stepVerdict, stepSpace)...)
	}
	after := false
	for _, next := range steps {
		if after {
			r.end(next, check.Skipped, "needs: "+strings.ToLower(s.label))
		}
		after = after || next == s
	}
	return err
}

// inspector runs inspections; tests replace its fields.
type inspector struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	run      runner
	connect  func(ctx context.Context, addr, password string) (querier, error)
	password func() string
	// poll is the pause between readiness attempts, ready the deadline.
	poll, ready time.Duration
	// cleanup bounds the removal of the container.
	cleanup time.Duration
}

func newInspector() *inspector {
	return &inspector{
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		run:      execRunner,
		connect:  connectSQL,
		// Random for every container, passed only through the environment of
		// the runtime: upper case and digits, plus the lower case letter and
		// the symbol the SQL Server password policy asks for.
		password: func() string { return "a!" + rand.Text() },
		poll:     time.Second,
		ready:    3 * time.Minute,
		cleanup:  30 * time.Second,
	}
}

// Run inspects the backups of req, reporting each step through emit. An error
// means the inspection could not run (no runtime, image missing, SQL Server
// not starting, target lookup failed); a backup that cannot be read is in
// the result, with outcome "error".
func Run(ctx context.Context, req Request, emit check.Emitter) (*Result, error) {
	return newInspector().inspect(ctx, req, emit)
}

func (in *inspector) inspect(ctx context.Context, req Request, emit check.Emitter) (*Result, error) {
	paths, err := ResolvePaths(req.Paths)
	if err != nil {
		return nil, err
	}
	image := req.Image
	if image == "" {
		image = in.getenv(EnvImage)
	}
	if image == "" {
		image = DefaultImage
	}
	if err := ValidateImage(image); err != nil {
		return nil, err
	}
	r := &reporter{emit: emit, judged: req.Target != nil}
	res := &Result{Image: image, Backups: []Backup{}}

	if req.Target != nil {
		r.start(stepTarget)
		t, err := req.Target(ctx)
		if err != nil {
			return nil, r.fail(stepTarget, err)
		}
		res.Target = &t
		r.end(stepTarget, check.OK, t.describe())
	}

	r.start(stepRuntime)
	rt, err := in.detectRuntime(ctx)
	if err != nil {
		return nil, r.fail(stepRuntime, err)
	}
	res.Runtime = rt.Name + " " + rt.Version
	r.end(stepRuntime, check.OK, rt.String())

	r.start(stepImage)
	if err := in.imageExists(ctx, rt, image); err != nil {
		return nil, r.fail(stepImage, err)
	}
	r.end(stepImage, check.OK, image)

	r.start(stepContainer)
	c := &container{in: in, rt: rt, name: "dialga-inspect-" + strings.ToLower(rand.Text()[:12])}
	defer func() {
		r.start(stepCleanup)
		if err := c.remove(ctx); err != nil {
			r.end(stepCleanup, check.Warning, fmt.Sprintf("%v: remove it with %s rm -f %s", err, rt.Name, c.name))
			return
		}
		r.end(stepCleanup, check.OK, c.name+" removed")
	}()
	disks := make([]mount, len(paths))
	for i, p := range paths {
		disks[i] = mount{host: p, container: fmt.Sprintf("/bak/%d.bak", i+1), readOnly: true}
	}
	password := in.password()
	addr, err := c.start(ctx, image, disks, password)
	if err != nil {
		return nil, r.fail(stepContainer, err)
	}
	r.end(stepContainer, check.OK, fmt.Sprintf("%s on %s, removed at the end", c.name, addr))

	r.start(stepReady)
	began := time.Now()
	q, version, err := c.waitReady(ctx, addr, password)
	if err != nil {
		return nil, r.fail(stepReady, err)
	}
	defer func() { _ = q.Close() }()
	res.Server = version
	r.end(stepReady, check.OK, fmt.Sprintf("SQL Server %s after %s", version, time.Since(began).Round(time.Second)))

	names := make(map[string]string, len(paths)) // container path → file name, for the messages
	for _, d := range disks {
		names[d.container] = filepath.Base(d.host)
	}
	reader := Product(majorOf(version))

	r.start(stepMedia)
	var probs []string
	res.Backups, probs, err = readMedia(ctx, q, disks, names, reader)
	if err != nil {
		return nil, r.fail(stepMedia, err)
	}
	r.end(stepMedia, stepStatus(probs), withProblems(mediaDetail(len(paths), res.Backups), probs))

	r.start(stepHeader)
	if probs, err = readHeaders(ctx, q, res.Backups, names, reader); err != nil {
		return nil, r.fail(stepHeader, err)
	}
	r.end(stepHeader, stepStatus(probs), withProblems(headerDetail(res.Backups), probs))

	r.start(stepFiles)
	if probs, err = readFiles(ctx, q, res.Backups, names, reader); err != nil {
		return nil, r.fail(stepFiles, err)
	}
	for _, b := range res.Backups {
		res.RequiredBytes += b.RequiredBytes
	}
	r.end(stepFiles, stepStatus(probs), withProblems(filesDetail(res.Backups, res.RequiredBytes), probs))

	if res.Target == nil {
		for i := range res.Backups {
			if res.Backups[i].Outcome == "" {
				res.Backups[i].Outcome = Read
			}
		}
		res.Outcome = overall(res)
		return res, nil
	}

	r.start(stepVerdict)
	for i := range res.Backups {
		if res.Backups[i].Outcome == "" {
			judge(&res.Backups[i], *res.Target)
		}
	}
	st, detail := verdictDetail(res.Backups, res.Target.Name)
	r.end(stepVerdict, st, detail)

	r.start(stepSpace)
	space := judgeSpace(res.RequiredBytes, *res.Target)
	res.Space = &space
	r.end(stepSpace, space.Status, space.Message)

	res.Outcome = overall(res)
	return res, nil
}

// overall: an unreadable backup makes the whole inspection an error, then any
// backup or the space that rules the import out.
func overall(res *Result) Outcome {
	out := Restorable
	if res.Target == nil {
		out = Read
	}
	for _, b := range res.Backups {
		switch b.Outcome {
		case Unreadable:
			return Unreadable
		case NotRestorable:
			out = NotRestorable
		}
	}
	if res.Space != nil && res.Space.Status == check.Failed {
		out = NotRestorable
	}
	return out
}

// ResolvePaths checks that every path is a readable regular file and returns
// their absolute, symlink-free form.
func ResolvePaths(paths []string) ([]string, error) {
	switch {
	case len(paths) == 0:
		return nil, errors.New("no .bak file given")
	case len(paths) > MaxFiles:
		return nil, fmt.Errorf("too many files: %d, at most %d", len(paths), MaxFiles)
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]string, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err == nil {
			abs, err = filepath.EvalSymlinks(abs)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, unwrapPathError(err))
		}
		// The runtime takes the bind mount as source:target:options.
		if strings.ContainsAny(abs, ":\n\r\x00") {
			return nil, fmt.Errorf("%s: the path cannot contain ':' or control characters: rename or move the file", p)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, unwrapPathError(err))
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: not a regular file", p)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, unwrapPathError(err))
		}
		_ = f.Close()
		if prev, dup := seen[abs]; dup {
			return nil, fmt.Errorf("%s and %s are the same file", prev, p)
		}
		seen[abs] = p
		out = append(out, abs)
	}
	return out, nil
}

// unwrapPathError drops the path a *PathError repeats.
func unwrapPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
