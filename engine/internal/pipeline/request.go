// Package pipeline turns an import or an export of several databases into a
// job of the scheduler (internal/jobs), each database going through its
// steps:
//   - import: upload (S3 transfer), restore and wait (one native RDS task),
//     post (the read-only post-restore report; not with NoRecovery);
//   - export: export and wait (one native RDS task), download.
//
// Build only reads: it runs the pre-flight checks of the transfer and ops
// plan builders for every database and returns a Plan to preview (the
// dry-run, the default) or to start, after the confirmation it asks for,
// once per job. A database whose checks fail refuses the job, unless the
// request skips the failing databases.
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// MaxItems is the most databases of one job.
const MaxItems = 50

// MaxStripes is the most files of one backup RDS restores.
const MaxStripes = 10

// ErrInvalidRequest marks the errors of the request itself, as opposed to
// the failures of AWS or SQL Server.
var ErrInvalidRequest = errors.New("invalid job request")

type requestError struct{ msg string }

func (e requestError) Error() string        { return e.msg }
func (e requestError) Is(target error) bool { return target == ErrInvalidRequest }

func invalidf(format string, args ...any) error {
	return requestError{fmt.Sprintf(format, args...)}
}

// Request is a job to plan on one target: the databases of an import or of
// an export.
type Request struct {
	Kind   jobs.Kind
	Import []ImportItem
	Export []ExportItem
	// Prefix replaces the import prefix of the target for the uploads (""
	// is the root of the bucket); nil keeps it. An item Key wins.
	Prefix *string
	// SkipFailing leaves out the databases whose pre-flight failed instead
	// of refusing the job.
	SkipFailing bool
	// Stamp (yyyymmddhhmmss, UTC) dates the names a replace creates; "" is
	// now. Plan.Stamp is the one a preview chose: send it back to start the
	// job previewed.
	Stamp string
}

// ImportItem is a backup to upload and restore.
type ImportItem struct {
	// Files are the local backup, absolute paths: one .bak, or the stripes
	// of one backup (up to 10), restored together with a "*" key.
	Files    []string
	Database string
	// Key is the S3 key of the upload, one file only (default
	// <prefix>/<file name>).
	Key string
	// Differential restores onto the database left RESTORING by a full
	// restore with NoRecovery.
	Differential bool
	// NoRecovery leaves the database RESTORING (a differential follows):
	// no post-restore report then.
	NoRecovery bool
	// Overwrite replaces the objects at the keys, if there are any.
	Overwrite bool
	// Replace restores onto a database that exists: "auto", "swap" or "drop"
	// (ops.ReplaceOptions); "" refuses it. BackupFirst, for drop, backs the
	// current database up to S3 first; nil means true.
	Replace     string
	BackupFirst *bool
}

// ExportItem is a database to back up and download.
type ExportItem struct {
	Database string
	// Key is the S3 key of the backup (default
	// <export prefix>/<database>_<UTC timestamp>.bak).
	Key string
	// Destination is the local file, or an existing folder, an absolute
	// path; "" leaves the backup in S3 (no download).
	Destination string
	// Overwrite replaces the local file, if there is one.
	Overwrite bool
}

