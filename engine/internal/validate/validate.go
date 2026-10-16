// Package validate checks user-supplied names before they reach T-SQL or S3.
//
// The rules are the ones of mssql-import.sh (valid_db, valid_key), so the app
// accepts exactly what the shell scripts accept. The engine calls the rds_*
// procedures through RPC with named parameters, so these checks are not the
// only defence against injection: they keep names predictable and the S3 ARN
// usable by RDS.
package validate

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	dbNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,99}$`)
	s3KeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._/*+=@-]+$`)
	// derived names are a valid name plus a suffix: the same characters, up
	// to the 128 of a SQL Server identifier.
	derivedNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
	stampPattern       = regexp.MustCompile(`^[0-9]{14}$`)

	// System databases of SQL Server plus the RDS administration database.
	systemDatabases = map[string]bool{
		"master": true, "model": true, "msdb": true, "tempdb": true, "rdsadmin": true,
	}
)

// DBName reports whether name can be used as the target of a restore or the
// source of a backup: letters, digits, "_" and "-", at most 100 characters,
// not starting with "-", and not a system database.
func DBName(name string) error {
	if !dbNamePattern.MatchString(name) {
		return fmt.Errorf("invalid database name %q: use letters, digits, _ and -, max 100 characters", name)
	}
	if systemDatabases[strings.ToLower(name)] {
		return fmt.Errorf("%q is a system database", name)
	}
	return nil
}

// DerivedDBName reports whether name can be a name Dialga derives from a
// database name (the temporary and the old database of a swap): the same
// characters as DBName, up to 128.
func DerivedDBName(name string) error {
	if !derivedNamePattern.MatchString(name) {
		return fmt.Errorf("invalid database name %q: use letters, digits, _ and -, max 128 characters", name)
	}
	if systemDatabases[strings.ToLower(name)] {
		return fmt.Errorf("%q is a system database", name)
	}
	return nil
}

// Stamp reports whether s is a replace timestamp, yyyymmddhhmmss.
func Stamp(s string) error {
	if !stampPattern.MatchString(s) {
		return fmt.Errorf("invalid stamp %q: expected yyyymmddhhmmss", s)
	}
	return nil
}

// S3Key reports whether key can be embedded in the S3 ARN passed to the rds_*
// procedures: no spaces or quotes, no leading "/", no "..". A "*" is allowed
// for backups striped over several files.
func S3Key(key string) error {
	switch {
	case !s3KeyPattern.MatchString(key):
		return fmt.Errorf("invalid S3 key %q: no spaces or quotes allowed", key)
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("invalid S3 key %q: it must not start with /", key)
	case strings.Contains(key, ".."):
		return fmt.Errorf("invalid S3 key %q: parent directory references are not allowed", key)
	}
	return nil
}
