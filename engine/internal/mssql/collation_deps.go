// Work package C: see the collation design.

package mssql

import "context"

// ConversionCatalog is what convert-columns reads of a database to plan a
// conversion: one read per category, for the whole database, filtered in Go
// (collation design §4.3). The fields of the Catalog* types are a starting
// point: work package C owns them.
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
	Rows            int64
	ReservedMB      float64
}

// CatalogIndex is an index of a user table.
type CatalogIndex struct {
	ObjectID      int64
	IndexID       int
	Type          int
	Disabled      bool
	Hypothetical  bool
	DataSpaceType string
	Compressions  int
	Def           IndexDef
	KeyIDs        []int
	IncludedIDs   []int
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
	ParentID       int64
	ParentColumnID int
	ColumnIDs      []int
	Def            CheckDef
	// NoDefinition: the definition is not readable (no VIEW DEFINITION).
	NoDefinition bool
}

// CatalogDefault is a DEFAULT constraint.
type CatalogDefault struct {
	ParentID int64
	ColumnID int
	Def      DefaultDef
}

// CatalogStats is a statistic that is not the one of an index.
type CatalogStats struct {
	ObjectID    int64
	StatsID     int
	AutoCreated bool
	Incremental bool
	ColumnIDs   []int
	Def         StatsDef
}

// CatalogComputed is a computed column and the columns it reads.
type CatalogComputed struct {
	ObjectID  int64
	ColumnID  int
	Name      string
	ColumnIDs []int
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

// ConversionCatalog reads what convert-columns needs of db (collation design
// §4.3).
func (s Session) ConversionCatalog(ctx context.Context, db string) (ConversionCatalog, error) {
	return ConversionCatalog{}, errNotYet
}

// Exists runs a "SELECT TOP (1) 1 ..." data check: true when it returns a
// row. The caller bounds it with a timeout.
func (s Session) Exists(ctx context.Context, query string) (bool, error) {
	return false, errNotYet
}
