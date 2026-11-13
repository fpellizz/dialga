// Work package C: see the collation design.

package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mssqldb "github.com/microsoft/go-mssqldb"
)

// ConversionCatalog is what convert-columns reads of a database to plan a
// conversion: one read per category, for the whole database, filtered in Go
// (collation design §4.3).
type ConversionCatalog struct {
	// Major is the major version of SQL Server (which catalog columns exist).
	Major       int
	Tables      map[int64]CatalogTable
	Columns     []CharColumn
	Indexes     []CatalogIndex
	ForeignKeys []CatalogFK
	Checks      []CatalogCheck
	Defaults    []CatalogDefault
	Stats       []CatalogStats
	Computed    []CatalogComputed
	FullText    []CatalogFullText
	SchemaBound []CatalogReference
	// ExtendedProperties are those of the constraints (class 1, minor 0) and
	// of the indexes (class 7) of user tables.
	ExtendedProperties []CatalogExtendedProperty
	// Masked are the columns with a dynamic data mask.
	Masked []ColumnKey
}

// ColumnKey names a column by its ids.
type ColumnKey struct {
	ObjectID int64
	ColumnID int
}

// CatalogTable is a user table and what keeps it from being converted.
type CatalogTable struct {
	Schema, Name    string
	MemoryOptimized bool
	Temporal        int
	Replicated      bool
	CDC             bool
	FileTable       bool
	Graph           bool
	Ledger          bool
	// ChangeTracking: the table is in sys.change_tracking_tables (it needs
	// its primary key).
	ChangeTracking bool
	Rows           int64
	ReservedMB     float64
}

// CatalogIndex is an index of a user table.
type CatalogIndex struct {
	ObjectID int64
	IndexID  int
	// Type is sys.indexes.type: 1 clustered, 2 non-clustered, 3 XML,
	// 4 spatial, 5 and 6 columnstore, 7 hash.
	Type          int
	Disabled      bool
	Hypothetical  bool
	DataSpaceType string
	// Compressions counts the different compressions of its partitions.
	Compressions int
	// ConstraintID is the object of its primary key or unique constraint
	// (0 for a plain index).
	ConstraintID int64
	Def          IndexDef
	KeyIDs       []int
	// IncludedIDs are the included columns, and any other column the index
	// lists (partitioning, columnstore).
	IncludedIDs []int
	// FilterUnreadable: has_filter is set but filter_definition is NULL (no
	// VIEW DEFINITION): it cannot be created again.
	FilterUnreadable bool
	// OptimizeSequentialKey: OPTIMIZE_FOR_SEQUENTIAL_KEY is on (SQL Server
	// 2019+), which IndexDef does not keep.
	OptimizeSequentialKey bool
}

// CatalogFK is a foreign key between user tables.
type CatalogFK struct {
	ObjectID, ParentID, RefID int64
	KeyIndexID                int
	ParentIDs, RefIDs         []int
	Def                       ForeignKeyDef
}

// CatalogCheck is a CHECK constraint.
type CatalogCheck struct {
	ObjectID       int64
	ParentID       int64
	ParentColumnID int
	// ColumnIDs are the columns of its table it reads (dependencies).
	ColumnIDs []int
	Def       CheckDef
	// NoDefinition: the definition is not readable (no VIEW DEFINITION).
	NoDefinition bool
}

// CatalogDefault is a DEFAULT constraint.
type CatalogDefault struct {
	ObjectID     int64
	ParentID     int64
	ColumnID     int
	Def          DefaultDef
	NoDefinition bool
}

// CatalogStats is a statistic that is not the one of an index.
type CatalogStats struct {
	ObjectID    int64
	StatsID     int
	AutoCreated bool
	Incremental bool
	ColumnIDs   []int
	Def         StatsDef
	// FilterUnreadable: has_filter is set but filter_definition is NULL.
	FilterUnreadable bool
	// PersistedSample and AutoDrop are options StatsDef does not keep.
	PersistedSample bool
	AutoDrop        bool
}

