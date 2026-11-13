package ops

import (
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// validUnit is a unit with one of everything a conversion rebuilds.
func validUnit() mssql.ConversionUnit {
	return mssql.ConversionUnit{Database: "sales", Collation: "Latin1_General_100_CS_AS",
		Tables: []mssql.TableConversion{{
			Schema: "dbo", Name: "Order Details",
			Columns: []mssql.ColumnChange{
				{Name: "code", Type: "nvarchar(50)", From: "SQL_Latin1_General_CP1_CI_AS", To: "Latin1_General_100_CS_AS"},
				{Name: "note", Type: "varchar(max)", Nullable: true, From: "SQL_Latin1_General_CP1_CI_AS", To: "Latin1_General_100_CS_AS"},
				{Name: "kind", Type: "char(3)", From: "SQL_Latin1_General_CP1_CI_AS", To: "Latin1_General_100_CS_AS"},
				{Name: "owner", Type: "sysname", From: "SQL_Latin1_General_CP1_CI_AS", To: "Latin1_General_100_CS_AS"},
			},
			Indexes: []mssql.IndexDef{{Name: "PK_od", Kind: mssql.IndexPrimaryKey, Clustered: true, Unique: true,
				Keys: []mssql.IndexColumn{{Name: "code"}}, Included: []string{"note"}, Compression: "PAGE", FileGroup: "PRIMARY"}},
			Checks:   []mssql.CheckDef{{Name: "CK_kind", Definition: "([kind]<>'x')"}},
			Defaults: []mssql.DefaultDef{{Name: "DF_kind", Column: "kind", Definition: "('abc')"}},
			Stats:    []mssql.StatsDef{{Name: "st_code", Columns: []string{"code"}, Recreate: true}},
			ExtendedProperties: []mssql.ExtendedPropertyDef{
				{Target: "PK_od", Level2: mssql.Level2Constraint, Name: "MS_Description", Value: "it's the key"}},
		}},
		ForeignKeys: []mssql.ForeignKeyDef{{Name: "FK_od_order", Schema: "dbo", Table: "Order Details", Columns: []string{"code"},
			RefSchema: "dbo", RefTable: "orders", RefColumns: []string{"code"}, OnDelete: "CASCADE", OnUpdate: "NO_ACTION"}},
	}
}

func TestCollationStepValidate(t *testing.T) {
	var target config.Target
	set := CollationStep{ID: CollSetDatabase, Database: "sales", Collation: "Latin1_General_100_CI_AS", From: "SQL_Latin1_General_CP1_CI_AS"}
	if err := set.Validate(target); err != nil {
		t.Fatalf("set-collation: %v", err)
	}
	u := validUnit()
	convert := CollationStep{ID: CollConvert, Database: "sales", Collation: "Latin1_General_100_CS_AS", Unit: &u}
	if err := convert.Validate(target); err != nil {
		t.Fatalf("convert: %v", err)
	}
	bad := map[string]func(st *CollationStep, u *mssql.ConversionUnit){
		"unknown id":           func(st *CollationStep, _ *mssql.ConversionUnit) { st.ID = "drop" },
		"system database":      func(st *CollationStep, u *mssql.ConversionUnit) { st.Database, u.Database = "master", "master" },
		"collation":            func(st *CollationStep, u *mssql.ConversionUnit) { st.Collation, u.Collation = "Latin1;GO", "Latin1;GO" },
		"from":                 func(st *CollationStep, _ *mssql.ConversionUnit) { st.From = "x y" },
		"no unit":              func(st *CollationStep, _ *mssql.ConversionUnit) { st.Unit = nil },
		"unit of another db":   func(_ *CollationStep, u *mssql.ConversionUnit) { u.Database = "other" },
		"unit to another coll": func(_ *CollationStep, u *mssql.ConversionUnit) { u.Collation = "Latin1_General_BIN2" },
		"no table":             func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables = nil },
		"table name":           func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Name = "" },
		"schema control":       func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Schema = "dbo\n" },
		"column name": func(_ *CollationStep, u *mssql.ConversionUnit) {
			u.Tables[0].Columns[0].Name = strings.Repeat("c", 129)
		},
		"column type":       func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Columns[0].Type = "nvarchar(50) NULL; --" },
		"char max":          func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Columns[0].Type = "char(max)" },
		"text":              func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Columns[0].Type = "text" },
		"column from":       func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Columns[0].From = "" },
		"column to":         func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Columns[0].To = "Latin1_General_BIN2" },
		"index kind":        func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Indexes[0].Kind = "columnstore" },
		"index compression": func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Indexes[0].Compression = "COLUMNSTORE" },
		"index filegroup":   func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Indexes[0].FileGroup = "" },
		"index key":         func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Indexes[0].Keys[0].Name = "" },
		"index included":    func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Indexes[0].Included[0] = "a\x00" },
		"check name":        func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Checks[0].Name = "" },
		"default column":    func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Defaults[0].Column = "" },
		"stats column":      func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].Stats[0].Columns[0] = "" },
		"ext prop level2":   func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].ExtendedProperties[0].Level2 = "COLUMN" },
		"ext prop name":     func(_ *CollationStep, u *mssql.ConversionUnit) { u.Tables[0].ExtendedProperties[0].Name = "" },
		"fk action":         func(_ *CollationStep, u *mssql.ConversionUnit) { u.ForeignKeys[0].OnDelete = "SET NULL" },
		"fk ref column":     func(_ *CollationStep, u *mssql.ConversionUnit) { u.ForeignKeys[0].RefColumns[0] = "" },
	}
	for name, change := range bad {
		t.Run(name, func(t *testing.T) {
			u := validUnit()
			st := convert
			st.Unit = &u
			change(&st, &u)
			if err := st.Validate(target); err == nil {
				t.Fatal("valid")
			}
		})
	}
	withUnit := set
	withUnit.Unit = &u
	if err := withUnit.Validate(target); err == nil {
		t.Fatal("a set-collation step with a unit is valid")
	}
}