// Validate checks the request without reaching the target.
func (r Request) Validate(t config.Target) error {
	n := 0
	switch r.Kind {
	case jobs.Import:
		if len(r.Export) > 0 {
			return invalidf("an import has import items only")
		}
		n = len(r.Import)
	case jobs.Export:
		if len(r.Import) > 0 {
			return invalidf("an export has export items only")
		}
		n = len(r.Export)
	default:
		return invalidf("kind must be import or export (got %q)", r.Kind)
	}
	switch {
	case n == 0:
		return invalidf("no databases in the job")
	case n > MaxItems:
		return invalidf("%d databases: a job takes %d at most", n, MaxItems)
	}
	seen := map[string]bool{}
	database := func(db string) error {
		if err := validate.DBName(db); err != nil {
			return requestError{err.Error()}
		}
		// SQL Server names are case-insensitive with the default collation.
		if seen[strings.ToLower(db)] {
			return invalidf("database %s is in the job twice", db)
		}
		seen[strings.ToLower(db)] = true
		return nil
	}
	if r.Prefix != nil && r.Kind != jobs.Import {
		return invalidf("a prefix is for imports")
	}
	if r.Prefix != nil && *r.Prefix != "" {
		// Checked here, so a bad prefix fails the request, not item by item.
		if err := validate.S3Key(*r.Prefix + "/x"); err != nil {
			return invalidf("invalid prefix %q: %v", *r.Prefix, err)
		}
	}
	for _, it := range r.Import {
		if err := database(it.Database); err != nil {
			return err
		}
		switch n := len(it.Files); {
		case n == 0:
			return invalidf("%s: no backup file", it.Database)
		case n > MaxStripes:
			return invalidf("%s: %d files, RDS restores a backup of %d files at most", it.Database, n, MaxStripes)
		case n > 1 && it.Key != "":
			return invalidf("%s: a key names one file: a striped backup goes under the prefix", it.Database)
		}
		over := ops.ReplaceOptions{Mode: it.Replace, BackupFirst: it.BackupFirst, Stamp: r.Stamp}
		if err := ops.ValidateReplace(over, mssql.RestoreOptions{Differential: it.Differential, NoRecovery: it.NoRecovery}); err != nil {
			return invalidf("%s: %v", it.Database, err)
		}
		files := map[string]bool{}
		for _, f := range it.Files {
			if !filepath.IsAbs(f) {
				return invalidf("%s: the backup file must be an absolute path (got %q)", it.Database, f)
			}
			if files[f] {
				return invalidf("%s: %s is listed twice", it.Database, f)
			}
			files[f] = true
		}
	}
	for _, it := range r.Export {
		if err := database(it.Database); err != nil {
			return err
		}
		if it.Destination != "" && !filepath.IsAbs(it.Destination) {
			return invalidf("%s: the destination must be an absolute path (got %q)", it.Database, it.Destination)
		}
		if err := ops.Validate(ops.OpExport, ops.Params{Database: it.Database, Key: it.Key}, t); err != nil {
			return requestError{fmt.Sprintf("%s: %v", it.Database, err)}
		}
	}
	return nil
}

// File is a job plan in YAML, the input of "dialga run" (and, later, what
// the GUI saves with "Save as plan"):
//
//	apiVersion: v1
//	kind: Import                 # or Export
//	target: dev-sql
//	prefix: import/2026-10       # optional, Import: instead of the import prefix
//	skipFailing: false
//	replace: auto                # optional: the default of the items (so is backupFirst)
//	items:
//	  - file: backups/sales.bak  # Import: relative to the plan file
//	    database: sales
//	    key: import/sales.bak    # optional
//	    differential: false      # optional
//	    noRecovery: false        # optional
//	    overwrite: false         # optional: replace the object in S3
//	    replace: auto            # optional: restore onto an existing database (auto, swap, drop)
//	    backupFirst: true        # optional: drop only, back the database up to S3 first (default true)
//	  - files: [crm_1.bak, crm_2.bak]  # a striped backup
//	    database: crm
//	  - database: hr             # Export
//	    destination: ./exports   # optional: a folder or a file
//	    key: export/hr.bak       # optional
//	    overwrite: false         # optional: replace the local file
//
// Unknown keys are errors, so a typo never silently falls back to a default.
type File struct {
	APIVersion  string  `yaml:"apiVersion"`
	Kind        string  `yaml:"kind"`
	Target      string  `yaml:"target"`
	Prefix      *string `yaml:"prefix"`
	SkipFailing bool    `yaml:"skipFailing"`
	// Replace and BackupFirst are the default of the items that give none.
	Replace     string     `yaml:"replace"`
	BackupFirst *bool      `yaml:"backupFirst"`
	Items       []FileItem `yaml:"items"`
}

