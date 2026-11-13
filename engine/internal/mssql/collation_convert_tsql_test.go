package mssql

import (
	"strings"
	"testing"
)

// The units of the golden files collation-convert-*.sql (database golden_db,
// to Latin1_General_100_CS_AS).
const (
	goldenFrom = "SQL_Latin1_General_CP1_CI_AS"
	goldenTo   = "Latin1_General_100_CS_AS"
)

func goldenColumn(name, typ string, nullable bool) ColumnChange {
	return ColumnChange{Name: name, Type: typ, Nullable: nullable, From: goldenFrom, To: goldenTo}
}

func goldenUnit(tables []TableConversion, fks ...ForeignKeyDef) ConversionUnit {
	return ConversionUnit{Database: "golden_db", Collation: goldenTo, Tables: tables, ForeignKeys: fks}
}

func defaultIndex(def IndexDef) IndexDef {
	def.AllowRowLocks, def.AllowPageLocks = true, true
	if def.Compression == "" {
		def.Compression = "NONE"
	}
	if def.FileGroup == "" {
		def.FileGroup = "PRIMARY"
	}
	return def
}

func goldenConvertSimple() ConversionUnit {
	return goldenUnit([]TableConversion{{Schema: "dbo", Name: "customers", Columns: []ColumnChange{goldenColumn("name", "nvarchar(100)", true)}}})
}

// goldenConvertPKFK: the parent has a clustered primary key on the code, the
// child references it ON UPDATE CASCADE.
func goldenConvertPKFK() ConversionUnit {
	return goldenUnit([]TableConversion{
		{Schema: "dbo", Name: "order_lines", Columns: []ColumnChange{goldenColumn("order_code", "varchar(20)", false)},
			Indexes: []IndexDef{defaultIndex(IndexDef{Name: "IX_order_lines_order", Kind: IndexPlain, Keys: []IndexColumn{{Name: "order_code"}}})}},
		{Schema: "dbo", Name: "orders", Columns: []ColumnChange{goldenColumn("code", "varchar(20)", false)},
			Indexes: []IndexDef{defaultIndex(IndexDef{Name: "PK_orders", Kind: IndexPrimaryKey, Clustered: true, Unique: true, Keys: []IndexColumn{{Name: "code"}}})}},
	}, ForeignKeyDef{Name: "FK_order_lines_orders", Schema: "dbo", Table: "order_lines", Columns: []string{"order_code"},
		RefSchema: "dbo", RefTable: "orders", RefColumns: []string{"code"}, OnDelete: "NO_ACTION", OnUpdate: "CASCADE"})
}

func goldenConvertIndexOptions() ConversionUnit {
	return goldenUnit([]TableConversion{{Schema: "sales", Name: "products", Columns: []ColumnChange{
		goldenColumn("sku", "char(12)", false), goldenColumn("label", "nvarchar(max)", true)},
		Indexes: []IndexDef{{Name: "UX_products_sku", Kind: IndexPlain, Unique: true,
			Keys: []IndexColumn{{Name: "sku"}, {Name: "created", Descending: true}}, Included: []string{"label", "price"},
			Filter: "([sku] IS NOT NULL)", FillFactor: 80, PadIndex: true, IgnoreDupKey: true, NoRecompute: true,
			AllowRowLocks: true, AllowPageLocks: false, Compression: "PAGE", FileGroup: "INDEXES"}}}})
}

func goldenConvertCheckDefaultStats() ConversionUnit {
	return goldenUnit([]TableConversion{{Schema: "dbo", Name: "accounts", Columns: []ColumnChange{
		goldenColumn("status", "varchar(10)", false), goldenColumn("region", "nchar(2)", true)},
		Checks:   []CheckDef{{Name: "CK_accounts_status", Definition: "([status]='closed' OR [status]='open')"}},
		Defaults: []DefaultDef{{Name: "DF_accounts_status", Column: "status", Definition: "('open')"}},
		Stats: []StatsDef{
			{Name: "_WA_Sys_00000002_0EA330E9", Columns: []string{"status"}},
			{Name: "st_accounts_region", Columns: []string{"region", "status"}, Filter: "([region] IS NOT NULL)", NoRecompute: true, Recreate: true},
		}}})
}

func goldenConvertNocheck() ConversionUnit {
	return goldenUnit([]TableConversion{
		{Schema: "dbo", Name: "child", Columns: []ColumnChange{goldenColumn("parent_code", "varchar(10)", true)},
			Checks: []CheckDef{{Name: "CK_child_code", Definition: "([parent_code]<>'')", NotForReplication: true, NotTrusted: true, Disabled: true}}},
		{Schema: "dbo", Name: "parent", Columns: []ColumnChange{goldenColumn("code", "varchar(10)", false)},
			Indexes: []IndexDef{defaultIndex(IndexDef{Name: "UQ_parent_code", Kind: IndexUniqueConstraint, Unique: true, Keys: []IndexColumn{{Name: "code"}}})}},
	}, ForeignKeyDef{Name: "FK_child_parent", Schema: "dbo", Table: "child", Columns: []string{"parent_code"},
		RefSchema: "dbo", RefTable: "parent", RefColumns: []string{"code"}, OnDelete: "SET_NULL", OnUpdate: "NO_ACTION",
		NotForReplication: true, Disabled: true, NotTrusted: true})
}

