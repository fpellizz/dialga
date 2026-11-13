// Work package C: see the collation design.
//
// The types of this file are frozen (step 0): they are what a job journals
// for a convert step. ConvertUnit and the data-check queries are C's.

package mssql

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

// ConvertUnit is the transaction that converts u (collation design §5.2).
// Names and types are validated by the caller.
func ConvertUnit(u ConversionUnit) string {
	return ""
}
