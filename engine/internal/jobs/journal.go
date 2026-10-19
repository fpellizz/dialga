package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ErrLocked means another process owns the job: it runs it (or resumes it)
// right now.
var ErrLocked = errors.New("the job is in use by another Dialga process")

// idPattern is the shape of the job ids (newID): also the journal file names.
var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// ValidID reports whether id can name a job.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// DefaultDir is the journal directory: $XDG_STATE_HOME/dialga/jobs, or
// ~/.local/state/dialga/jobs, on Linux; <user config dir>/dialga/jobs
// elsewhere.
func DefaultDir() (string, error) {
	return defaultDir(runtime.GOOS, os.Getenv, os.UserHomeDir, os.UserConfigDir)
}

func defaultDir(goos string, getenv func(string) string, home, config func() (string, error)) (string, error) {
	if goos == "linux" {
		// The XDG spec says to ignore a relative path.
		if s := getenv("XDG_STATE_HOME"); s != "" && filepath.IsAbs(s) {
			return filepath.Join(s, "dialga", "jobs"), nil
		}
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("job journal: %w", err)
		}
		return filepath.Join(h, ".local", "state", "dialga", "jobs"), nil
	}
	c, err := config()
	if err != nil {
		return "", fmt.Errorf("job journal: %w", err)
	}
	return filepath.Join(c, "dialga", "jobs"), nil
}

// journal keeps one JSON file per job, written atomically (temporary file,
// fsync, rename), readable by the user only.
//
// Several processes share the directory: the engine of the app and every CLI
// run that starts a job. They never write the same file, because a job is
// written only by the process that runs it, and that process holds the lock
// of the job (flock on .<id>.lock) from the moment it takes its id until the
// job ends. The kernel drops the lock if the process dies, so a job file
// that has not ended and whose lock is free is an orphan: the process that
// finds it marks it interrupted (while holding the lock, so only one does).
// A job file that has not ended and whose lock is held is running in another
// process: it is listed and followed, never touched. Resuming a job takes
// its lock too, so the app and the CLI cannot both resume it.
type journal struct {
	dir string
}

// staleTmp is how old a temporary file is before it is taken for a leftover
// of a crash: another process may be writing a younger one right now.
const staleTmp = time.Minute

func openJournal(dir string) (*journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("job journal: %w", err)
	}
	// Leftovers of a write cut short by a crash: the job file before it is
	// still whole.
	tmps, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	for _, f := range tmps {
		if info, err := os.Stat(f); err == nil && time.Since(info.ModTime()) > staleTmp {
			_ = os.Remove(f)
		}
	}
	return &journal{dir: dir}, nil
}

func (j *journal) path(id string) string { return filepath.Join(j.dir, id+".json") }

func (j *journal) lockPath(id string) string { return filepath.Join(j.dir, "."+id+".lock") }

// lock takes the lock of the job: ErrLocked if another process holds it.
func (j *journal) lock(id string) (release func() error, err error) {
	return lockFile(j.lockPath(id))
}

// claim takes the lock of a new job id, which must not name a job already.
func (j *journal) claim(id string) (release func() error, ok bool) {
	release, err := j.lock(id)
	if err != nil {
		return nil, false
	}
	if _, err := os.Stat(j.path(id)); err == nil {
		_ = release()
		return nil, false
	}
	return release, true
}

func (j *journal) write(id string, data []byte) (err error) {
	f, err := os.CreateTemp(j.dir, "."+id+".*.tmp") // mode 0600
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), j.path(id)); err != nil {
		return err
	}
	// The rename itself survives a power cut once the directory is synced
	// (not possible on Windows: best effort).
	if d, derr := os.Open(j.dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// remove deletes the job file and, unless a process holds it, its lock file.
func (j *journal) remove(id string) error {
	err := os.Remove(j.path(id))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if release, lerr := j.lock(id); lerr == nil {
		_ = os.Remove(j.lockPath(id))
		_ = release()
	}
	return err
}

// read reads the file of one job.
func (j *journal) read(id string) (Job, error) {
	data, err := os.ReadFile(j.path(id))
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return Job{}, fmt.Errorf("%s.json: %w", id, err)
	}
	if job.ID != id {
		return Job{}, fmt.Errorf("%s.json: holds job %q", id, job.ID)
	}
	if job.Origin == "" {
		job.Origin = OriginApp // journals of older versions
	}
	return job, nil
}

// load reads every job file. A file that cannot be read is reported and
// left in place, for a person to look at.
func (j *journal) load() ([]Job, []error) {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, []error{err}
	}
	var (
		list []Job
		errs []error
	)
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !ValidID(id) {
			continue
		}
		job, err := j.read(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		list = append(list, job)
	}
	return list, errs
}

// ReadJournal reads the jobs in dir without taking a lock or changing a
// file, so it works while an engine runs ("dialga jobs"). A job that has
// not ended and whose process is gone was interrupted: the next engine to
// start marks it so. A missing dir means no jobs yet.
func ReadJournal(dir string) ([]Job, []error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return (&journal{dir: dir}).load()
}