func goldenConvertQuote() ConversionUnit {
	u := goldenUnit([]TableConversion{{Schema: "o'neil", Name: "order]s detail", Columns: []ColumnChange{goldenColumn("it's code", "nvarchar(50)", false)},
		Indexes:  []IndexDef{defaultIndex(IndexDef{Name: "PK o]neil", Kind: IndexPrimaryKey, Clustered: true, Unique: true, Keys: []IndexColumn{{Name: "it's code"}}})},
		Defaults: []DefaultDef{{Name: "DF it's", Column: "it's code", Definition: "(N'it''s')"}}}})
	u.Database = "o-neil_db"
	return u
}

func goldenConvertExtendedProperties() ConversionUnit {
	return goldenUnit([]TableConversion{
		{Schema: "dbo", Name: "lines", Columns: []ColumnChange{goldenColumn("code", "varchar(10)", false)},
			Indexes: []IndexDef{defaultIndex(IndexDef{Name: "IX_lines_code", Kind: IndexPlain, Keys: []IndexColumn{{Name: "code"}}})},
			Checks:  []CheckDef{{Name: "CK_lines_code", Definition: "([code]<>'')"}},
			ExtendedProperties: []ExtendedPropertyDef{
				{Target: "CK_lines_code", Level2: Level2Constraint, Name: "MS_Description", Value: "never empty"},
				{Target: "FK_lines_heads", Level2: Level2Constraint, Name: "MS_Description", Value: "the head's line"},
				{Target: "IX_lines_code", Level2: Level2Index, Name: "MS_Description", Value: "lookup by code"},
			}},
		{Schema: "dbo", Name: "heads", Columns: []ColumnChange{goldenColumn("code", "varchar(10)", false)},
			Indexes: []IndexDef{defaultIndex(IndexDef{Name: "PK_heads", Kind: IndexPrimaryKey, Clustered: true, Unique: true, Keys: []IndexColumn{{Name: "code"}}})},
			ExtendedProperties: []ExtendedPropertyDef{
				{Target: "PK_heads", Level2: Level2Constraint, Name: "MS_Description", Value: "key"},
				{Target: "PK_heads", Level2: Level2Index, Name: "owner", Value: "sales"},
			}},
	}, ForeignKeyDef{Name: "FK_lines_heads", Schema: "dbo", Table: "lines", Columns: []string{"code"},
		RefSchema: "dbo", RefTable: "heads", RefColumns: []string{"code"}, OnDelete: "CASCADE", OnUpdate: "SET_DEFAULT"})
}

func goldenDuplicates() string {
	return DuplicateKeyQuery("golden_db", TableRef{Schema: "dbo", Name: "orders"}, []string{"code", "year"}, []string{"code"}, goldenTo, "([code] IS NOT NULL)")
}

func goldenOrphans() string {
	return OrphanQuery("golden_db", ForeignKeyDef{Name: "FK_lines_orders", Schema: "dbo", Table: "lines", Columns: []string{"order_code", "year"},
		RefSchema: "dbo", RefTable: "orders", RefColumns: []string{"code", "year"}, OnDelete: "NO_ACTION", OnUpdate: "NO_ACTION"},
		[]string{"order_code"}, goldenTo)
}

func goldenCodePage() string {
	return CodePageLossQuery("golden_db", TableRef{Schema: "dbo", Name: "customers"},
		ColumnChange{Name: "city", Type: "varchar(40)", Nullable: true, From: goldenFrom, To: "Cyrillic_General_CI_AS"}, 40)
}

// A unit is one transaction: XACT_ABORT, LOCK_TIMEOUT, BEGIN and COMMIT
// around everything, foreign keys dropped first and created last.
func TestConvertUnitShape(t *testing.T) {
	for name, u := range map[string]ConversionUnit{
		"simple": goldenConvertSimple(), "pk-fk": goldenConvertPKFK(), "index": goldenConvertIndexOptions(),
		"check": goldenConvertCheckDefaultStats(), "nocheck": goldenConvertNocheck(), "quote": goldenConvertQuote(),
		"props": goldenConvertExtendedProperties(),
	} {
		got := ConvertUnit(u)
		lines := strings.Split(got, "\n")
		if lines[0] != "USE "+quoteIdent(u.Database)+";" || lines[1] != "SET XACT_ABORT ON;" || lines[2] != "SET LOCK_TIMEOUT 30000;" ||
			lines[3] != "BEGIN TRANSACTION;" || lines[len(lines)-1] != "COMMIT TRANSACTION;" {
			t.Errorf("%s: envelope\n%s", name, got)
		}
		if ConvertUnit(u) != got {
			t.Errorf("%s: not deterministic", name)
		}
	}
	// a long max length of a code-page check is skipped
	q := CodePageLossQuery("db", TableRef{Schema: "dbo", Name: "t"}, ColumnChange{Name: "c", Type: "varchar(max)", To: "Latin1_General_100_CI_AS_SC_UTF8"}, -1)
	if strings.Contains(q, "DATALENGTH") {
		t.Fatalf("max column checks the length:\n%s", q)
	}
}