func TestValidateKeepsTheCollationFieldsToTheirOperations(t *testing.T) {
	var target config.Target
	for _, op := range []string{OpRestore, OpExport, OpFinish, OpDrop, OpCancel, OpGrant, OpRevoke, OpFixOrphan} {
		for name, p := range map[string]Params{
			"collation":      {Collation: "Latin1_General_100_CI_AS"},
			"tables":         {Tables: []mssql.TableRef{{Schema: "dbo", Name: "t"}}},
			"skipDataChecks": {SkipDataChecks: true},
		} {
			p.Database, p.Key, p.TaskID = "sales", "sales.bak", 0
			if op == OpCancel {
				p.Database, p.Key, p.TaskID = "", "", 3
			}
			p.Login, p.Level, p.Remove, p.User = "dora", "read", "level", "dora"
			if !IsAccess(op) {
				p.Login, p.Level, p.Remove, p.User = "", "", "", ""
			}
			err := Validate(op, p, target)
			if err == nil || !strings.Contains(err.Error(), "are for set-collation and convert-columns") {
				t.Errorf("%s with %s: %v", op, name, err)
			}
		}
	}
	for _, op := range []string{OpSetCollation, OpConvertColumns} {
		for name, p := range map[string]Params{
			"key":     {Database: "sales", Key: "sales.bak"},
			"task":    {Database: "sales", TaskID: 3},
			"restore": {Database: "sales", Restore: mssql.RestoreOptions{NoRecovery: true}},
			"replace": {Database: "sales", Replace: ReplaceOptions{Mode: ReplaceSwap}},
			"login":   {Database: "sales", Login: "dora"},
			"backup":  {Database: "sales", Backup: true},
		} {
			if err := Validate(op, p, target); err == nil || strings.Contains(err.Error(), "not implemented") {
				t.Errorf("%s with %s: %v", op, name, err)
			}
		}
	}
}
