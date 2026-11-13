package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// convertInstance is fakeInstance with a database sales whose tables
// customers (name) and hist (temporal, name) are not yet Latin1_General_100_CS_AS.
type convertInstance struct {
	*fakeInstance
	cat mssql.ConversionCatalog
}

const convertFrom, convertTo = "SQL_Latin1_General_CP1_CI_AS", "Latin1_General_100_CS_AS"

func newConvertInstance() *convertInstance {
	col := func(id int64, table string) mssql.CharColumn {
		return mssql.CharColumn{ObjectID: id, ColumnID: 1, Schema: "dbo", Table: table, Column: "name", Type: "nvarchar",
			BaseType: "nvarchar", MaxLength: 100, Nullable: true, Collation: convertFrom}
	}
	return &convertInstance{
		fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE"}},
		cat: mssql.ConversionCatalog{Major: 16,
			Tables: map[int64]mssql.CatalogTable{
				1: {Schema: "dbo", Name: "customers", ReservedMB: 1},
				2: {Schema: "dbo", Name: "hist", Temporal: 1},
			},
			Columns: []mssql.CharColumn{col(1, "customers"), col(2, "hist")}},
	}
}

func (f *convertInstance) CollationState(context.Context, string) (mssql.CollationState, error) {
	return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: convertFrom, State: "ONLINE"},
		UserAccess: "MULTI_USER", CanAlter: true, Exists: true}, nil
}

func (f *convertInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	if name != convertFrom && name != convertTo {
		return mssql.CollationInfo{}, false, nil
	}
	return mssql.CollationInfo{Name: name, CodePage: 1252}, true, nil
}

func (f *convertInstance) ConversionCatalog(context.Context, string) (mssql.ConversionCatalog, error) {
	return f.cat, nil
}

func (f *convertInstance) Exec(ctx context.Context, stmt string) error {
	if err := f.fakeInstance.Exec(ctx, stmt); err != nil {
		return err
	}
	for i := range f.cat.Columns {
		if strings.Contains(stmt, "["+f.cat.Columns[i].Table+"] ALTER COLUMN") {
			f.cat.Columns[i].Collation = convertTo
		}
	}
	return nil
}

func withConvert(a *app, f *convertInstance) {
	withOps(a, f.fakeInstance, fakeObjects{})
	a.openOps = func(_ context.Context, name string, t config.Target, _ string) (*ops.Session, func(), error) {
		return &ops.Session{Name: name, Target: t, SQL: f}, func() {}, nil
	}
}

func TestCollationConvertDryRun(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := newConvertInstance()
	withConvert(a, f)
	code, out, _ := run(a, "collation", "convert", "sales", "--to", convertTo)
	if code != 0 || len(f.ran) != 0 {
		t.Fatalf("code %d ran %v\n%s", code, f.ran, out)
	}
	for _, want := range []string{"Refused tables", "dbo.hist (system-versioned", "ALTER TABLE [dbo].[customers] ALTER COLUMN [name] nvarchar(50) COLLATE " + convertTo + " NULL;",
		"Rerun with --apply"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run(a, "collation", "convert", "sales", "--to", convertTo, "-o", "json")
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || !doc.Ready || doc.Confirmation != "sales" || len(doc.Plan.Actions) != 1 {
		t.Fatalf("json: code %d %v %+v", code, err, doc)
	}
}

func TestCollationConvertRefusedTableFails(t *testing.T) {
	read := journalOf(t)
	a, _, _ := testApp(t, testConfig)
	f := newConvertInstance()
	withConvert(a, f)
	if code, out, _ := run(a, "collation", "convert", "sales", "--to", convertTo, "--table", "dbo.hist"); code != exitCheckFailed || !strings.Contains(out, "cannot convert dbo.hist") {
		t.Fatalf("dry run: code %d\n%s", code, out)
	}
	if code, _, _ := run(a, "collation", "convert", "sales", "--to", convertTo, "--table", "hist", "--apply"); code != exitCheckFailed || len(f.ran) != 0 {
		t.Fatalf("apply: code %d ran %v", code, f.ran)
	}
	list := read()
	if len(list) != 1 || list[0].Operation != ops.OpConvertColumns || list[0].Refusal == nil || list[0].Refusal.Status != 412 || list[0].Items[0].Name != "sales" {
		t.Fatalf("journal %+v", list)
	}
	if code, _, errOut := run(a, "collation", "convert", "sales", "--table", "[dbo"); code != exitFailure || !strings.Contains(errOut, "unclosed") {
		t.Fatalf("bad table: code %d %q", code, errOut)
	}
	if code, _, errOut := run(a, "collation", "convert", "sales", "--to", "Latin1 General"); code != exitFailure || !strings.Contains(errOut, "invalid collation") {
		t.Fatalf("bad collation: code %d %q", code, errOut)
	}
}

func TestCollationConvertApply(t *testing.T) {
	read := journalOf(t)
	a, _, _ := testApp(t, testConfig)
	f := newConvertInstance()
	withConvert(a, f)
	// destructive: without a terminal the database name must be given
	if code, _, _ := run(a, "collation", "convert", "sales", "--to", convertTo, "--apply"); code == 0 || len(f.ran) != 0 {
		t.Fatalf("no confirmation: code %d ran %v", code, f.ran)
	}
	code, out, errOut := run(a, "collation", "convert", "sales", "--to", convertTo, "--apply", "--confirm", "sales")
	if code != 0 || len(f.ran) != 1 || !strings.Contains(f.ran[0], "COLLATE "+convertTo) {
		t.Fatalf("code %d ran %v\n%s%s", code, f.ran, out, errOut)
	}
	if !strings.Contains(out, "1 column(s) of 1 table(s) converted to "+convertTo) {
		t.Fatalf("output:\n%s", out)
	}
	var job jobs.Job
	for _, j := range read() {
		if j.Refusal == nil {
			job = j
		}
	}
	if job.State != jobs.Succeeded || job.Operation != ops.OpConvertColumns || job.Items[0].Steps[0].ID != "convert" {
		t.Fatalf("job %+v", job)
	}
	// a second apply has nothing left to convert
	if code, out, _ := run(a, "collation", "convert", "sales", "--to", convertTo); code != 0 || !strings.Contains(out, "nothing to convert") {
		t.Fatalf("again: code %d\n%s", code, out)
	}
}

func TestParseTableRef(t *testing.T) {
	for in, want := range map[string]mssql.TableRef{
		"dbo.orders":          {Schema: "dbo", Name: "orders"},
		"orders":              {Schema: "dbo", Name: "orders"},
		"[sales].[o.d]":       {Schema: "sales", Name: "o.d"},
		"[a.b].c":             {Schema: "a.b", Name: "c"},
		"[x]]y].[it's here]":  {Schema: "x]y", Name: "it's here"},
		"sales.[order lines]": {Schema: "sales", Name: "order lines"},
	} {
		if got, err := parseTableRef(in); err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"a.b.c", "[a", "a.", ".a", "[a]b", ""} {
		if got, err := parseTableRef(bad); err == nil {
			t.Errorf("%q: %+v", bad, got)
		}
	}
}
