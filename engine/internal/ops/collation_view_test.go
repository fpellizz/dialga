package ops

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

const srvColl = "SQL_Latin1_General_CP1_CI_AS"

// viewSQL fakes the collation reads of the views.
type viewSQL struct {
	fakeSQL
	server mssql.ServerCollation
	dbs    []mssql.DatabaseCollation
	states map[string]mssql.CollationState
	cols   []mssql.CharColumn
	infos  map[string]mssql.CollationInfo
	err    error
}

func (f *viewSQL) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return f.server, f.err
}

func (f *viewSQL) DatabaseCollations(context.Context) ([]mssql.DatabaseCollation, error) {
	return f.dbs, f.err
}

func (f *viewSQL) CollationState(_ context.Context, db string) (mssql.CollationState, error) {
	return f.states[db], f.err
}

func (f *viewSQL) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return f.cols, f.err
}

func (f *viewSQL) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	i, ok := f.infos[name]
	return i, ok, f.err
}

func (f *viewSQL) Collations(context.Context) ([]mssql.CollationInfo, error) {
	return []mssql.CollationInfo{{Name: "A"}, {Name: "B"}}, f.err
}

func newViewSession(f *viewSQL) *Session {
	return &Session{Name: "dev", Target: config.Target{Instance: "dev-sql"}, SQL: f}
}

func TestCollationOverviewFlags(t *testing.T) {
	f := &viewSQL{
		server: mssql.ServerCollation{Server: srvColl, Tempdb: srvColl},
		dbs: []mssql.DatabaseCollation{
			{Name: "same", Collation: srvColl, State: "ONLINE", Containment: "NONE"},
			{Name: "diff", Collation: "Latin1_General_CS_AS", State: "ONLINE", Containment: "NONE"},
			{Name: "contained", Collation: "Latin1_General_CS_AS", State: "ONLINE", Containment: "PARTIAL"},
			{Name: "off", State: "OFFLINE", Containment: "NONE"},
		},
	}
	o, err := newViewSession(f).CollationOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.Target != "dev" || o.Instance != "dev-sql" || o.Server != srvColl || o.Tempdb != srvColl || !o.ServerFixed {
		t.Fatalf("overview %+v", o)
	}
	want := map[string][2]bool{"same": {false, false}, "diff": {true, true}, "contained": {true, false}, "off": {false, false}}
	if len(o.Databases) != 4 {
		t.Fatalf("databases %+v", o.Databases)
	}
	for _, d := range o.Databases {
		if w := want[d.Name]; d.MismatchServer != w[0] || d.TempTableRisk != w[1] {
			t.Errorf("%s: mismatch %v risk %v, want %v", d.Name, d.MismatchServer, d.TempTableRisk, w)
		}
	}
	f.err = errors.New("boom")
	if _, err := newViewSession(f).CollationOverview(context.Background()); err == nil {
		t.Fatal("an error of the read was swallowed")
	}
}

func col(schema, table, name, typ, base, coll string, length int) mssql.CharColumn {
	return mssql.CharColumn{Schema: schema, Table: table, Column: name, Type: typ, BaseType: base, MaxLength: length, Collation: coll}
}

