package mssql

import (
	"strconv"
	"strings"
)

// The read types of the collation views and operations (collation design,
// step 0): frozen, the work packages build on them.

// ServerCollation is the collation of the instance: the server default,
// which tempdb (and so every #temp table) always has, and that of model,
// which new databases start from.
type ServerCollation struct {
	// Server is SERVERPROPERTY('Collation'): chosen when the RDS instance is
	// created, it cannot be changed afterwards.
	Server         string `json:"server"`
	Tempdb         string `json:"tempdb"`
	Model          string `json:"model"`
	ProductVersion string `json:"productVersion"`
	// Major is SERVERPROPERTY('ProductMajorVersion') (14 = 2017, 16 = 2022).
	Major int `json:"-"`
}

// CollationInfo is a collation the instance knows (sys.fn_helpcollations).
type CollationInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// CodePage is the code page of char and varchar; 0 = Unicode only.
	CodePage int `json:"codePage"`
	// Version is COLLATIONPROPERTY 'Version' (0 to 3).
	Version         int  `json:"version"`
	CaseSensitive   bool `json:"caseSensitive"`
	AccentSensitive bool `json:"accentSensitive"`
	KanaSensitive   bool `json:"kanaSensitive"`
	WidthSensitive  bool `json:"widthSensitive"`
	Binary          bool `json:"binary"`
	UTF8            bool `json:"utf8"`
	// Legacy is a SQL Server collation (SQL_...), kept for compatibility.
	Legacy bool `json:"legacy"`
}

// The bits of COLLATIONPROPERTY(name, 'ComparisonStyle'): each one set means
// the comparison ignores that difference.
const (
	styleIgnoreCase   = 1
	styleIgnoreAccent = 2
	styleIgnoreKana   = 65536
	styleIgnoreWidth  = 131072
)

// TraitsFromStyle is the CollationInfo of name with the traits its
// ComparisonStyle and its name tell: the sensitivities from the style, binary
// (a _BIN or _BIN2 part), UTF-8 (a _UTF8 part) and legacy (SQL_ prefix) from
// the name. The other fields are left to the caller.
func TraitsFromStyle(name string, style int) CollationInfo {
	info := CollationInfo{
		Name:            name,
		CaseSensitive:   style&styleIgnoreCase == 0,
		AccentSensitive: style&styleIgnoreAccent == 0,
		KanaSensitive:   style&styleIgnoreKana == 0,
		WidthSensitive:  style&styleIgnoreWidth == 0,
		Legacy:          strings.HasPrefix(strings.ToUpper(name), "SQL_"),
	}
	for part := range strings.SplitSeq(strings.ToUpper(name), "_") {
		switch part {
		case "BIN", "BIN2":
			info.Binary = true
		case "UTF8":
			info.UTF8 = true
		}
	}
	return info
}

// DatabaseCollation is the default collation of a database.
type DatabaseCollation struct {
	Name string `json:"name"`
	// Collation is empty when the database is not online (SQL Server does
	// not tell it then).
	Collation string `json:"collation,omitempty"`
	State     string `json:"state"`
	// Containment is NONE or PARTIAL: the #temp tables of a contained
	// database take its collation, not tempdb's.
	Containment string `json:"containment"`
}

// CollationState is one database as set-collation and convert-columns read
// it before they plan and before they act.
type CollationState struct {
	DatabaseCollation
	// UserAccess is MULTI_USER, SINGLE_USER or RESTRICTED_USER.
	UserAccess string
	ReadOnly   bool
	// CanAlter: HAS_PERMS_BY_NAME(db, 'DATABASE', 'ALTER') = 1 for the
	// session's login.
	CanAlter bool
	// AvailabilityReplica: the database is in an availability group
	// (Multi-AZ with Always On): sys.databases.replica_id or
	// group_database_id is set.
	AvailabilityReplica bool
	// Mirrored: the database is mirrored (Multi-AZ with database mirroring).
	Mirrored bool
	// Exists is false when the instance has no database with that name.
	Exists bool
}

// TableRef names a user table.
type TableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// CharColumn is a column of a user table that has a collation.
type CharColumn struct {
	ObjectID int64  `json:"-"`
	ColumnID int    `json:"-"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	// Type is the type of the column: char, varchar, nchar, nvarchar, text,
	// ntext, sysname, or the name of an alias type.
	Type string `json:"type"`
	// BaseType is the system type under it.
	BaseType string `json:"baseType"`
	// MaxLength is sys.columns.max_length: bytes, -1 = max.
	MaxLength int    `json:"maxLength"`
	Nullable  bool   `json:"nullable"`
	Collation string `json:"collation"`
	Computed  bool   `json:"computed,omitempty"`
	// Sparse: is_sparse or is_column_set.
	Sparse bool `json:"sparse,omitempty"`
	// AliasType: a user-defined alias type (sys.types.is_user_defined).
	AliasType bool `json:"aliasType,omitempty"`
	// Encrypted: Always Encrypted (encryption_type NOT NULL).
	Encrypted bool `json:"encrypted,omitempty"`
}

// Unsupported says why convert-columns never touches the column; "" means
// it can be converted.
func (c CharColumn) Unsupported() string {
	switch base := strings.ToLower(c.BaseType); {
	case c.Computed:
		return "computed column: its collation follows its expression"
	case base == "text" || base == "ntext":
		return "text/ntext: change it to varchar(max)/nvarchar(max) first"
	case c.AliasType:
		return "user-defined type " + c.Type + ": not supported"
	case c.Sparse:
		return "sparse column or column set: not supported"
	case c.Encrypted:
		return "Always Encrypted column: not supported"
	}
	return ""
}

// SQLType is the type as ALTER COLUMN writes it: nvarchar(50), varchar(max),
// sysname. The length of nchar and nvarchar is in characters (max_length is
// in bytes).
func (c CharColumn) SQLType() string {
	if strings.EqualFold(c.Type, "sysname") && !c.AliasType {
		return "sysname"
	}
	base := strings.ToLower(c.BaseType)
	switch base {
	case "char", "varchar", "nchar", "nvarchar":
	default:
		return base // text, ntext: no length
	}
	switch {
	case c.MaxLength == -1:
		return base + "(max)"
	case base == "nchar" || base == "nvarchar":
		return base + "(" + strconv.Itoa(c.MaxLength/2) + ")"
	default:
		return base + "(" + strconv.Itoa(c.MaxLength) + ")"
	}
}