// CatalogComputed is a computed column and the columns it reads.
type CatalogComputed struct {
	ObjectID   int64
	ColumnID   int
	Name       string
	Definition string
	ColumnIDs  []int
}

// CatalogFullText is the full-text index of a table.
type CatalogFullText struct {
	ObjectID   int64
	KeyIndexID int
	ColumnIDs  []int
}

// CatalogReference is a schema-bound module that references a table
// (ColumnID 0) or one of its columns.
type CatalogReference struct {
	ReferencedID int64
	ColumnID     int
	Schema, Name string
	Type         string
}

// CatalogExtendedProperty is an extended property of a constraint (Class 1,
// MajorID the constraint) or of an index (Class 7, MajorID the table,
// MinorID the index). BaseType is the type of its sql_variant value ("" for
// NULL).
type CatalogExtendedProperty struct {
	Class    int
	MajorID  int64
	MinorID  int
	Name     string
	Value    string
	BaseType string
}

// ConversionCatalog reads what convert-columns needs of db (collation design
// §4.3). db must be validated: it is quoted, not a parameter.
func (s Session) ConversionCatalog(ctx context.Context, db string) (ConversionCatalog, error) {
	q := quoteIdent(db)
	cat := ConversionCatalog{Tables: map[int64]CatalogTable{}}
	var major sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT CONVERT(int, SERVERPROPERTY('ProductMajorVersion'))`).Scan(&major); err != nil {
		return cat, err
	}
	cat.Major = int(major.Int64)
	// Columns that only some versions (or updates) have.
	var seqKey, persisted, autoDrop sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT COL_LENGTH('sys.indexes', 'optimize_for_sequential_key'),
		COL_LENGTH('sys.stats', 'has_persisted_sample'), COL_LENGTH('sys.stats', 'auto_drop')`).Scan(&seqKey, &persisted, &autoDrop); err != nil {
		return cat, err
	}
	optional := func(ok sql.NullInt64, expr string) string {
		if ok.Valid {
			return expr
		}
		return "CAST(0 AS bit)"
	}
	read := func(what, query string, row func(*sql.Rows) error) error {
		rows, err := s.DB.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("read the %s of %s: %w", what, db, err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := row(rows); err != nil {
				return fmt.Errorf("read the %s of %s: %w", what, db, err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read the %s of %s: %w", what, db, err)
		}
		return nil
	}

	graph, ledger := "CAST(0 AS bit)", "CAST(0 AS bit)"
	if cat.Major >= 14 {
		graph = "CAST(t.is_node | t.is_edge AS bit)"
	}
	if cat.Major >= 16 {
		ledger = "CAST(CASE WHEN t.ledger_type <> 0 THEN 1 ELSE 0 END AS bit)"
	}
	if err := read("tables", `SELECT t.object_id, s.name, t.name, t.is_memory_optimized, t.temporal_type,
		CAST(t.is_replicated | t.is_merge_published AS bit), t.is_tracked_by_cdc, t.is_filetable, `+graph+`, `+ledger+`,
		CAST(CASE WHEN EXISTS (SELECT 1 FROM `+q+`.sys.change_tracking_tables ct WHERE ct.object_id = t.object_id) THEN 1 ELSE 0 END AS bit)
		FROM `+q+`.sys.tables t JOIN `+q+`.sys.schemas s ON s.schema_id = t.schema_id WHERE t.is_ms_shipped = 0`,
		func(r *sql.Rows) error {
			var id int64
			var t CatalogTable
			var temporal int
			if err := r.Scan(&id, &t.Schema, &t.Name, &t.MemoryOptimized, &temporal, &t.Replicated, &t.CDC, &t.FileTable, &t.Graph, &t.Ledger, &t.ChangeTracking); err != nil {
				return err
			}
			t.Temporal = temporal
			cat.Tables[id] = t
			return nil
		}); err != nil {
		return cat, err
	}
	if err := read("sizes", `SELECT p.object_id, SUM(CASE WHEN p.index_id IN (0,1) THEN p.rows ELSE 0 END), SUM(a.total_pages)
		FROM `+q+`.sys.partitions p JOIN `+q+`.sys.allocation_units a
		  ON a.container_id = CASE WHEN a.type IN (1,3) THEN p.hobt_id ELSE p.partition_id END
		GROUP BY p.object_id`, func(r *sql.Rows) error {
		var id, rows, pages int64
		if err := r.Scan(&id, &rows, &pages); err != nil {
			return err
		}
		if t, ok := cat.Tables[id]; ok {
			t.Rows, t.ReservedMB = rows, float64(pages)*8/1024
			cat.Tables[id] = t
		}
		return nil
	}); err != nil {
		return cat, err
	}

	encrypted := "CAST(0 AS bit)" // Always Encrypted: SQL Server 2016+
	if cat.Major >= 13 {
		encrypted = "CAST(CASE WHEN c.encryption_type IS NULL THEN 0 ELSE 1 END AS bit)"
	}
	if err := read("columns", `SELECT t.object_id, c.column_id, s.name, t.name, c.name, ut.name, bt.name, c.max_length, c.is_nullable,
		c.collation_name, c.is_computed, CAST(c.is_sparse | c.is_column_set AS bit), ut.is_user_defined, `+encrypted+`
		FROM `+q+`.sys.columns c
		JOIN `+q+`.sys.tables t ON t.object_id = c.object_id AND t.is_ms_shipped = 0
		JOIN `+q+`.sys.schemas s ON s.schema_id = t.schema_id
		JOIN `+q+`.sys.types ut ON ut.user_type_id = c.user_type_id
		JOIN `+q+`.sys.types bt ON bt.user_type_id = c.system_type_id
		WHERE c.collation_name IS NOT NULL
		ORDER BY s.name, t.name, c.column_id`, func(r *sql.Rows) error {
		var c CharColumn
		var maxLen int16
		if err := r.Scan(&c.ObjectID, &c.ColumnID, &c.Schema, &c.Table, &c.Column, &c.Type, &c.BaseType, &maxLen, &c.Nullable,
			&c.Collation, &c.Computed, &c.Sparse, &c.AliasType, &c.Encrypted); err != nil {
			return err
		}
		c.MaxLength = int(maxLen)
		cat.Columns = append(cat.Columns, c)
		return nil
	}); err != nil {
		return cat, err
	}

	indexAt := map[[2]int64]int{}
	if err := read("indexes", `SELECT i.object_id, i.index_id, i.name, i.type, i.is_unique, i.is_primary_key, i.is_unique_constraint,
		i.is_disabled, i.is_hypothetical, i.fill_factor, i.is_padded, i.ignore_dup_key, i.allow_row_locks, i.allow_page_locks,
		i.filter_definition, ds.name, ds.type, ISNULL(st.no_recompute, 0),
		(SELECT MIN(p.data_compression_desc) FROM `+q+`.sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id),
		(SELECT COUNT(DISTINCT p.data_compression) FROM `+q+`.sys.partitions p WHERE p.object_id = i.object_id AND p.index_id = i.index_id),
		ISNULL(kc.object_id, 0), i.has_filter, `+optional(seqKey, "i.optimize_for_sequential_key")+`
		FROM `+q+`.sys.indexes i JOIN `+q+`.sys.tables t ON t.object_id = i.object_id AND t.is_ms_shipped = 0
		LEFT JOIN `+q+`.sys.data_spaces ds ON ds.data_space_id = i.data_space_id
		LEFT JOIN `+q+`.sys.stats st ON st.object_id = i.object_id AND st.stats_id = i.index_id
		LEFT JOIN `+q+`.sys.key_constraints kc ON kc.parent_object_id = i.object_id AND kc.unique_index_id = i.index_id
		WHERE i.index_id > 0
		ORDER BY i.object_id, i.index_id`, func(r *sql.Rows) error {
		var ix CatalogIndex
		var name, filter, fg, dsType, compression sql.NullString
		var typ, fill uint8
		var pk, uq, hasFilter bool
		if err := r.Scan(&ix.ObjectID, &ix.IndexID, &name, &typ, &ix.Def.Unique, &pk, &uq, &ix.Disabled, &ix.Hypothetical, &fill,
			&ix.Def.PadIndex, &ix.Def.IgnoreDupKey, &ix.Def.AllowRowLocks, &ix.Def.AllowPageLocks, &filter, &fg, &dsType,
			&ix.Def.NoRecompute, &compression, &ix.Compressions, &ix.ConstraintID, &hasFilter, &ix.OptimizeSequentialKey); err != nil {
			return err
		}
		ix.FilterUnreadable = hasFilter && !filter.Valid
		ix.Type, ix.DataSpaceType = int(typ), strings.TrimSpace(dsType.String)
		ix.Def.Name, ix.Def.Filter, ix.Def.FileGroup, ix.Def.FillFactor = name.String, filter.String, fg.String, int(fill)
		ix.Def.Clustered = ix.Type == 1
		ix.Def.Compression = compression.String
		switch {
		case pk:
			ix.Def.Kind = IndexPrimaryKey
		case uq:
			ix.Def.Kind = IndexUniqueConstraint
		default:
			ix.Def.Kind = IndexPlain
		}
		indexAt[[2]int64{ix.ObjectID, int64(ix.IndexID)}] = len(cat.Indexes)
		cat.Indexes = append(cat.Indexes, ix)
		return nil
	}); err != nil {
		return cat, err
	}
	if err := read("index columns", `SELECT ic.object_id, ic.index_id, ic.column_id, c.name, ic.key_ordinal, ic.is_descending_key, ic.is_included_column
		FROM `+q+`.sys.index_columns ic JOIN `+q+`.sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		ORDER BY ic.object_id, ic.index_id, ic.key_ordinal, ic.index_column_id`, func(r *sql.Rows) error {
		var obj int64
		var idx, col int
		var name string
		var ord uint8
		var desc, incl bool
		if err := r.Scan(&obj, &idx, &col, &name, &ord, &desc, &incl); err != nil {
			return err
		}
		at, ok := indexAt[[2]int64{obj, int64(idx)}]
		if !ok {
			return nil
		}
		ix := &cat.Indexes[at]
		switch {
		case incl:
			ix.IncludedIDs = append(ix.IncludedIDs, col)
			ix.Def.Included = append(ix.Def.Included, name)
		case ord > 0:
			ix.KeyIDs = append(ix.KeyIDs, col)
			ix.Def.Keys = append(ix.Def.Keys, IndexColumn{Name: name, Descending: desc})
		default:
			ix.IncludedIDs = append(ix.IncludedIDs, col)
		}
		return nil
	}); err != nil {
		return cat, err
	}

	fkAt := map[int64]int{}
	if err := read("foreign keys", `SELECT fk.object_id, fk.name, fk.parent_object_id, ps.name, pt.name, fk.referenced_object_id, rs.name, rt.name,
		fk.key_index_id, fk.delete_referential_action_desc, fk.update_referential_action_desc, fk.is_disabled, fk.is_not_trusted,
		fk.is_not_for_replication
		FROM `+q+`.sys.foreign_keys fk
		JOIN `+q+`.sys.tables pt ON pt.object_id = fk.parent_object_id JOIN `+q+`.sys.schemas ps ON ps.schema_id = pt.schema_id
		JOIN `+q+`.sys.tables rt ON rt.object_id = fk.referenced_object_id JOIN `+q+`.sys.schemas rs ON rs.schema_id = rt.schema_id
		ORDER BY fk.object_id`, func(r *sql.Rows) error {
		var fk CatalogFK
		var keyIndex sql.NullInt64
		if err := r.Scan(&fk.ObjectID, &fk.Def.Name, &fk.ParentID, &fk.Def.Schema, &fk.Def.Table, &fk.RefID, &fk.Def.RefSchema,
			&fk.Def.RefTable, &keyIndex, &fk.Def.OnDelete, &fk.Def.OnUpdate, &fk.Def.Disabled, &fk.Def.NotTrusted,
			&fk.Def.NotForReplication); err != nil {
			return err
		}
		fk.KeyIndexID = int(keyIndex.Int64)
		fkAt[fk.ObjectID] = len(cat.ForeignKeys)
		cat.ForeignKeys = append(cat.ForeignKeys, fk)
		return nil
	}); err != nil {
		return cat, err
	}
	if err := read("foreign key columns", `SELECT k.constraint_object_id, k.parent_column_id, pc.name, k.referenced_column_id, rc.name
		FROM `+q+`.sys.foreign_key_columns k
		JOIN `+q+`.sys.columns pc ON pc.object_id = k.parent_object_id AND pc.column_id = k.parent_column_id
		JOIN `+q+`.sys.columns rc ON rc.object_id = k.referenced_object_id AND rc.column_id = k.referenced_column_id
		ORDER BY k.constraint_object_id, k.constraint_column_id`, func(r *sql.Rows) error {
		var id int64
		var pid, rid int
		var pname, rname string
		if err := r.Scan(&id, &pid, &pname, &rid, &rname); err != nil {
			return err
		}
		at, ok := fkAt[id]
		if !ok {
			return nil
		}
		fk := &cat.ForeignKeys[at]
		fk.ParentIDs, fk.RefIDs = append(fk.ParentIDs, pid), append(fk.RefIDs, rid)
		fk.Def.Columns, fk.Def.RefColumns = append(fk.Def.Columns, pname), append(fk.Def.RefColumns, rname)
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("check constraints", `SELECT cc.object_id, cc.parent_object_id, cc.parent_column_id, cc.name, cc.definition,
		cc.is_disabled, cc.is_not_trusted, cc.is_not_for_replication
		FROM `+q+`.sys.check_constraints cc ORDER BY cc.object_id`, func(r *sql.Rows) error {
		var c CatalogCheck
		var def sql.NullString
		if err := r.Scan(&c.ObjectID, &c.ParentID, &c.ParentColumnID, &c.Def.Name, &def, &c.Def.Disabled, &c.Def.NotTrusted,
			&c.Def.NotForReplication); err != nil {
			return err
		}
		c.Def.Definition, c.NoDefinition = def.String, !def.Valid
		cat.Checks = append(cat.Checks, c)
		return nil
	}); err != nil {
		return cat, err
	}
	// deps[referencing class, id, minor] are the columns of the same table
	// an expression reads (checks: class 1, the constraint, minor 0;
	// computed columns: class 1, the table and the column). The class keeps
	// a DDL trigger (12, 13) with the same id from mixing in.
	deps := map[[3]int64][]int{}
	refTable := map[[3]int64]int64{}
	if err := read("dependencies", `SELECT d.referencing_class, d.referencing_id, d.referencing_minor_id, d.referenced_id, d.referenced_minor_id
		FROM `+q+`.sys.sql_expression_dependencies d WHERE d.referenced_minor_id > 0 AND d.referenced_id IS NOT NULL`,
		func(r *sql.Rows) error {
			var class uint8
			var from, to int64
			var fromMinor, toMinor int
			if err := r.Scan(&class, &from, &fromMinor, &to, &toMinor); err != nil {
				return err
			}
			k := [3]int64{int64(class), from, int64(fromMinor)}
			deps[k] = append(deps[k], toMinor)
			refTable[k] = to
			return nil
		}); err != nil {
		return cat, err
	}
	for i := range cat.Checks {
		k := [3]int64{1, cat.Checks[i].ObjectID, 0}
		if refTable[k] == cat.Checks[i].ParentID {
			cat.Checks[i].ColumnIDs = deps[k]
		}
	}

	if err := read("defaults", `SELECT dc.object_id, dc.parent_object_id, dc.parent_column_id, dc.name, c.name, dc.definition
		FROM `+q+`.sys.default_constraints dc
		JOIN `+q+`.sys.columns c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id
		ORDER BY dc.object_id`, func(r *sql.Rows) error {
		var d CatalogDefault
		var def sql.NullString
		if err := r.Scan(&d.ObjectID, &d.ParentID, &d.ColumnID, &d.Def.Name, &d.Def.Column, &def); err != nil {
			return err
		}
		d.Def.Definition, d.NoDefinition = def.String, !def.Valid
		cat.Defaults = append(cat.Defaults, d)
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("statistics", `SELECT s.object_id, s.stats_id, s.name, s.auto_created, s.user_created, s.no_recompute,
		s.filter_definition, s.is_incremental, sc.column_id, c.name, s.has_filter,
		`+optional(persisted, "s.has_persisted_sample")+`, `+optional(autoDrop, "s.auto_drop")+`
		FROM `+q+`.sys.stats s JOIN `+q+`.sys.stats_columns sc ON sc.object_id = s.object_id AND sc.stats_id = s.stats_id
		JOIN `+q+`.sys.columns c ON c.object_id = sc.object_id AND c.column_id = sc.column_id
		JOIN `+q+`.sys.tables t ON t.object_id = s.object_id AND t.is_ms_shipped = 0
		WHERE NOT EXISTS (SELECT 1 FROM `+q+`.sys.indexes i WHERE i.object_id = s.object_id AND i.index_id = s.stats_id)
		ORDER BY s.object_id, s.stats_id, sc.stats_column_id`, func(r *sql.Rows) error {
		var st CatalogStats
		var user, hasFilter bool
		var filter sql.NullString
		var col int
		var name string
		if err := r.Scan(&st.ObjectID, &st.StatsID, &st.Def.Name, &st.AutoCreated, &user, &st.Def.NoRecompute, &filter,
			&st.Incremental, &col, &name, &hasFilter, &st.PersistedSample, &st.AutoDrop); err != nil {
			return err
		}
		if n := len(cat.Stats); n > 0 && cat.Stats[n-1].ObjectID == st.ObjectID && cat.Stats[n-1].StatsID == st.StatsID {
			last := &cat.Stats[n-1]
			last.ColumnIDs = append(last.ColumnIDs, col)
			last.Def.Columns = append(last.Def.Columns, name)
			return nil
		}
		st.Def.Filter, st.Def.Recreate = filter.String, !st.AutoCreated
		st.FilterUnreadable = hasFilter && !filter.Valid
		st.ColumnIDs, st.Def.Columns = []int{col}, []string{name}
		cat.Stats = append(cat.Stats, st)
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("computed columns", `SELECT cc.object_id, cc.column_id, cc.name, ISNULL(cc.definition, N'')
		FROM `+q+`.sys.computed_columns cc ORDER BY cc.object_id, cc.column_id`, func(r *sql.Rows) error {
		var c CatalogComputed
		if err := r.Scan(&c.ObjectID, &c.ColumnID, &c.Name, &c.Definition); err != nil {
			return err
		}
		k := [3]int64{1, c.ObjectID, int64(c.ColumnID)}
		if refTable[k] == c.ObjectID {
			c.ColumnIDs = deps[k]
		}
		cat.Computed = append(cat.Computed, c)
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("full-text indexes", `SELECT fi.object_id, fi.unique_index_id, fic.column_id FROM `+q+`.sys.fulltext_indexes fi
		LEFT JOIN `+q+`.sys.fulltext_index_columns fic ON fic.object_id = fi.object_id
		ORDER BY fi.object_id`, func(r *sql.Rows) error {
		var obj int64
		var key int
		var col sql.NullInt64
		if err := r.Scan(&obj, &key, &col); err != nil {
			return err
		}
		n := len(cat.FullText)
		if n == 0 || cat.FullText[n-1].ObjectID != obj {
			cat.FullText = append(cat.FullText, CatalogFullText{ObjectID: obj, KeyIndexID: key})
			n++
		}
		if col.Valid {
			cat.FullText[n-1].ColumnIDs = append(cat.FullText[n-1].ColumnIDs, int(col.Int64))
		}
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("schema-bound references", `SELECT DISTINCT d.referenced_id, d.referenced_minor_id, s.name, o.name, o.type_desc
		FROM `+q+`.sys.sql_expression_dependencies d
		JOIN `+q+`.sys.sql_modules m ON m.object_id = d.referencing_id AND m.is_schema_bound = 1
		JOIN `+q+`.sys.objects o ON o.object_id = d.referencing_id JOIN `+q+`.sys.schemas s ON s.schema_id = o.schema_id
		WHERE d.referenced_id IS NOT NULL`, func(r *sql.Rows) error {
		var ref CatalogReference
		if err := r.Scan(&ref.ReferencedID, &ref.ColumnID, &ref.Schema, &ref.Name, &ref.Type); err != nil {
			return err
		}
		cat.SchemaBound = append(cat.SchemaBound, ref)
		return nil
	}); err != nil {
		return cat, err
	}

	if err := read("extended properties", `SELECT ep.class, ep.major_id, ep.minor_id, ep.name, CONVERT(nvarchar(max), ep.value),
		CONVERT(nvarchar(128), SQL_VARIANT_PROPERTY(ep.value, 'BaseType'))
		FROM `+q+`.sys.extended_properties ep
		WHERE (ep.class = 1 AND ep.minor_id = 0 AND EXISTS (SELECT 1 FROM `+q+`.sys.objects o
		        WHERE o.object_id = ep.major_id AND o.type IN ('PK', 'UQ', 'F', 'C', 'D')))
		   OR ep.class = 7
		ORDER BY ep.class, ep.major_id, ep.minor_id, ep.name`, func(r *sql.Rows) error {
		var e CatalogExtendedProperty
		var class uint8
		var value, base sql.NullString
		if err := r.Scan(&class, &e.MajorID, &e.MinorID, &e.Name, &value, &base); err != nil {
			return err
		}
		e.Class, e.Value, e.BaseType = int(class), value.String, base.String
		cat.ExtendedProperties = append(cat.ExtendedProperties, e)
		return nil
	}); err != nil {
		return cat, err
	}

	if cat.Major >= 13 {
		if err := read("masked columns", `SELECT mc.object_id, mc.column_id FROM `+q+`.sys.masked_columns mc WHERE mc.is_masked = 1`,
			func(r *sql.Rows) error {
				var k ColumnKey
				if err := r.Scan(&k.ObjectID, &k.ColumnID); err != nil {
					return err
				}
				cat.Masked = append(cat.Masked, k)
				return nil
			}); err != nil {
			return cat, err
		}
	}
	return cat, nil
}

// Exists runs a "SELECT TOP (1) 1 ..." data check: true when it returns a
// row. The caller bounds it with a timeout.
func (s Session) Exists(ctx context.Context, query string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, query).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// sqlLockTimeout is the error SQL Server raises when LOCK_TIMEOUT expires.
const sqlLockTimeout = 1222

// IsLockTimeout reports whether err is a lock request that timed out
// (Msg 1222): a conversion that waited ConvertLockTimeoutMS for its tables.
func IsLockTimeout(err error) bool {
	var e mssqldb.Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Number == sqlLockTimeout {
		return true
	}
	for _, m := range e.All {
		if m.Number == sqlLockTimeout {
			return true
		}
	}
	return false
}