// FileItem is one database of a File.
type FileItem struct {
	File         string   `yaml:"file"`
	Files        []string `yaml:"files"`
	Database     string   `yaml:"database"`
	Key          string   `yaml:"key"`
	Differential bool     `yaml:"differential"`
	NoRecovery   bool     `yaml:"noRecovery"`
	Overwrite    bool     `yaml:"overwrite"`
	Destination  string   `yaml:"destination"`
	Replace      string   `yaml:"replace"`
	BackupFirst  *bool    `yaml:"backupFirst"`
}

// MaxFileSize is the largest plan file ReadFile and ParseFile take.
const MaxFileSize = 1 << 20

// The kinds of a File.
const (
	FileImport = "Import"
	FileExport = "Export"
)

// ReadFile reads a plan file; relative paths in it are relative to its folder.
func ReadFile(path string) (File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = fh.Close() }()
	data, err := io.ReadAll(io.LimitReader(fh, MaxFileSize+1))
	if err != nil {
		return File{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return File{}, err
	}
	f, err := ParseFile(data, filepath.Dir(abs))
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// ParseFile decodes a plan file and makes its paths absolute against base.
func ParseFile(data []byte, base string) (File, error) {
	if len(data) > MaxFileSize {
		return File{}, invalidf("the plan file is larger than %d KiB", MaxFileSize>>10)
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return File{}, invalidf("invalid YAML: %v", err)
	}
	if f.APIVersion != config.APIVersion {
		return File{}, invalidf("apiVersion is %q, this build reads %q", f.APIVersion, config.APIVersion)
	}
	if f.Kind != FileImport && f.Kind != FileExport {
		return File{}, invalidf("kind must be %s or %s (got %q)", FileImport, FileExport, f.Kind)
	}
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	for i, it := range f.Items {
		where := fmt.Sprintf("item %d", i+1)
		if it.Database != "" {
			where += " (" + it.Database + ")"
		}
		switch {
		case f.Kind == FileImport && it.File == "" && len(it.Files) == 0:
			return File{}, invalidf("%s: an import item needs file (or files)", where)
		case f.Kind == FileImport && it.File != "" && len(it.Files) > 0:
			return File{}, invalidf("%s: give file or files, not both", where)
		case f.Kind == FileImport && it.Destination != "":
			return File{}, invalidf("%s: destination is for export items", where)
		case f.Kind == FileExport && (it.File != "" || len(it.Files) > 0 || it.NoRecovery || it.Differential || it.Replace != "" || it.BackupFirst != nil):
			return File{}, invalidf("%s: file, files, noRecovery, differential, replace and backupFirst are for import items", where)
		case f.Kind == FileExport && f.Prefix != nil:
			return File{}, invalidf("prefix is for imports")
		case f.Kind == FileExport && (f.Replace != "" || f.BackupFirst != nil):
			return File{}, invalidf("replace and backupFirst are for imports")
		}
		f.Items[i].File, f.Items[i].Destination = abs(it.File), abs(it.Destination)
		for k, file := range it.Files {
			f.Items[i].Files[k] = abs(file)
		}
	}
	return f, nil
}

// Request is the job the file describes (the target aside).
func (f File) Request() Request {
	r := Request{SkipFailing: f.SkipFailing}
	if f.Kind == FileExport {
		r.Kind = jobs.Export
		for _, it := range f.Items {
			r.Export = append(r.Export, ExportItem{Database: it.Database, Key: it.Key, Destination: it.Destination, Overwrite: it.Overwrite})
		}
		return r
	}
	r.Kind, r.Prefix = jobs.Import, f.Prefix
	for _, it := range f.Items {
		files := it.Files
		if it.File != "" {
			files = []string{it.File}
		}
		item := ImportItem{Files: files, Database: it.Database, Key: it.Key,
			Differential: it.Differential, NoRecovery: it.NoRecovery, Overwrite: it.Overwrite,
			Replace: f.Replace, BackupFirst: f.BackupFirst}
		if it.Replace != "" {
			item.Replace = it.Replace
		}
		if it.BackupFirst != nil {
			item.BackupFirst = it.BackupFirst
		}
		r.Import = append(r.Import, item)
	}
	return r
}
