// Work package C: see the collation design.
//
// The types of this file are frozen (step 0): they are what a job journals
// for a convert step. ConvertUnit and the data-check queries are C's.

package mssql

import (
	"slices"
	"strconv"
	"strings"
)

// ColumnChange is a column a conversion alters.
type ColumnChange struct {
	Name string `json:"name"`
	// Type is the type as CharColumn.SQLType writes it: nvarchar(50),
	// varchar(max), sysname.
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// IndexColumn is a key column of an index.
type IndexColumn struct {
	Name       string `json:"name"`
	Descending bool   `json:"descending,omitempty"`
}

// IndexDef is an index, a primary key or a unique constraint, as a
// conversion drops and creates it again.
type IndexDef struct {
	Name string `json:"name"`
	// Kind is primary-key, unique-constraint or index.
	Kind      string        `json:"kind"`
	Clustered bool          `json:"clustered"`
	Unique    bool          `json:"unique"`
	Keys      []IndexColumn `json:"keys"`
	Included  []string      `json:"included,omitempty"`
	// Filter is filter_definition, as read.
	Filter string `json:"filter,omitempty"`
	// FillFactor 0 is the default.
	FillFactor     int  `json:"fillFactor,omitempty"`
	PadIndex       bool `json:"padIndex,omitempty"`
	IgnoreDupKey   bool `json:"ignoreDupKey,omitempty"`
	NoRecompute    bool `json:"noRecompute,omitempty"`
	AllowRowLocks  bool `json:"allowRowLocks"`
	AllowPageLocks bool `json:"allowPageLocks"`
	// Compression is NONE, ROW or PAGE.
	Compression string `json:"compression"`
	FileGroup   string `json:"fileGroup"`
}

// The kinds of IndexDef.
const (
	IndexPrimaryKey       = "primary-key"
	IndexUniqueConstraint = "unique-constraint"
	IndexPlain            = "index"
)

// ForeignKeyDef is a foreign key a conversion drops and creates again.
type ForeignKeyDef struct {
	Name       string   `json:"name"`
	Schema     string   `json:"schema"`
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	// OnDelete and OnUpdate are NO_ACTION, CASCADE, SET_NULL or SET_DEFAULT.
	OnDelete          string `json:"onDelete"`
	OnUpdate          string `json:"onUpdate"`
	NotForReplication bool   `json:"notForReplication,omitempty"`
	Disabled          bool   `json:"disabled,omitempty"`
	NotTrusted        bool   `json:"notTrusted,omitempty"`
}

// ReferentialActions are the values of ForeignKeyDef.OnDelete and OnUpdate.
var ReferentialActions = []string{"NO_ACTION", "CASCADE", "SET_NULL", "SET_DEFAULT"}

// CheckDef is a CHECK constraint a conversion drops and creates again.
type CheckDef struct {
	Name              string `json:"name"`
	Definition        string `json:"definition"`
	NotForReplication bool   `json:"notForReplication,omitempty"`
	Disabled          bool   `json:"disabled,omitempty"`
	NotTrusted        bool   `json:"notTrusted,omitempty"`
}

// DefaultDef is a DEFAULT constraint a conversion drops and creates again.
type DefaultDef struct {
	Name       string `json:"name"`
	Column     string `json:"column"`
	Definition string `json:"definition"`
}

// StatsDef is a statistic on a converted column. Recreate is false for an
// auto-created one: it is only dropped (SQL Server creates it again).
type StatsDef struct {
	Name        string   `json:"name"`
	Columns     []string `json:"columns"`
	Filter      string   `json:"filter,omitempty"`
	NoRecompute bool     `json:"noRecompute,omitempty"`
	Recreate    bool     `json:"recreate,omitempty"`
}

// ExtendedPropertyDef is an extended property of an index or a constraint a
// conversion drops: it is added again once the object is created again.
type ExtendedPropertyDef struct {
	// Target is the name of the index or constraint it belongs to.
	Target string `json:"target"`
	// Level2 is CONSTRAINT or INDEX.
	Level2 string `json:"level2"`
	Name   string `json:"name"`
	// Value is the text of the property (only text values are kept).
	Value string `json:"value"`
}

// The values of ExtendedPropertyDef.Level2.
const (
	Level2Constraint = "CONSTRAINT"
	Level2Index      = "INDEX"
)

// TableConversion is one table of a conversion unit: the columns it alters
// and what depends on them.
type TableConversion struct {
	Schema             string                `json:"schema"`
	Name               string                `json:"name"`
	Columns            []ColumnChange        `json:"columns"`
	Indexes            []IndexDef            `json:"indexes,omitempty"`
	Checks             []CheckDef            `json:"checks,omitempty"`
	Defaults           []DefaultDef          `json:"defaults,omitempty"`
	Stats              []StatsDef            `json:"stats,omitempty"`
	ExtendedProperties []ExtendedPropertyDef `json:"extendedProperties,omitempty"`
}

// ConversionUnit is a set of tables linked by foreign keys, converted in one
// transaction.
type ConversionUnit struct {
	Database    string            `json:"database"`
	Collation   string            `json:"collation"`
	Tables      []TableConversion `json:"tables"`
	ForeignKeys []ForeignKeyDef   `json:"foreignKeys,omitempty"`
}

// ConvertLockTimeoutMS is the LOCK_TIMEOUT of a conversion: it does not wait
// longer for the tables it alters.
const ConvertLockTimeoutMS = 30000

// ConvertUnit is the transaction that converts u (collation design §5.2):
// the foreign keys are dropped, then per table what depends on the columns
// (CHECK, DEFAULT, statistics, non-clustered and clustered indexes); the
// columns are altered; everything is created again in the reverse order,
// each object followed by its extended properties. XACT_ABORT makes any
// error roll the whole unit back; LOCK_TIMEOUT keeps it from waiting for
// the tables more than ConvertLockTimeoutMS. Names and types are validated
// by the caller (CollationStep.Validate); the collation is never quoted.
func ConvertUnit(u ConversionUnit) string {
	var b strings.Builder
	line := func(parts ...string) {
		for _, p := range parts {
			b.WriteString(p)
		}
		b.WriteString("\n")
	}
	line("USE ", quoteIdent(u.Database), ";")
	line("SET XACT_ABORT ON;")
	line("SET LOCK_TIMEOUT ", strconv.Itoa(ConvertLockTimeoutMS), ";")
	line("BEGIN TRANSACTION;")
	for _, fk := range u.ForeignKeys {
		line("ALTER TABLE ", tableName(fk.Schema, fk.Table), " DROP CONSTRAINT ", quoteIdent(fk.Name), ";")
	}
	for _, t := range u.Tables {
		tn := tableName(t.Schema, t.Name)
		for _, c := range t.Checks {
			line("ALTER TABLE ", tn, " DROP CONSTRAINT ", quoteIdent(c.Name), ";")
		}
		for _, d := range t.Defaults {
			line("ALTER TABLE ", tn, " DROP CONSTRAINT ", quoteIdent(d.Name), ";")
		}
		for _, s := range t.Stats {
			line("DROP STATISTICS ", tn, ".", quoteIdent(s.Name), ";")
		}
		for _, clustered := range []bool{false, true} {
			for _, ix := range t.Indexes {
				if ix.Clustered != clustered {
					continue
				}
				if ix.Kind == IndexPlain {
					line("DROP INDEX ", quoteIdent(ix.Name), " ON ", tn, ";")
				} else {
					line("ALTER TABLE ", tn, " DROP CONSTRAINT ", quoteIdent(ix.Name), ";")
				}
			}
		}
	}
	for _, t := range u.Tables {
		for _, c := range t.Columns {
			null := " NOT NULL"
			if c.Nullable {
				null = " NULL"
			}
			line("ALTER TABLE ", tableName(t.Schema, t.Name), " ALTER COLUMN ", quoteIdent(c.Name), " ", c.Type, " COLLATE ", u.Collation, null, ";")
		}
	}
	for _, t := range u.Tables {
		tn := tableName(t.Schema, t.Name)
		props := func(level2, target string) {
			for _, e := range t.ExtendedProperties {
				if e.Level2 == level2 && e.Target == target {
					line(addExtendedProperty(u.Database, t.Schema, t.Name, e))
				}
			}
		}
		for _, clustered := range []bool{true, false} {
			for _, ix := range t.Indexes {
				if ix.Clustered != clustered {
					continue
				}
				line(createIndex(tn, ix))
				if ix.Kind != IndexPlain {
					props(Level2Constraint, ix.Name)
				}
				props(Level2Index, ix.Name)
			}
		}
		for _, s := range t.Stats {
			if !s.Recreate {
				continue
			}
			stmt := "CREATE STATISTICS " + quoteIdent(s.Name) + " ON " + tn + " (" + identList(s.Columns) + ")"
			if s.Filter != "" {
				stmt += " WHERE " + s.Filter
			}
			if s.NoRecompute {
				stmt += " WITH NORECOMPUTE"
			}
			line(stmt, ";")
		}
		for _, d := range t.Defaults {
			line("ALTER TABLE ", tn, " ADD CONSTRAINT ", quoteIdent(d.Name), " DEFAULT ", d.Definition, " FOR ", quoteIdent(d.Column), ";")
			props(Level2Constraint, d.Name)
		}
		for _, c := range t.Checks {
			nfr := ""
			if c.NotForReplication {
				nfr = " NOT FOR REPLICATION"
			}
			line("ALTER TABLE ", tn, withCheck(c.NotTrusted), " ADD CONSTRAINT ", quoteIdent(c.Name), " CHECK", nfr, " ", c.Definition, ";")
			if c.Disabled {
				line("ALTER TABLE ", tn, " NOCHECK CONSTRAINT ", quoteIdent(c.Name), ";")
			}
			props(Level2Constraint, c.Name)
		}
	}
	for _, fk := range u.ForeignKeys {
		tn := tableName(fk.Schema, fk.Table)
		nfr := ""
		if fk.NotForReplication {
			nfr = " NOT FOR REPLICATION"
		}
		line("ALTER TABLE ", tn, withCheck(fk.NotTrusted), " ADD CONSTRAINT ", quoteIdent(fk.Name),
			" FOREIGN KEY (", identList(fk.Columns), ") REFERENCES ", tableName(fk.RefSchema, fk.RefTable),
			" (", identList(fk.RefColumns), ") ON DELETE ", referentialAction(fk.OnDelete),
			" ON UPDATE ", referentialAction(fk.OnUpdate), nfr, ";")
		if fk.Disabled {
			line("ALTER TABLE ", tn, " NOCHECK CONSTRAINT ", quoteIdent(fk.Name), ";")
		}
		// The extended properties of a foreign key are in its table's list.
		for _, t := range u.Tables {
			if t.Schema != fk.Schema || t.Name != fk.Table {
				continue
			}
			for _, e := range t.ExtendedProperties {
				if e.Level2 == Level2Constraint && e.Target == fk.Name {
					line(addExtendedProperty(u.Database, t.Schema, t.Name, e))
				}
			}
		}
	}
	b.WriteString("COMMIT TRANSACTION;")
	return b.String()
}

func tableName(schema, table string) string { return quoteIdent(schema) + "." + quoteIdent(table) }

func identList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = quoteIdent(n)
	}
	return strings.Join(q, ", ")
}

