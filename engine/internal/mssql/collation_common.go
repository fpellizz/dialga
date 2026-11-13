package mssql

import (
	"context"
	"database/sql"
	"errors"
)

// The reads of the collation views and operations that every work package
// uses (collation design, step 0): frozen.

// ServerCollation reads the collation of the instance, of tempdb and of
// model, and the version of SQL Server.
func (s Session) ServerCollation(ctx context.Context) (ServerCollation, error) {
	var c ServerCollation
	var tempdb, model sql.NullString
	var major sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT CONVERT(nvarchar(128), SERVERPROPERTY('Collation')),
		(SELECT collation_name FROM sys.databases WHERE name = N'tempdb'),
		(SELECT collation_name FROM sys.databases WHERE name = N'model'),
		CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion')),
		CONVERT(int, SERVERPROPERTY('ProductMajorVersion'))`).Scan(&c.Server, &tempdb, &model, &c.ProductVersion, &major)
	if err != nil {
		return ServerCollation{}, err
	}
	c.Tempdb, c.Model, c.Major = tempdb.String, model.String, int(major.Int64)
	return c, nil
}

// Collation reads one collation of sys.fn_helpcollations; found is false
// when the instance does not know name.
func (s Session) Collation(ctx context.Context, name string) (CollationInfo, bool, error) {
	var n, desc string
	var codePage, version, style sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT name, description, CONVERT(int, COLLATIONPROPERTY(name, 'CodePage')),
		CONVERT(int, COLLATIONPROPERTY(name, 'Version')), CONVERT(int, COLLATIONPROPERTY(name, 'ComparisonStyle'))
		FROM sys.fn_helpcollations() WHERE name = @name`, sql.Named("name", name)).Scan(&n, &desc, &codePage, &version, &style)
	if errors.Is(err, sql.ErrNoRows) {
		return CollationInfo{}, false, nil
	}
	if err != nil {
		return CollationInfo{}, false, err
	}
	info := TraitsFromStyle(n, int(style.Int64))
	info.Description, info.CodePage, info.Version = desc, int(codePage.Int64), int(version.Int64)
	return info, true, nil
}

// CollationState reads db as set-collation and convert-columns need it:
// Exists is false when the instance has no database with that name. It
// reads only sys.databases and sys.database_mirroring (catalog views): no
// VIEW SERVER STATE needed, unlike sys.dm_hadr_database_replica_states.
func (s Session) CollationState(ctx context.Context, db string) (CollationState, error) {
	var st CollationState
	err := s.DB.QueryRowContext(ctx, `SELECT d.name, ISNULL(d.collation_name, N''), d.state_desc, d.containment_desc,
		d.user_access_desc, d.is_read_only,
		CONVERT(bit, ISNULL(HAS_PERMS_BY_NAME(d.name, N'DATABASE', N'ALTER'), 0)),
		CONVERT(bit, CASE WHEN d.replica_id IS NOT NULL OR d.group_database_id IS NOT NULL THEN 1 ELSE 0 END),
		CONVERT(bit, CASE WHEN EXISTS (SELECT 1 FROM sys.database_mirroring m
			WHERE m.database_id = d.database_id AND m.mirroring_guid IS NOT NULL) THEN 1 ELSE 0 END)
		FROM sys.databases d WHERE d.name = @db`, sql.Named("db", db)).Scan(
		&st.Name, &st.Collation, &st.State, &st.Containment, &st.UserAccess, &st.ReadOnly,
		&st.CanAlter, &st.AvailabilityReplica, &st.Mirrored)
	if errors.Is(err, sql.ErrNoRows) {
		return CollationState{}, nil
	}
	if err != nil {
		return CollationState{}, err
	}
	st.Exists = true
	return st, nil
}
