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
)

// ErrLocked means another engine owns the journal directory.
var ErrLocked = errors.New("the job journal is in use by another Dialga engine")

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
type journal struct {
	dir    string
	unlock func() error
}

func openJournal(dir string) (*journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("job journal: %w", err)
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	// Leftovers of a write cut short by a crash: the job file before it is
	// still whole.
	tmps, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	for _, f := range tmps {
		_ = os.Remove(f)
	}
	return &journal{dir: dir, unlock: unlock}, nil
}

func (j *journal) path(id string) string { return filepath.Join(j.dir, id+".json") }

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

func (j *journal) remove(id string) error {
	err := os.Remove(j.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
		data, err := os.ReadFile(j.path(id))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if job.ID != id {
			errs = append(errs, fmt.Errorf("%s: holds job %q", e.Name(), job.ID))
			continue
		}
		list = append(list, job)
	}
	return list, errs
}

func (j *journal) close() error { return j.unlock() }