func withCheck(notTrusted bool) string {
	if notTrusted {
		return " WITH NOCHECK"
	}
	return " WITH CHECK"
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// referentialAction is the T-SQL of a ForeignKeyDef action: NO_ACTION is
// NO ACTION.
func referentialAction(a string) string { return strings.ReplaceAll(a, "_", " ") }

// createIndex creates an index, a primary key or a unique constraint again
// with the options it had.
func createIndex(table string, ix IndexDef) string {
	kind := "NONCLUSTERED"
	if ix.Clustered {
		kind = "CLUSTERED"
	}
	keys := make([]string, len(ix.Keys))
	for i, k := range ix.Keys {
		dir := " ASC"
		if k.Descending {
			dir = " DESC"
		}
		keys[i] = quoteIdent(k.Name) + dir
	}
	var b strings.Builder
	switch ix.Kind {
	case IndexPrimaryKey, IndexUniqueConstraint:
		what := "PRIMARY KEY"
		if ix.Kind == IndexUniqueConstraint {
			what = "UNIQUE"
		}
		b.WriteString("ALTER TABLE " + table + " ADD CONSTRAINT " + quoteIdent(ix.Name) + " " + what + " " + kind +
			" (" + strings.Join(keys, ", ") + ")")
	default:
		unique := ""
		if ix.Unique {
			unique = "UNIQUE "
		}
		b.WriteString("CREATE " + unique + kind + " INDEX " + quoteIdent(ix.Name) + " ON " + table + " (" + strings.Join(keys, ", ") + ")")
		if len(ix.Included) > 0 {
			b.WriteString(" INCLUDE (" + identList(ix.Included) + ")")
		}
		if ix.Filter != "" {
			b.WriteString(" WHERE " + ix.Filter)
		}
	}
	b.WriteString(" WITH (PAD_INDEX = " + onOff(ix.PadIndex) + ", ")
	if ix.FillFactor > 0 {
		b.WriteString("FILLFACTOR = " + strconv.Itoa(ix.FillFactor) + ", ")
	}
	b.WriteString("IGNORE_DUP_KEY = " + onOff(ix.IgnoreDupKey) +
		", STATISTICS_NORECOMPUTE = " + onOff(ix.NoRecompute) +
		", ALLOW_ROW_LOCKS = " + onOff(ix.AllowRowLocks) +
		", ALLOW_PAGE_LOCKS = " + onOff(ix.AllowPageLocks) +
		", DATA_COMPRESSION = " + ix.Compression + ") ON " + quoteIdent(ix.FileGroup) + ";")
	return b.String()
}

// addExtendedProperty adds an extended property of an index or a constraint
// of schema.table again.
func addExtendedProperty(db, schema, table string, e ExtendedPropertyDef) string {
	return "EXEC " + quoteIdent(db) + ".sys.sp_addextendedproperty @name = " + Str(e.Name) +
		", @value = " + Str(e.Value) +
		", @level0type = N'SCHEMA', @level0name = " + Str(schema) +
		", @level1type = N'TABLE', @level1name = " + Str(table) +
		", @level2type = " + Str(e.Level2) + ", @level2name = " + Str(e.Target) + ";"
}

// The data checks of a conversion run under READUNCOMMITTED: they never
// block the application (and can miss rows that change meanwhile: the
// conversion then fails and rolls back). Each one is a "SELECT TOP (1) 1":
// a row is a problem (Session.Exists).

// DuplicateKeyQuery finds the values of the keys of a unique index (or
// primary key) of table that would collide once the keys in convert are
// compared with collation: values that differ only by case or accent under
// the old one. filter is the filter of the index ("" for none).
func DuplicateKeyQuery(db string, table TableRef, keys []string, convert []string, collation, filter string) string {
	cols := make([]string, len(keys))
	for i, k := range keys {
		cols[i] = quoteIdent(k)
		if slices.Contains(convert, k) {
			cols[i] += " COLLATE " + collation
		}
	}
	q := "SELECT TOP (1) 1 FROM " + quoteIdent(db) + "." + tableName(table.Schema, table.Name) + " WITH (READUNCOMMITTED)"
	if filter != "" {
		q += " WHERE " + filter
	}
	return q + "\nGROUP BY " + strings.Join(cols, ", ") + " HAVING COUNT_BIG(*) > 1;"
}

// OrphanQuery finds the rows of the table of fk whose key would no longer
// find its parent once the columns in convert (of either side, by the name
// in the child) are compared with collation. Only enabled, trusted foreign
// keys are checked.
func OrphanQuery(db string, fk ForeignKeyDef, convert []string, collation string) string {
	var notNull, match []string
	for i, c := range fk.Columns {
		notNull = append(notNull, "c."+quoteIdent(c)+" IS NOT NULL")
		p, k := "p."+quoteIdent(fk.RefColumns[i]), "c."+quoteIdent(c)
		if slices.Contains(convert, c) {
			p += " COLLATE " + collation
			k += " COLLATE " + collation
		}
		match = append(match, p+" = "+k)
	}
	return "SELECT TOP (1) 1 FROM " + quoteIdent(db) + "." + tableName(fk.Schema, fk.Table) + " AS c WITH (READUNCOMMITTED)\n" +
		"WHERE " + strings.Join(notNull, " AND ") + " AND NOT EXISTS (SELECT 1 FROM " + quoteIdent(db) + "." +
		tableName(fk.RefSchema, fk.RefTable) + " AS p WITH (READUNCOMMITTED)\n" +
		"  WHERE " + strings.Join(match, " AND ") + ");"
}

// CodePageLossQuery finds the values of a char or varchar column that the
// code page of col.To cannot hold (they would become "?") or that would no
// longer fit in maxBytes bytes (UTF-8 takes more bytes); maxBytes <= 0 (max)
// skips the length.
func CodePageLossQuery(db string, table TableRef, col ColumnChange, maxBytes int) string {
	c := quoteIdent(col.Name)
	converted := "CAST(CAST(" + c + " AS nvarchar(max)) COLLATE " + col.To + " AS varchar(max))"
	q := "SELECT TOP (1) 1 FROM " + quoteIdent(db) + "." + tableName(table.Schema, table.Name) + " WITH (READUNCOMMITTED) WHERE " + c + " IS NOT NULL AND (\n" +
		"  CAST(" + converted + " AS nvarchar(max)) COLLATE Latin1_General_BIN2\n" +
		"    <> CAST(" + c + " AS nvarchar(max)) COLLATE Latin1_General_BIN2"
	if maxBytes > 0 {
		q += "\n  OR DATALENGTH(" + converted + ") > " + strconv.Itoa(maxBytes)
	}
	return q + ");"
}
