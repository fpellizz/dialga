package mssql

import (
	"context"
	"database/sql"
)

// The read-only collation views (collation design §4.1). Nothing here
// changes anything.

// DatabaseCollations lists the user databases with their default collation,
// state and containment (system databases and rdsadmin left out).
func (s Session) DatabaseCollations(ctx context.Context) ([]DatabaseCollation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.name, ISNULL(d.collation_name, N''), d.state_desc, d.containment_desc
		FROM sys.databases d WHERE d.database_id > 4 AND d.name <> N'rdsadmin' ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []DatabaseCollation{}
	for rows.Next() {
		var d DatabaseCollation
		if err := rows.Scan(&d.Name, &d.Collation, &d.State, &d.Containment); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Collations lists every collation of sys.fn_helpcollations, by name, with
// its traits.
func (s Session) Collations(ctx context.Context) ([]CollationInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, description, CONVERT(int, COLLATIONPROPERTY(name, 'CodePage')),
		CONVERT(int, COLLATIONPROPERTY(name, 'Version')), CONVERT(int, COLLATIONPROPERTY(name, 'ComparisonStyle'))
		FROM sys.fn_helpcollations() ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []CollationInfo{}
	for rows.Next() {
		var name, desc string
		var codePage, version, style sql.NullInt64
		if err := rows.Scan(&name, &desc, &codePage, &version, &style); err != nil {
			return nil, err
		}
		info := TraitsFromStyle(name, int(style.Int64))
		info.Description, info.CodePage, info.Version = desc, int(codePage.Int64), int(version.Int64)
		out = append(out, info)
	}
	return out, rows.Err()
}

// CharColumns lists the columns of the user tables of db that have a
// collation, by schema, table and column position. db is quoted, but must
// have been validated (validate.DBName) and be ONLINE.
func (s Session) CharColumns(ctx context.Context, db string) ([]CharColumn, error) {
	q := quoteIdent(db)
	rows, err := s.DB.QueryContext(ctx, `SELECT t.object_id, c.column_id, s.name, t.name, c.name, ut.name, bt.name,
		c.max_length, c.is_nullable, c.collation_name, c.is_computed,
		CAST(c.is_sparse | c.is_column_set AS bit), ut.is_user_defined,
		CAST(CASE WHEN c.encryption_type IS NULL THEN 0 ELSE 1 END AS bit)
		FROM `+q+`.sys.columns c
		JOIN `+q+`.sys.tables  t  ON t.object_id = c.object_id AND t.is_ms_shipped = 0
		JOIN `+q+`.sys.schemas s  ON s.schema_id = t.schema_id
		JOIN `+q+`.sys.types   ut ON ut.user_type_id = c.user_type_id
		JOIN `+q+`.sys.types   bt ON bt.user_type_id = c.system_type_id
		WHERE c.collation_name IS NOT NULL
		ORDER BY s.name, t.name, c.column_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []CharColumn{}
	for rows.Next() {
		var c CharColumn
		var collation sql.NullString
		if err := rows.Scan(&c.ObjectID, &c.ColumnID, &c.Schema, &c.Table, &c.Column, &c.Type, &c.BaseType,
			&c.MaxLength, &c.Nullable, &collation, &c.Computed, &c.Sparse, &c.AliasType, &c.Encrypted); err != nil {
			return nil, err
		}
		c.Collation = collation.String
		out = append(out, c)
	}
	return out, rows.Err()
}
