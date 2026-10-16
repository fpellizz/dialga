package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrNoDatabase means the instance has no database with that name.
var ErrNoDatabase = errors.New("database not found on the instance")

// DatabaseInfo is the row of sys.databases a post-restore report shows.
type DatabaseInfo struct {
	Name               string `json:"name"`
	State              string `json:"state"`
	RecoveryModel      string `json:"recoveryModel"`
	CompatibilityLevel int    `json:"compatibilityLevel"`
	// Collation is empty while the database is not online.
	Collation  string `json:"collation,omitempty"`
	Owner      string `json:"owner,omitempty"`
	UserAccess string `json:"userAccess"`
	ReadOnly   bool   `json:"readOnly"`
}

// DatabaseFile is a data or log file of a database (sys.master_files).
type DatabaseFile struct {
	Type   string  `json:"type"`
	Name   string  `json:"name"`
	SizeMB float64 `json:"sizeMB"`
	// MaxSizeMB is absent when the file grows without a limit.
	MaxSizeMB    *float64 `json:"maxSizeMB,omitempty"`
	PhysicalName string   `json:"physicalName"`
}

// ObjectCount is the number of user objects of one type.
type ObjectCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// DatabaseReport is what mssql-import.sh post reads about a database.
type DatabaseReport struct {
	Database DatabaseInfo
	Files    []DatabaseFile
	// Access reports whether the login can enter the database; without it
	// (or while the database is not online) the fields below stay empty.
	Access bool
	// OrphanUsers are SQL users whose login does not exist on the instance.
	OrphanUsers []string
	// WindowsPrincipals counts Windows users and groups, unusable on RDS
	// without Active Directory.
	WindowsPrincipals int
	Objects           []ObjectCount
}

// DatabaseReport reads the report of db, or ErrNoDatabase. It only reads.
func (s Session) DatabaseReport(ctx context.Context, db string) (DatabaseReport, error) {
	r := DatabaseReport{Files: []DatabaseFile{}, OrphanUsers: []string{}, Objects: []ObjectCount{}}
	var collation, owner sql.NullString
	d := &r.Database
	err := s.DB.QueryRowContext(ctx, `SELECT name, state_desc, recovery_model_desc, compatibility_level,
		collation_name, SUSER_SNAME(owner_sid), user_access_desc, is_read_only
		FROM sys.databases WHERE name = @db`, sql.Named("db", db)).
		Scan(&d.Name, &d.State, &d.RecoveryModel, &d.CompatibilityLevel, &collation, &owner, &d.UserAccess, &d.ReadOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return DatabaseReport{}, fmt.Errorf("%s: %w", db, ErrNoDatabase)
	}
	if err != nil {
		return DatabaseReport{}, err
	}
	d.Collation, d.Owner = collation.String, owner.String
	if r.Files, err = s.databaseFiles(ctx, db); err != nil {
		return DatabaseReport{}, err
	}
	if d.State != "ONLINE" {
		return r, nil
	}
	var access sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT HAS_DBACCESS(@db)`, sql.Named("db", db)).Scan(&access); err != nil {
		return DatabaseReport{}, err
	}
	if r.Access = access.Int64 == 1; !r.Access {
		return r, nil
	}
	// The database name cannot be a parameter of a three-part name: it is
	// quoted as an identifier instead (callers validate it first anyway).
	q := quoteIdent(db)
	if r.OrphanUsers, err = s.strings(ctx, `SELECT dp.name FROM `+q+`.sys.database_principals dp
		LEFT JOIN sys.server_principals sp ON sp.sid = dp.sid
		WHERE dp.type = 'S' AND dp.authentication_type = 1 AND dp.principal_id > 4
		  AND sp.sid IS NULL ORDER BY dp.name`); err != nil {
		return DatabaseReport{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+q+`.sys.database_principals
		WHERE type IN ('U', 'G') AND principal_id > 4`).Scan(&r.WindowsPrincipals); err != nil {
		return DatabaseReport{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT type_desc, COUNT(*) AS n FROM `+q+`.sys.objects
		WHERE is_ms_shipped = 0 GROUP BY type_desc ORDER BY n DESC, type_desc`)
	if err != nil {
		return DatabaseReport{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var o ObjectCount
		if err := rows.Scan(&o.Type, &o.Count); err != nil {
			return DatabaseReport{}, err
		}
		r.Objects = append(r.Objects, o)
	}
	return r, rows.Err()
}

func (s Session) databaseFiles(ctx context.Context, db string) ([]DatabaseFile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT type_desc, name, size, max_size, physical_name
		FROM sys.master_files WHERE database_id = DB_ID(@db) ORDER BY type, file_id`, sql.Named("db", db))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := []DatabaseFile{}
	for rows.Next() {
		var f DatabaseFile
		var pages, maxPages int64
		if err := rows.Scan(&f.Type, &f.Name, &pages, &maxPages, &f.PhysicalName); err != nil {
			return nil, err
		}
		f.SizeMB = pagesToMB(pages)
		if maxPages != -1 {
			mb := pagesToMB(maxPages)
			f.MaxSizeMB = &mb
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s Session) strings(ctx context.Context, query string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	list := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// pagesToMB converts 8 KiB pages to MiB, rounded to one decimal.
func pagesToMB(pages int64) float64 { return math.Round(float64(pages)*10/128) / 10 }

// quoteIdent quotes name as a T-SQL identifier, like QUOTENAME.
func quoteIdent(name string) string { return "[" + strings.ReplaceAll(name, "]", "]]") + "]" }