func TestDatabaseCollationGroupsAndColumns(t *testing.T) {
	const def = "Latin1_General_CS_AS"
	text := col("dbo", "t2", "body", "text", "text", srvColl, 16)
	computed := col("dbo", "t2", "calc", "varchar", "varchar", "Cyrillic_General_CI_AS", 10)
	computed.Computed = true
	f := &viewSQL{
		server: mssql.ServerCollation{Server: srvColl, Tempdb: srvColl},
		states: map[string]mssql.CollationState{"sales": {DatabaseCollation: mssql.DatabaseCollation{
			Name: "sales", Collation: def, State: "ONLINE", Containment: "NONE"}, Exists: true}},
		cols: []mssql.CharColumn{
			col("dbo", "t1", "a", "nvarchar", "nvarchar", def, 100),
			col("dbo", "t1", "b", "varchar", "varchar", def, 10),
			col("dbo", "t2", "c", "varchar", "varchar", def, -1),
			col("dbo", "t1", "legacy", "varchar", "varchar", srvColl, 5),
			text, computed,
		},
		infos: map[string]mssql.CollationInfo{def: {Name: def, CodePage: 1252}, srvColl: {CodePage: 1252}},
	}
	v, err := newViewSession(f).DatabaseCollation(context.Background(), "sales")
	if err != nil {
		t.Fatal(err)
	}
	if v.Collation != def || v.Server != srvColl || !v.MismatchServer || !v.TempTableRisk || v.CharColumns != 6 || v.Truncated {
		t.Fatalf("view %+v", v)
	}
	wantGroups := []CollationGroup{
		{Collation: def, Default: true, Columns: 3, Tables: 2, CodePage: 1252},
		{Collation: srvColl, Columns: 2, Tables: 2, CodePage: 1252},
		{Collation: "Cyrillic_General_CI_AS", Columns: 1, Tables: 1}, // unknown to the fake: no code page
	}
	if fmt.Sprint(v.Groups) != fmt.Sprint(wantGroups) {
		t.Fatalf("groups %+v, want %+v", v.Groups, wantGroups)
	}
	if len(v.Columns) != 3 || v.Columns[0].Column != "legacy" || v.Columns[0].Unsupported != "" ||
		v.Columns[1].Unsupported == "" || v.Columns[2].Unsupported == "" {
		t.Fatalf("columns %+v", v.Columns)
	}
}

func TestDatabaseCollationTruncatesAndNoColumns(t *testing.T) {
	const def = "Latin1_General_CS_AS"
	f := &viewSQL{
		server: mssql.ServerCollation{Server: def, Tempdb: def},
		states: map[string]mssql.CollationState{"big": {DatabaseCollation: mssql.DatabaseCollation{
			Name: "big", Collation: def, State: "ONLINE", Containment: "NONE"}, Exists: true}},
	}
	for i := range maxCollationColumns + 5 {
		f.cols = append(f.cols, col("dbo", "t", fmt.Sprintf("c%d", i), "varchar", "varchar", srvColl, 5))
	}
	v, err := newViewSession(f).DatabaseCollation(context.Background(), "big")
	if err != nil || !v.Truncated || len(v.Columns) != maxCollationColumns || v.CharColumns != maxCollationColumns+5 {
		t.Fatalf("truncated view: %v, %d columns, truncated %v, total %d", err, len(v.Columns), v.Truncated, v.CharColumns)
	}
	if v.MismatchServer || v.TempTableRisk {
		t.Fatalf("flags on a database that matches the server: %+v", v)
	}
	f.cols = nil
	v, err = newViewSession(f).DatabaseCollation(context.Background(), "big")
	if err != nil || v.Columns == nil || v.Groups == nil || len(v.Groups) != 1 || !v.Groups[0].Default {
		t.Fatalf("empty view %+v, %v", v, err)
	}
}

func TestDatabaseCollationErrors(t *testing.T) {
	f := &viewSQL{
		server: mssql.ServerCollation{Server: srvColl, Tempdb: srvColl},
		states: map[string]mssql.CollationState{"off": {DatabaseCollation: mssql.DatabaseCollation{Name: "off", State: "OFFLINE"}, Exists: true}},
	}
	s := newViewSession(f)
	if _, err := s.DatabaseCollation(context.Background(), "nope"); !errors.Is(err, mssql.ErrNoDatabase) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := s.DatabaseCollation(context.Background(), "off"); !errors.Is(err, ErrNotOnline) {
		t.Fatalf("offline: %v", err)
	}
	f.err = errors.New("boom")
	if _, err := s.DatabaseCollation(context.Background(), "off"); err == nil || errors.Is(err, ErrNotOnline) {
		t.Fatalf("read error: %v", err)
	}
	if got, err := newViewSession(&viewSQL{}).Collations(context.Background()); err != nil || len(got) != 2 {
		t.Fatalf("Collations: %v %v", got, err)
	}
}
