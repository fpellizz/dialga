package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"go.yaml.in/yaml/v3"
)

// Writing the file (dialga config init/add-target/remove-target and the GUI).
// Save renders the file again from the parsed values: the comments and the
// key order of a hand-edited file are not kept. The previous version stays
// next to it as <file>.bak.

// ErrTargetExists means a target has that name and the caller did not ask to
// replace it.
var ErrTargetExists = errors.New("target already exists")

// ErrUnknownTarget means no target has that name.
var ErrUnknownTarget = errors.New("unknown target")

// InvalidFileError means the file on disk does not parse. Update never
// overwrites it: a hand-edited file is fixed by hand first.
type InvalidFileError struct{ Err error }

func (e *InvalidFileError) Error() string {
	return e.Err.Error() + " (fix it first: Dialga does not overwrite an invalid file)"
}

func (e *InvalidFileError) Unwrap() error { return e.Err }

// fileHeader opens every file Save writes.
const fileHeader = `# Dialga configuration, written by Dialga. Comments are not kept when Dialga
# saves it: the previous version is in the .bak file next to this one.
# Every key is described in docs/config.example.yaml.
`

// writeMu serializes the changes made by this process (CLI or API).
var writeMu sync.Mutex

// renameFile is os.Rename; tests make it fail.
var renameFile = os.Rename

// ValidTargetName reports whether name can name a target.
func ValidTargetName(name string) bool { return targetNamePattern.MatchString(name) }

// ValidProfile reports whether p can name an AWS profile in the file.
func ValidProfile(p string) bool { return profilePattern.MatchString(p) }

// ValidRegion reports whether r looks like an AWS region.
func ValidRegion(r string) bool { return regionPattern.MatchString(r) }

// ValidBucket reports whether b can name an S3 bucket in the file.
func ValidBucket(b string) bool { return bucketPattern.MatchString(b) }

// SupportedPasswordSource accepts the references a new target can get:
// "prompt" (or empty), env:VAR, keyring and secretsmanager:<arn>.
func SupportedPasswordSource(ref string) error {
	if _, err := (SQL{Password: ref}).PasswordSource(); ref == "" || err == nil {
		return nil
	}
	// The value is not repeated: it could be a password typed in the wrong field.
	return errors.New("the SQL password source must be prompt, env:VAR, keyring or secretsmanager:<secret ARN>, never the password itself")
}

// Check applies the defaults to t and validates it under name, with the
// rules of Parse.
func Check(name string, t Target) (Target, error) {
	t.applyDefaults()
	var errs []error
	if !ValidTargetName(name) {
		errs = append(errs, fmt.Errorf("target %q: name must be lower case letters, digits and -", name))
	}
	if err := t.validate(); err != nil {
		errs = append(errs, err)
	}
	return t, errors.Join(errs...)
}

// SetTarget adds the target, or replaces the one with the same name when
// replace is true (ErrTargetExists otherwise). It validates the target first.
func (c *Config) SetTarget(name string, t Target, replace bool) error {
	t, err := Check(name, t)
	if err != nil {
		return err
	}
	if _, ok := c.Targets[name]; ok && !replace {
		return fmt.Errorf("%w: %s", ErrTargetExists, name)
	}
	if c.Targets == nil {
		c.Targets = map[string]Target{}
	}
	c.Targets[name] = t
	return nil
}

// RemoveTarget deletes the named target (ErrUnknownTarget if there is none).
func (c *Config) RemoveTarget(name string) error {
	if _, ok := c.Targets[name]; !ok {
		return fmt.Errorf("%w %q", ErrUnknownTarget, name)
	}
	delete(c.Targets, name)
	return nil
}

// Update reads the file at path (missing: an empty configuration), applies
// change and saves the result with Save. A file that does not parse is an
// *InvalidFileError and stays as it is. Changes from this process never
// interleave; another process writing at the same moment can still win.
func Update(path string, change func(*Config) error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	cfg, err := LoadOrEmpty(path)
	if err != nil {
		return err
	}
	if err := change(cfg); err != nil {
		return err
	}
	return Save(path, cfg)
}

// LoadOrEmpty reads the file at path like Load, but a missing file (or one
// without targets) is an empty configuration and a file that does not parse
// is an *InvalidFileError: what Update builds on.
func LoadOrEmpty(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{APIVersion: APIVersion, Targets: map[string]Target{}}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err == nil {
		return cfg, nil
	}
	if isEmptyDoc(data) {
		return &Config{APIVersion: APIVersion, Targets: map[string]Target{}}, nil
	}
	return nil, &InvalidFileError{fmt.Errorf("%s: %w", path, err)}
}

// isEmptyDoc reports whether data is a valid v1 document without targets,
// the only kind of file Parse rejects that Update can still build on.
func isEmptyDoc(data []byte) bool {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(&cfg) == nil && cfg.APIVersion == APIVersion && len(cfg.Targets) == 0
}

// Save writes c to path atomically: a temporary file in the same directory,
// synced, then renamed over the old one, so a failure leaves the old file
// intact. The file is 0600; its directory, when Save creates it, 0700. The
// previous file is first copied to path.bak. A symbolic link stays a link:
// the file it points to is replaced. With no targets left the file is
// removed (after the copy), so the next start is a first run again.
func Save(path string, c *Config) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var out []byte
	if len(c.Targets) > 0 {
		body, err := c.Marshal()
		if err != nil {
			return err
		}
		// Never write a file the next Load would reject.
		if _, err := Parse(body); err != nil {
			return fmt.Errorf("refusing to write an invalid configuration: %w", err)
		}
		out = append([]byte(fileHeader), body...)
	}
	if exists {
		if err := writeAtomic(path+".bak", old); err != nil {
			return fmt.Errorf("backup of %s: %w", path, err)
		}
	}
	if out == nil {
		if !exists {
			return nil
		}
		return os.Remove(path)
	}
	return writeAtomic(path, out)
}

func writeAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return renameFile(f.Name(), path)
}
