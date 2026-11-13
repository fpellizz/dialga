package mssql

import (
	"context"
	"database/sql"
	"strings"
)

// CollationBlocker is an object that keeps ALTER DATABASE ... COLLATE from
// running because it depends on the collation of the database.
type CollationBlocker struct {
	// Kind is schema-bound, computed-column, check-constraint,
	// table-function or memory-optimized.
	Kind   string `json:"kind"`
	Schema string `json:"schema"`
	Object string `json:"object"`
	Detail string `json:"detail,omitempty"`
}

// The kinds of CollationBlocker.
const (
	BlockerSchemaBound = "schema-bound"
	BlockerComputed    = "computed-column"
	BlockerCheck       = "check-constraint"
	BlockerTableFunc   = "table-function"
	BlockerMemoryOptim = "memory-optimized"
)

// dbPlaceholder is replaced by the quoted name of the database in the
// catalog queries (inDB).
const dbPlaceholder = "{{db}}"

// What SQL Server refuses (Msg 5075), measured by the integration tests
// (TestIntegrationSetCollationHeuristic, SQL Server 2022): every schema-bound
// view or function, every computed column, every CHECK constraint, whatever
// they refer to (a view on int columns, CHECK (qty > 0) and a computed
// column on int block too: it is not only the char columns that count), and
// the table functions that return char columns. Plain views, indexes and
// DEFAULT constraints do not block it.
//
// The queries read the catalog of db through its three-part names;
// {{db}} is replaced by [db].
var blockerQueries = []struct {
	kind  string
	query string
}{
	{BlockerSchemaBound, `SELECT s.name, o.name, o.type_desc FROM {{db}}.sys.sql_modules m
JOIN {{db}}.sys.objects o ON o.object_id = m.object_id JOIN {{db}}.sys.schemas s ON s.schema_id = o.schema_id
WHERE m.is_schema_bound = 1
ORDER BY s.name, o.name`},
	{BlockerComputed, `SELECT s.name, t.name + N'.' + cc.name, CONVERT(nvarchar(400), cc.definition) FROM {{db}}.sys.computed_columns cc
JOIN {{db}}.sys.tables t ON t.object_id = cc.object_id JOIN {{db}}.sys.schemas s ON s.schema_id = t.schema_id
ORDER BY s.name, t.name, cc.name`},
	{BlockerCheck, `SELECT s.name, t.name + N'.' + k.name, CONVERT(nvarchar(400), k.definition) FROM {{db}}.sys.check_constraints k
JOIN {{db}}.sys.tables t ON t.object_id = k.parent_object_id JOIN {{db}}.sys.schemas s ON s.schema_id = t.schema_id
ORDER BY s.name, t.name, k.name`},
	{BlockerTableFunc, `SELECT s.name, o.name, o.type_desc FROM {{db}}.sys.objects o JOIN {{db}}.sys.schemas s ON s.schema_id = o.schema_id
WHERE o.type = 'TF' AND EXISTS (SELECT 1 FROM {{db}}.sys.columns c WHERE c.object_id = o.object_id AND c.collation_name IS NOT NULL)
ORDER BY s.name, o.name`},
}

func inDB(db, query string) string {
	return strings.ReplaceAll(query, dbPlaceholder, quoteIdent(db))
}

// CollationBlockers lists the objects of db that depend on its collation
// (collation design §4.2).
func (s Session) CollationBlockers(ctx context.Context, db string) ([]CollationBlocker, error) {
	var out []CollationBlocker
	for _, bq := range blockerQueries {
		rows, err := s.DB.QueryContext(ctx, inDB(db, bq.query))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var schema, object string
			var detail sql.NullString
			if err := rows.Scan(&schema, &object, &detail); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, CollationBlocker{Kind: bq.kind, Schema: schema, Object: object, Detail: detail.String})
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var fx int
	if err := s.DB.QueryRowContext(ctx, inDB(db, `SELECT COUNT(*) FROM {{db}}.sys.filegroups WHERE type = 'FX'`)).Scan(&fx); err != nil {
		return nil, err
	}
	if fx > 0 {
		out = append(out, CollationBlocker{Kind: BlockerMemoryOptim, Object: db,
			Detail: "the database has a memory-optimized filegroup"})
	}
	return out, nil
}

// NameClashes lists (up to 20) the names of db that would collide under
// collation (collation design §4.2). collation is validated by the caller.
func (s Session) NameClashes(ctx context.Context, db, collation string) ([]string, error) {
	q := inDB(db, `SELECT * FROM (
SELECT TOP (20) N'object ' + s.name + N'.' + MIN(o.name) AS d FROM {{db}}.sys.objects o JOIN {{db}}.sys.schemas s ON s.schema_id = o.schema_id
  GROUP BY s.name, o.schema_id, o.name COLLATE <coll> HAVING COUNT(*) > 1
UNION ALL SELECT TOP (20) N'schema ' + MIN(name) FROM {{db}}.sys.schemas GROUP BY name COLLATE <coll> HAVING COUNT(*) > 1
UNION ALL SELECT TOP (20) N'column ' + MIN(c.name) FROM {{db}}.sys.columns c GROUP BY c.object_id, c.name COLLATE <coll> HAVING COUNT(*) > 1
UNION ALL SELECT TOP (20) N'index ' + MIN(i.name) FROM {{db}}.sys.indexes i WHERE i.name IS NOT NULL GROUP BY i.object_id, i.name COLLATE <coll> HAVING COUNT(*) > 1
UNION ALL SELECT TOP (20) N'principal ' + MIN(name) FROM {{db}}.sys.database_principals GROUP BY name COLLATE <coll> HAVING COUNT(*) > 1
) x`)
	q = strings.ReplaceAll(q, "<coll>", collation)
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
		if len(out) == 20 {
			break
		}
	}
	return out, rows.Err()
}
