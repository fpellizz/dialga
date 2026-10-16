package mssql

import (
	"context"
	"database/sql"
)

// Database is a user database of the instance, from sys.databases and the
// sizes of its files in sys.master_files.
type Database struct {
	Name string `json:"name"`
	// State is state_desc: ONLINE, RESTORING, RECOVERING, RECOVERY_PENDING,
	// SUSPECT, EMERGENCY, OFFLINE.
	State string `json:"state"`
	// Recovery is the recovery model: FULL, SIMPLE or BULK_LOGGED.
	Recovery      string `json:"recovery"`
	Compatibility int    `json:"compatibility"`
	// Collation is empty while the database is offline.
	Collation string `json:"collation,omitempty"`
	// Owner is the login of owner_sid; empty when no login has that SID.
	Owner string `json:"owner,omitempty"`
	// UserAccess is MULTI_USER, SINGLE_USER or RESTRICTED_USER.
	UserAccess string `json:"userAccess"`
	ReadOnly   bool   `json:"readOnly"`
	// CreatedAt is the server's wall clock, formatted as 2006-01-02T15:04:05.
	CreatedAt string `json:"createdAt"`
	// DataMB is the size of the data files (rows, filestream, full-text),
	// LogMB of the log files, in MiB. Both are 0 when the login cannot read
	// sys.master_files (it needs VIEW ANY DEFINITION, as the RDS master user has).
	DataMB float64 `json:"dataMB"`
	LogMB  float64 `json:"logMB"`
}

// Usage says how busy an instance is.
type Usage struct {
	// UserDatabases counts the databases Databases lists.
	UserDatabases int
	// ActiveTasks counts the native tasks not finished yet: queued,
	// running or being cancelled.
	ActiveTasks int
}

// DatabaseReader reads the user databases of an instance. Catalog
// implements it on a SQL session; tests replace it.
type DatabaseReader interface {
	Databases(ctx context.Context) ([]Database, error)
	Usage(ctx context.Context) (Usage, error)
}

// Catalog reads the databases through a SQL session.
type Catalog struct{ DB *sql.DB }

// userDatabases leaves out master, tempdb, model, msdb and the RDS one.
const userDatabases = `d.database_id > 4 AND d.name <> N'rdsadmin'`

// Databases returns the user databases, by name. Sizes are in 8 KiB pages
// in sys.master_files: 128 pages make a MiB.
func (c Catalog) Databases(ctx context.Context) ([]Database, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT d.name, d.state_desc, d.recovery_model_desc,
		d.compatibility_level, d.collation_name, SUSER_SNAME(d.owner_sid), d.user_access_desc,
		d.is_read_only, d.create_date,
		ISNULL(SUM(CASE WHEN f.type = 1 THEN 0 ELSE CAST(f.size AS bigint) END), 0),
		ISNULL(SUM(CASE WHEN f.type = 1 THEN CAST(f.size AS bigint) ELSE 0 END), 0)
		FROM sys.databases AS d
		LEFT JOIN sys.master_files AS f ON f.database_id = d.database_id
		WHERE `+userDatabases+`
		GROUP BY d.name, d.state_desc, d.recovery_model_desc, d.compatibility_level,
			d.collation_name, d.owner_sid, d.user_access_desc, d.is_read_only, d.create_date
		ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	list := []Database{}
	for rows.Next() {
		var (
			d                                         Database
			state, recovery, collation, owner, access sql.NullString
			created                                   sql.NullTime
			dataPages, logPages                       int64
		)
		if err := rows.Scan(&d.Name, &state, &recovery, &d.Compatibility, &collation, &owner, &access,
			&d.ReadOnly, &created, &dataPages, &logPages); err != nil {
			return nil, err
		}
		d.State, d.Recovery, d.Collation, d.Owner, d.UserAccess =
			state.String, recovery.String, collation.String, owner.String, access.String
		d.CreatedAt = serverTime(created)
		d.DataMB, d.LogMB = float64(dataPages)/128, float64(logPages)/128
		list = append(list, d)
	}
	return list, rows.Err()
}

// Usage counts the user databases and the native tasks not finished yet.
func (c Catalog) Usage(ctx context.Context) (Usage, error) {
	var u Usage
	err := c.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sys.databases AS d WHERE `+userDatabases+`),
		(SELECT COUNT(*) FROM msdb.dbo.rds_fn_task_status(NULL, 0)
		   WHERE lifecycle IN (N'CREATED', N'IN_PROGRESS', N'CANCEL_REQUESTED'))`).
		Scan(&u.UserDatabases, &u.ActiveTasks)
	return u, err
}
