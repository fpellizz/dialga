package mssql

import "testing"

func TestTraitsFromStyle(t *testing.T) {
	tests := []struct {
		name  string
		style int
		want  CollationInfo
	}{
		{"SQL_Latin1_General_CP1_CI_AS", 196609,
			CollationInfo{CaseSensitive: false, AccentSensitive: true, Legacy: true}},
		{"Latin1_General_100_CS_AS_KS_WS", 0,
			CollationInfo{CaseSensitive: true, AccentSensitive: true, KanaSensitive: true, WidthSensitive: true}},
		{"Latin1_General_100_CI_AI", 196611,
			CollationInfo{}},
		{"Latin1_General_100_CI_AS_SC_UTF8", 196609,
			CollationInfo{AccentSensitive: true, UTF8: true}},
		{"Latin1_General_BIN", 0,
			CollationInfo{CaseSensitive: true, AccentSensitive: true, KanaSensitive: true, WidthSensitive: true, Binary: true}},
		{"Latin1_General_100_BIN2_UTF8", 0,
			CollationInfo{CaseSensitive: true, AccentSensitive: true, KanaSensitive: true, WidthSensitive: true, Binary: true, UTF8: true}},
		{"SQL_Latin1_General_CP850_BIN2", 0,
			CollationInfo{CaseSensitive: true, AccentSensitive: true, KanaSensitive: true, WidthSensitive: true, Binary: true, Legacy: true}},
		{"Japanese_XJIS_140_CI_AS_KS", 131073,
			CollationInfo{AccentSensitive: true, KanaSensitive: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			want.Name = tc.name
			if got := TraitsFromStyle(tc.name, tc.style); got != want {
				t.Fatalf("TraitsFromStyle(%q, %d)\ngot  %+v\nwant %+v", tc.name, tc.style, got, want)
			}
		})
	}
}

func TestCharColumnUnsupported(t *testing.T) {
	tests := []struct {
		name string
		col  CharColumn
		want string
	}{
		{"nvarchar", CharColumn{Type: "nvarchar", BaseType: "nvarchar", MaxLength: 100}, ""},
		{"sysname", CharColumn{Type: "sysname", BaseType: "nvarchar", MaxLength: 256}, ""},
		{"computed", CharColumn{Type: "nvarchar", BaseType: "nvarchar", Computed: true}, "computed column: its collation follows its expression"},
		{"computed first", CharColumn{Type: "text", BaseType: "text", Computed: true, Sparse: true}, "computed column: its collation follows its expression"},
		{"text", CharColumn{Type: "text", BaseType: "text", MaxLength: 16}, "text/ntext: change it to varchar(max)/nvarchar(max) first"},
		{"ntext", CharColumn{Type: "ntext", BaseType: "ntext", MaxLength: 16}, "text/ntext: change it to varchar(max)/nvarchar(max) first"},
		{"alias", CharColumn{Type: "PhoneNumber", BaseType: "varchar", MaxLength: 20, AliasType: true}, "user-defined type PhoneNumber: not supported"},
		{"sparse", CharColumn{Type: "varchar", BaseType: "varchar", MaxLength: 10, Sparse: true}, "sparse column or column set: not supported"},
		{"encrypted", CharColumn{Type: "varchar", BaseType: "varchar", MaxLength: 10, Encrypted: true}, "Always Encrypted column: not supported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.col.Unsupported(); got != tc.want {
				t.Fatalf("Unsupported() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCharColumnSQLType(t *testing.T) {
	tests := []struct {
		col  CharColumn
		want string
	}{
		{CharColumn{Type: "nvarchar", BaseType: "nvarchar", MaxLength: 100}, "nvarchar(50)"},
		{CharColumn{Type: "nvarchar", BaseType: "nvarchar", MaxLength: -1}, "nvarchar(max)"},
		{CharColumn{Type: "nchar", BaseType: "nchar", MaxLength: 20}, "nchar(10)"},
		{CharColumn{Type: "varchar", BaseType: "varchar", MaxLength: 8000}, "varchar(8000)"},
		{CharColumn{Type: "varchar", BaseType: "varchar", MaxLength: -1}, "varchar(max)"},
		{CharColumn{Type: "char", BaseType: "char", MaxLength: 3}, "char(3)"},
		{CharColumn{Type: "sysname", BaseType: "nvarchar", MaxLength: 256}, "sysname"},
		{CharColumn{Type: "PhoneNumber", BaseType: "varchar", MaxLength: 20, AliasType: true}, "varchar(20)"},
		{CharColumn{Type: "text", BaseType: "text", MaxLength: 16}, "text"},
	}
	for _, tc := range tests {
		if got := tc.col.SQLType(); got != tc.want {
			t.Errorf("%+v: SQLType() = %q, want %q", tc.col, got, tc.want)
		}
	}
}
