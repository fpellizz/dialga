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
	"unicode"
	"unicode/utf8"
)

var (
	dbNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,99}$`)
	s3KeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._/*+=@-]+$`)
	// derived names are a valid name plus a suffix: the same characters, up
	// to the 128 of a SQL Server identifier.
	derivedNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
	stampPattern       = regexp.MustCompile(`^[0-9]{14}$`)
	// SQL logins and database users: the pattern of the OpenAPI contract
	// (login, user), with "." for names like first.last.
	principalPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	// collations: a name as sys.fn_helpcollations gives it. It is
	// interpolated in COLLATE clauses, so nothing else is accepted.
	collationPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

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

// PrincipalName reports whether name can be the name of a SQL login or of a
// database user: letters, digits, "_", "." and "-", at most 128 characters,
// not starting with "." or "-". It does not look at which names are
// reserved (sa, dbo...): the access operations do.
func PrincipalName(name string) error {
	if !principalPattern.MatchString(name) {
		return fmt.Errorf("invalid login or user name %q: use letters, digits, _ . and -, max 128 characters, not starting with . or -", name)
	}
	return nil
}

// CollationName reports whether name can be a collation name as
// sys.fn_helpcollations gives it: a letter, then letters, digits and "_", at
// most 128 characters. Collations are interpolated in COLLATE clauses (they
// cannot be quoted), so nothing else is accepted; whether the instance knows
// the collation is checked on the instance.
func CollationName(name string) error {
	if !collationPattern.MatchString(name) {
		return fmt.Errorf("invalid collation name %q: use letters, digits and _, starting with a letter, max 128 characters", name)
	}
	return nil
}

// ObjectName reports whether name can be a schema, table or column name the
// user typed: 1 to 128 characters, no control characters. Such names are
// always quoted as identifiers, never interpolated as they are.
func ObjectName(name string) error {
	n := utf8.RuneCountInString(name)
	switch {
	case !utf8.ValidString(name):
		return fmt.Errorf("invalid object name %q: not valid UTF-8", name)
	case n == 0:
		return fmt.Errorf("invalid object name: it is empty")
	case n > 128:
		return fmt.Errorf("invalid object name %q: max 128 characters", name)
	case strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("invalid object name %q: no control characters allowed", name)
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
