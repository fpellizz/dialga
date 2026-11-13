//go:build integration

package mssql

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCollationReadOnSQLServer(t *testing.T) {
	db := catalogServer(t)
	execAll(t, db,
		`CREATE DATABASE coll_a COLLATE Latin1_General_CS_AS`,
		`CREATE DATABASE coll_cont COLLATE Latin1_General_100_CI_AS`,
	)
	// connections are reset between statements (no USE): run in the database
	// through its own sp_executesql
	inDB := func(stmt string) string {
		return "EXEC coll_a.sys.sp_executesql N'" + strings.ReplaceAll(stmt, "'", "''") + "'"
	}
	execAll(t, db,
		inDB(`CREATE TYPE dbo.code_t FROM varchar(8) NOT NULL`),
		inDB(`CREATE TABLE dbo.t1 (id int PRIMARY KEY,
			a nvarchar(50) NOT NULL,
			b varchar(max) NULL,
			c varchar(20) COLLATE Cyrillic_General_CI_AS NULL,
			d char(3) COLLATE SQL_Latin1_General_CP1_CI_AS NOT NULL,
			legacy text COLLATE SQL_Latin1_General_CP1_CI_AS NULL,
			code dbo.code_t,
			calc AS (UPPER(c)),
			n int)`),
		inDB(`CREATE SCHEMA ops`),
		inDB(`CREATE TABLE ops.[Odd Table] ([weird]] col] nvarchar(10) NULL)`))
	ctx := context.Background()
	s := Session{DB: db}

	dbs, err := s.DatabaseCollations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range dbs {
		names = append(names, d.Name)
		switch d.Name {
		case "coll_a":
			if d.Collation != "Latin1_General_CS_AS" || d.State != "ONLINE" || d.Containment != "NONE" {
				t.Errorf("coll_a = %+v", d)
			}
		case "master", "tempdb", "model", "msdb", "rdsadmin":
			t.Errorf("system database %s listed", d.Name)
		}
	}
	if !slices.Contains(names, "coll_a") || !slices.IsSorted(names) {
		t.Fatalf("databases %v", names)
	}

	cols, err := s.CharColumns(ctx, "coll_a")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CharColumn{}
	for _, c := range cols {
		by[c.Table+"."+c.Column] = c
	}
	if len(cols) != 8 || by["t1.n"].Column != "" || by["t1.id"].Column != "" {
		t.Fatalf("columns (%d) %+v", len(cols), cols)
	}
	if c := by["t1.a"]; c.Collation != "Latin1_General_CS_AS" || c.Type != "nvarchar" || c.MaxLength != 100 || c.Nullable ||
		c.Schema != "dbo" || c.SQLType() != "nvarchar(50)" || c.Unsupported() != "" || c.ObjectID == 0 || c.ColumnID != 2 {
		t.Errorf("t1.a = %+v", c)
	}
	if c := by["t1.b"]; c.MaxLength != -1 || !c.Nullable || c.SQLType() != "varchar(max)" {
		t.Errorf("t1.b = %+v", c)
	}
	if c := by["t1.c"]; c.Collation != "Cyrillic_General_CI_AS" || c.Unsupported() != "" {
		t.Errorf("t1.c = %+v", c)
	}
	if c := by["t1.d"]; c.Collation != "SQL_Latin1_General_CP1_CI_AS" || c.SQLType() != "char(3)" {
		t.Errorf("t1.d = %+v", c)
	}
	if c := by["t1.legacy"]; c.BaseType != "text" || c.Unsupported() == "" {
		t.Errorf("t1.legacy = %+v", c)
	}
	if c := by["t1.code"]; !c.AliasType || c.Type != "code_t" || c.BaseType != "varchar" || c.Unsupported() == "" {
		t.Errorf("t1.code = %+v", c)
	}
	if c := by["t1.calc"]; !c.Computed || c.Unsupported() == "" {
		t.Errorf("t1.calc = %+v", c)
	}
	if c := by["Odd Table.weird] col"]; c.Schema != "ops" || c.Collation != "Latin1_General_CS_AS" {
		t.Errorf("odd column = %+v", c)
	}
	// ordered by schema, table, position
	if cols[0].Schema != "dbo" || cols[0].Column != "a" || cols[len(cols)-1].Schema != "ops" {
		t.Errorf("order: first %+v, last %+v", cols[0], cols[len(cols)-1])
	}
	if cols, err := s.CharColumns(ctx, "coll_cont"); err != nil || len(cols) != 0 {
		t.Errorf("empty database: %v %v", cols, err)
	}
	if _, err := s.CharColumns(ctx, "no_such_db"); err == nil {
		t.Error("a missing database gave no error")
	}

	all, err := s.Collations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 5000 { // ordered by the collation of master, not by bytes
		t.Fatalf("%d collations", len(all))
	}
	i := slices.IndexFunc(all, func(c CollationInfo) bool { return c.Name == "Latin1_General_100_CI_AS_SC_UTF8" })
	if i < 0 {
		t.Fatal("UTF-8 collation missing")
	}
	want := TraitsFromStyle("Latin1_General_100_CI_AS_SC_UTF8", 196609)
	want.Description, want.CodePage, want.Version = all[i].Description, 65001, 2
	if all[i] != want || !all[i].UTF8 || all[i].CaseSensitive || !all[i].AccentSensitive || all[i].Description == "" {
		t.Fatalf("UTF-8 collation = %+v, want %+v", all[i], want)
	}
	// the same row as the single lookup
	one, found, err := s.Collation(ctx, "Latin1_General_CS_AS")
	j := slices.IndexFunc(all, func(c CollationInfo) bool { return c.Name == "Latin1_General_CS_AS" })
	if err != nil || !found || j < 0 || all[j] != one || !one.CaseSensitive {
		t.Fatalf("Collations row %+v vs Collation %+v (%v, found %v)", all[j], one, err, found)
	}
}
