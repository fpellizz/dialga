package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

const cliServerColl = "SQL_Latin1_General_CP1_CI_AS"

// collationInstance is fakeInstance with the collation reads.
type collationInstance struct{ *fakeInstance }

func (collationInstance) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{Server: cliServerColl, Tempdb: cliServerColl}, nil
}

func (collationInstance) DatabaseCollations(context.Context) ([]mssql.DatabaseCollation, error) {
	return []mssql.DatabaseCollation{
		{Name: "contained", Collation: "Latin1_General_CS_AS", State: "ONLINE", Containment: "PARTIAL"},
		{Name: "sales", Collation: "Latin1_General_CS_AS", State: "ONLINE", Containment: "NONE"},
	}, nil
}

func (collationInstance) CollationState(_ context.Context, db string) (mssql.CollationState, error) {
	switch db {
	case "sales":
		return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: db, Collation: "Latin1_General_CS_AS",
			State: "ONLINE", Containment: "NONE"}, Exists: true}, nil
	case "off":
		return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: db, State: "OFFLINE"}, Exists: true}, nil
	}
	return mssql.CollationState{}, nil
}

func (collationInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{Name: name, CodePage: 1252}, true, nil
}

func (collationInstance) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return []mssql.CharColumn{
		{Schema: "dbo", Table: "t", Column: "a", Type: "nvarchar", BaseType: "nvarchar", MaxLength: 100, Collation: "Latin1_General_CS_AS"},
		{Schema: "dbo", Table: "t", Column: "legacy", Type: "varchar", BaseType: "varchar", MaxLength: 10, Collation: cliServerColl},
		{Schema: "dbo", Table: "t", Column: "body", Type: "text", BaseType: "text", MaxLength: 16, Collation: cliServerColl},
	}, nil
}

func (collationInstance) Collations(context.Context) ([]mssql.CollationInfo, error) {
	return []mssql.CollationInfo{
		{Name: "Latin1_General_CS_AS", Description: "Latin1-General, case-sensitive", CodePage: 1252, CaseSensitive: true, AccentSensitive: true},
		{Name: "Latin1_General_100_CI_AS_SC_UTF8", Description: "Latin1-General-100, UTF-8", CodePage: 65001, AccentSensitive: true, UTF8: true},
		{Name: "Cyrillic_General_CI_AS", Description: "Cyrillic", CodePage: 1251},
	}, nil
}

// withCollation makes the app open sessions over the collation fake.
func withCollation(a *app) {
	f := collationInstance{&fakeInstance{}}
	a.openOps = func(_ context.Context, name string, t config.Target, _ string) (*ops.Session, func(), error) {
		return &ops.Session{Name: name, Target: t, SQL: f}, func() {}, nil
	}
}

func TestCollationShow(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCollation(a)
	code, out, _ := run(a, "collation", "show")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{"Collations on dev-sql", "server  " + cliServerColl + " (fixed at instance creation on RDS)",
		"sales", "≠ server, temp-table risk", "≠ server, contained"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run(a, "collation", "show", "dev-sql", "-o", "json")
	var o ops.CollationOverview
	if err := json.Unmarshal([]byte(out), &o); err != nil || code != 0 || len(o.Databases) != 2 || !o.ServerFixed {
		t.Fatalf("json: code %d %v %+v", code, err, o)
	}
	if code, _, _ := run(a, "collation", "show", "nope"); code != 1 {
		t.Fatalf("unknown target: code %d", code)
	}
}

func TestCollationDB(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCollation(a)
	code, out, _ := run(a, "collation", "db", "sales")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{"Collation of sales on dev-sql", "Temp-table risk", "3 character columns", "database default",
		"2 columns differ from the database default (--columns lists them)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "legacy") {
		t.Fatalf("columns listed without --columns:\n%s", out)
	}
	code, out, _ = run(a, "collation", "db", "sales", "--columns")
	for _, want := range []string{"dbo.t", "legacy", "varchar(10)", "text/ntext: change it to varchar(max)/nvarchar(max) first"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("--columns output lacks %q (code %d):\n%s", want, code, out)
		}
	}
	code, out, _ = run(a, "collation", "db", "sales", "-o", "json")
	var v ops.DatabaseCollationView
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || v.Database != "sales" || len(v.Columns) != 2 || v.CharColumns != 3 {
		t.Fatalf("json: code %d %v %+v", code, err, v)
	}
	if code, _, errOut := run(a, "collation", "db", "nope"); code != exitCheckFailed || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing db: code %d %q", code, errOut)
	}
	if code, _, errOut := run(a, "collation", "db", "off"); code != exitCheckFailed || !strings.Contains(errOut, "not online") {
		t.Fatalf("offline db: code %d %q", code, errOut)
	}
	if code, _, _ := run(a, "collation", "db", "tempdb"); code != 1 {
		t.Fatalf("system db: code %d", code)
	}
}

func TestCollationList(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCollation(a)
	code, out, _ := run(a, "collation", "list")
	if code != 0 || !strings.Contains(out, "Cyrillic_General_CI_AS") || !strings.Contains(out, "3 collations.") {
		t.Fatalf("list: code %d\n%s", code, out)
	}
	code, out, _ = run(a, "collation", "list", "--filter", "latin1")
	if code != 0 || strings.Contains(out, "Cyrillic") || !strings.Contains(out, "2 collations.") || !strings.Contains(out, "65001") {
		t.Fatalf("filtered list: code %d\n%s", code, out)
	}
	code, out, _ = run(a, "collation", "list", "--filter", "zzz")
	if code != 0 || !strings.Contains(out, "No collation matches.") {
		t.Fatalf("no match: code %d\n%s", code, out)
	}
	code, out, _ = run(a, "collation", "list", "--filter", "utf8", "-o", "json")
	var doc struct {
		Collations []mssql.CollationInfo `json:"collations"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || len(doc.Collations) != 1 || !doc.Collations[0].UTF8 {
		t.Fatalf("json: code %d %v %+v", code, err, doc)
	}
}
