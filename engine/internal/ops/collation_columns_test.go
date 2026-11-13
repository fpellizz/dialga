package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	mssqldb "github.com/microsoft/go-mssqldb"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// convertFake is fakeSQL with the reads of convert-columns: a database
// "sales" (default fromColl) whose catalog is cat; Exec of a unit converts
// its columns in cat, unless execErr.
type convertFake struct {
	*fakeSQL
	state  mssql.CollationState
	colls  map[string]mssql.CollationInfo
	cat    *catBuilder
	exists func(query string) (bool, error)
	reads  int
}

func newConvertFake(cat *catBuilder) *convertFake {
	f := &convertFake{
		fakeSQL: &fakeSQL{states: map[string]string{"sales": "ONLINE"}},
		state: mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: fromColl, State: "ONLINE"},
			UserAccess: "MULTI_USER", CanAlter: true, Exists: true},
		colls: map[string]mssql.CollationInfo{
			fromColl:                 {Name: fromColl, CodePage: 1252},
			toColl:                   {Name: toColl, CodePage: 1252},
			"Cyrillic_General_CI_AS": {Name: "Cyrillic_General_CI_AS", CodePage: 1251},
		},
		cat: cat,
	}
	f.onExec = func(_ *fakeSQL, stmt string) error {
		if f.execErr != nil {
			return nil
		}
		for i, c := range f.cat.cat.Columns {
			if strings.Contains(stmt, "ALTER COLUMN ["+c.Column+"]") && strings.Contains(stmt, "["+c.Table+"] ALTER") {
				coll := stmt[strings.Index(stmt, " COLLATE ")+9:]
				f.cat.cat.Columns[i].Collation = coll[:strings.IndexAny(coll, " ;\n")]
			}
		}
		return nil
	}
	return f
}

func (f *convertFake) CollationState(context.Context, string) (mssql.CollationState, error) {
	return f.state, nil
}

func (f *convertFake) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	c, ok := f.colls[name]
	return c, ok, nil
}

func (f *convertFake) ConversionCatalog(context.Context, string) (mssql.ConversionCatalog, error) {
	f.reads++
	return f.cat.cat, nil
}

func (f *convertFake) Exists(_ context.Context, query string) (bool, error) {
	if f.exists == nil {
		return false, nil
	}
	return f.exists(query)
}

func convertSession(f *convertFake) *Session {
	return &Session{Name: "dev-sql", Target: testTarget, SQL: f}
}

// salesCatalog: orders (PK on code) referenced by lines, and a lonely
// customers table.
func salesCatalog() *catBuilder {
	return newCatalog().
		table("orders", "code", "int:n").table("lines", "order_code").table("customers", "name").
		index("orders", "PK_orders", 1, []string{"code"}, pk).
		fk("FK_lines_orders", "lines", []string{"order_code"}, "orders", []string{"code"}, "PK_orders")
}

func TestValidateConvertColumns(t *testing.T) {
	many := make([]mssql.TableRef, MaxConvertTables+1)
	for i := range many {
		many[i] = mssql.TableRef{Schema: "dbo", Name: "t" + strings.Repeat("x", i%100) + string(rune('a'+i%26))}
	}
	for name, p := range map[string]Params{
		"system db":   {Database: "master"},
		"collation":   {Database: "sales", Collation: "Latin1 General"},
		"table name":  {Database: "sales", Tables: []mssql.TableRef{{Schema: "dbo", Name: ""}}},
		"schema":      {Database: "sales", Tables: []mssql.TableRef{{Schema: "d\tbo", Name: "t"}}},
		"duplicate":   {Database: "sales", Tables: []mssql.TableRef{{Schema: "dbo", Name: "T"}, {Schema: "DBO", Name: "t"}}},
		"too many":    {Database: "sales", Tables: many},
		"restore key": {Database: "sales", Key: "a.bak"},
		"login":       {Database: "sales", Login: "ana"},
	} {
		if err := Validate(OpConvertColumns, p, testTarget); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
	for name, p := range map[string]Params{
		"database default": {Database: "sales"},
		"collation":        {Database: "sales", Collation: toColl, SkipDataChecks: true},
		"tables":           {Database: "sales", Tables: []mssql.TableRef{{Schema: "dbo", Name: "o'rders]"}}},
	} {
		if err := Validate(OpConvertColumns, p, testTarget); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestConvertColumnsPlan(t *testing.T) {
	f := newConvertFake(salesCatalog())
	p, err := convertSession(f).Build(context.Background(), OpConvertColumns, Params{Database: "sales", Collation: toColl})
	if err != nil || !p.Ready() {
		t.Fatalf("err %v checks %+v", err, p.Checks)
	}
	if got := verdicts(p); got != "database:ok access:ok permission:ok collation:ok scope:ok uniqueness:ok references:ok code-page:ok size:ok sessions:ok tasks:ok" {
		t.Fatalf("checks %s", got)
	}
	if ids(p) != "convert,convert" || p.Plan.Subject != "sales" || p.Plan.Confirmation() != "sales" {
		t.Fatalf("actions %s subject %q", ids(p), p.Plan.Subject)
	}
	for _, a := range p.Plan.Actions {
		if a.Risk != core.Destructive || a.Kind != core.KindSQL {
			t.Fatalf("action %+v", a)
		}
	}
	if !strings.HasPrefix(p.Plan.Actions[0].Title, "Convert 1 column(s) of [dbo].[customers] to "+toColl) ||
		!strings.HasPrefix(p.Plan.Actions[1].Title, "Convert 2 column(s) of [dbo].[lines], [dbo].[orders] to ") {
		t.Fatalf("titles %q, %q", p.Plan.Actions[0].Title, p.Plan.Actions[1].Title)
	}
	steps := p.CollationSteps()
	if len(steps) != 2 || steps[1].Unit.Database != "sales" || steps[1].Unit.Collation != toColl || len(steps[1].Unit.ForeignKeys) != 1 {
		t.Fatalf("steps %+v", steps)
	}
	for i, st := range steps {
		if err := st.Validate(testTarget); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if stmt, _ := st.Statement(); stmt != p.Plan.Actions[i].Statement {
			t.Fatalf("statement %d differs", i)
		}
	}
	c := p.Collation
	if c == nil || c.Units != 2 || c.Tables != 3 || c.Columns != 3 || c.Indexes != 1 || c.ForeignKeys != 1 || c.DataMB != 3 || c.To != toColl {
		t.Fatalf("summary %+v", c)
	}
	if p.Params.Collation != toColl {
		t.Fatalf("params %+v", p.Params)
	}
}

func TestConvertColumnsResolvesTheDatabaseDefault(t *testing.T) {
	f := newConvertFake(salesCatalog())
	f.state.Collation = toColl
	f.cat.column("customers.name", func(c *mssql.CharColumn) { c.Collation = "Cyrillic_General_CI_AS" })
	p, err := convertSession(f).ConvertColumns(context.Background(), Params{Database: "sales"})
	if err != nil || !p.Ready() || p.Params.Collation != toColl || ids(p) != "convert,convert" {
		t.Fatalf("err %v params %+v checks %s", err, p.Params, verdicts(p))
	}
	if !strings.Contains(p.Checks[3].Detail, "the default of sales") {
		t.Fatalf("collation check %+v", p.Checks[3])
	}
}

func TestConvertColumnsRefusals(t *testing.T) {
	temporal := func() *catBuilder {
		b := salesCatalog().tableWith("hist", mssqlTemporal(), "name")
		return b
	}
	tests := []struct {
		name    string
		cat     *catBuilder
		params  Params
		setup   func(*convertFake)
		verdict string // the check that decides, "step:status"
		actions string
	}{
		{"offline", salesCatalog(), Params{Database: "sales"}, func(f *convertFake) { f.states["sales"] = "RESTORING" }, "database:failed", ""},
		{"read-only", salesCatalog(), Params{Database: "sales"}, func(f *convertFake) { f.state.ReadOnly = true }, "access:failed", ""},
		{"single user", salesCatalog(), Params{Database: "sales"}, func(f *convertFake) { f.state.UserAccess = "SINGLE_USER" }, "access:failed", ""},
		{"no permission", salesCatalog(), Params{Database: "sales"}, func(f *convertFake) { f.state.CanAlter = false }, "permission:failed", ""},
		{"unknown collation", salesCatalog(), Params{Database: "sales", Collation: "Klingon_CI_AS"}, nil, "collation:failed", ""},
		{"missing table", salesCatalog(), Params{Database: "sales", Collation: toColl, Tables: []mssql.TableRef{{Schema: "dbo", Name: "nope"}}}, nil, "tables:failed", ""},
		{"foreign keys bring in tables", salesCatalog(), Params{Database: "sales", Collation: toColl, Tables: []mssql.TableRef{{Schema: "dbo", Name: "LINES"}}}, nil, "fk-closure:warning", "convert"},
		{"nothing to convert", salesCatalog(), Params{Database: "sales"}, nil, "columns:warning", ""},
		{"explicit refused table fails", temporal(), Params{Database: "sales", Collation: toColl, Tables: []mssql.TableRef{{Schema: "dbo", Name: "hist"}}}, nil, "refused:failed", ""},
		{"whole database refused table warns", temporal(), Params{Database: "sales", Collation: toColl}, nil, "refused:warning", "convert,convert"},
		{"duplicates", salesCatalog(), Params{Database: "sales", Collation: toColl}, func(f *convertFake) {
			f.exists = func(q string) (bool, error) { return strings.Contains(q, "GROUP BY"), nil }
		}, "uniqueness:failed", ""},
		{"orphans", salesCatalog(), Params{Database: "sales", Collation: toColl}, func(f *convertFake) {
			f.exists = func(q string) (bool, error) { return strings.Contains(q, "NOT EXISTS"), nil }
		}, "references:failed", ""},
		{"check timeout", salesCatalog(), Params{Database: "sales", Collation: toColl}, func(f *convertFake) {
			f.exists = func(q string) (bool, error) { return false, context.DeadlineExceeded }
		}, "uniqueness:warning", "convert,convert"},
		{"skip data checks", salesCatalog(), Params{Database: "sales", Collation: toColl, SkipDataChecks: true}, func(f *convertFake) {
			f.exists = func(string) (bool, error) { return true, nil }
		}, "data-checks:warning", "convert,convert"},
		{"sessions and tasks warn", salesCatalog(), Params{Database: "sales", Collation: toColl}, func(f *convertFake) {
			f.sessions = map[string]int{"sales": 2}
			f.active = map[string]int{"sales": 1}
		}, "sessions:warning", "convert,convert"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConvertFake(tc.cat)
			if tc.setup != nil {
				tc.setup(f)
			}
			p, err := convertSession(f).ConvertColumns(context.Background(), tc.params)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(" "+verdicts(p)+" ", " "+tc.verdict+" ") {
				t.Fatalf("checks %s, want %s", verdicts(p), tc.verdict)
			}
			if ids(p) != tc.actions {
				t.Fatalf("actions %q, want %q (checks %s)", ids(p), tc.actions, verdicts(p))
			}
			if len(f.ran) != 0 {
				t.Fatalf("a plan ran %v", f.ran)
			}
		})
	}
}

func mssqlTemporal() mssql.CatalogTable { return mssql.CatalogTable{Temporal: 2} }

func TestConvertColumnsCodePage(t *testing.T) {
	cat := newCatalog().table("customers", "city", "nick")
	cat.column("customers.city", func(c *mssql.CharColumn) { c.Type, c.BaseType, c.MaxLength = "varchar", "varchar", 40 })
	f := newConvertFake(cat)
	var queries []string
	f.exists = func(q string) (bool, error) { queries = append(queries, q); return true, nil }
	p, err := convertSession(f).ConvertColumns(context.Background(), Params{Database: "sales", Collation: "Cyrillic_General_CI_AS"})
	if err != nil || p.Ready() || !strings.Contains(verdicts(p), "code-page:failed") {
		t.Fatalf("err %v checks %s", err, verdicts(p))
	}
	// only the varchar column, with its length
	if len(queries) != 1 || !strings.Contains(queries[0], "[city]") || !strings.Contains(queries[0], "> 40") {
		t.Fatalf("queries %q", queries)
	}
	// same code page: nothing to check
	queries = nil
	if p, _ := convertSession(newConvertFake(cat)).ConvertColumns(context.Background(), Params{Database: "sales", Collation: toColl}); !p.Ready() {
		t.Fatalf("checks %s", verdicts(p))
	}
}

func TestConvertColumnsLimit(t *testing.T) {
	cat := newCatalog()
	for i := range MaxConvertTables + 1 {
		cat.table(fmt.Sprintf("t%03d", i), "name")
	}
	p, err := convertSession(newConvertFake(cat)).ConvertColumns(context.Background(), Params{Database: "sales", Collation: toColl})
	if err != nil || !strings.Contains(verdicts(p), "limit:failed") || ids(p) != "" {
		t.Fatalf("err %v checks %s", err, verdicts(p))
	}
}

func TestRunConvertIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newConvertFake(salesCatalog())
	s := convertSession(f)
	p, err := s.ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
	if err != nil || !p.Ready() {
		t.Fatalf("err %v checks %s", err, verdicts(p))
	}
	st := p.CollationSteps()[1]
	out, err := s.RunCollationStep(ctx, st)
	if err != nil || len(f.ran) != 1 || out != "2 column(s) of 2 table(s) converted to "+toColl {
		t.Fatalf("first run %q %v ran %d", out, err, len(f.ran))
	}
	// a second run (a resumed job) finds it done
	out, err = s.RunCollationStep(ctx, st)
	if err != nil || len(f.ran) != 1 || !strings.HasPrefix(out, "already converted") {
		t.Fatalf("second run %q %v ran %d", out, err, len(f.ran))
	}
	// one column changed outside Dialga: refused
	f.cat.column("lines.order_code", func(c *mssql.CharColumn) { c.Collation = fromColl })
	if _, err := s.RunCollationStep(ctx, st); !errors.Is(err, errUnitState) || len(f.ran) != 1 {
		t.Fatalf("mixed: %v ran %d", err, len(f.ran))
	}
	// converted, but an index the unit creates is missing
	f.cat.column("lines.order_code", func(c *mssql.CharColumn) { c.Collation = toColl })
	f.cat.cat.Indexes = nil
	if _, err := s.RunCollationStep(ctx, st); !errors.Is(err, errUnitState) {
		t.Fatalf("missing index: %v", err)
	}
}

func TestRunConvertLockTimeout(t *testing.T) {
	ctx := context.Background()
	f := newConvertFake(salesCatalog())
	s := convertSession(f)
	p, _ := s.ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
	f.execErr = mssqldb.Error{Number: 1222, Message: "Lock request time out period exceeded."}
	_, err := s.RunCollationStep(ctx, p.CollationSteps()[0])
	if err == nil || !strings.Contains(err.Error(), "blocked by other sessions for 30 s: nothing changed") {
		t.Fatalf("err %v", err)
	}
	f.execErr = errors.New("Cannot insert duplicate key")
	if _, err := s.RunCollationStep(ctx, p.CollationSteps()[0]); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err %v", err)
	}
}

// The options a unit does not keep are one warning; a name it could not
// journal fails the preview.
func TestConvertColumnsOptionsAndNames(t *testing.T) {
	ctx := context.Background()
	cat := salesCatalog().stat("customers", "ST_name", false, false, "name").
		index("customers", "IX_name", 2, []string{"name"}, func(ix *mssql.CatalogIndex) { ix.OptimizeSequentialKey = true }).
		prop(7, "customers", "IX_name", "MS_Description", "varchar")
	cat.cat.Stats[0].PersistedSample = true
	p, err := convertSession(newConvertFake(cat)).ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
	if err != nil || !p.Ready() || !strings.Contains(verdicts(p), "options:warning") {
		t.Fatalf("err %v checks %s", err, verdicts(p))
	}
	detail := p.Checks[slices.IndexFunc(p.Checks, func(c check.Event) bool { return c.Step == "options" })].Detail
	for _, want := range []string{"OPTIMIZE_FOR_SEQUENTIAL_KEY of index dbo.customers.IX_name", "persisted sample of statistic dbo.customers.ST_name",
		"varchar type of extended property MS_Description"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("options %q lacks %q", detail, want)
		}
	}

	bad := salesCatalog().column("customers.name", func(c *mssql.CharColumn) { c.Type = "nvarchar"; c.MaxLength = 99999 })
	p, err = convertSession(newConvertFake(bad)).ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
	if err != nil || p.Ready() || !strings.Contains(verdicts(p), "names:failed") || ids(p) != "" {
		t.Fatalf("err %v checks %s actions %q", err, verdicts(p), ids(p))
	}
}

// A unit runs only as planned: a column or a dependent object that changed
// since the plan refuses it, nothing runs; a new auto-created statistic is
// dropped with the rest.
func TestRunConvertRefusesChangedTables(t *testing.T) {
	ctx := context.Background()
	plan := func(t *testing.T) (*convertFake, *Session, CollationStep) {
		t.Helper()
		f := newConvertFake(salesCatalog().check("customers", "CK_name", "name", nil, "([name]<>N'')"))
		s := convertSession(f)
		p, err := s.ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
		if err != nil || !p.Ready() {
			t.Fatalf("err %v checks %s", err, verdicts(p))
		}
		return f, s, p.CollationSteps()[0] // customers
	}
	for name, change := range map[string]func(*convertFake){
		"length": func(f *convertFake) { f.cat.column("customers.name", func(c *mssql.CharColumn) { c.MaxLength = 400 }) },
		"nullable": func(f *convertFake) {
			f.cat.column("customers.name", func(c *mssql.CharColumn) { c.Nullable = !c.Nullable })
		},
		"type": func(f *convertFake) {
			f.cat.column("customers.name", func(c *mssql.CharColumn) { c.Type, c.BaseType = "varchar", "varchar" })
		},
		"check":     func(f *convertFake) { f.cat.cat.Checks[0].Def.Definition = "(len([name])>(1))" },
		"new index": func(f *convertFake) { f.cat.index("customers", "IX_name", 2, []string{"name"}) },
		"refused": func(f *convertFake) {
			f.cat.cat.Masked = []mssql.ColumnKey{{ObjectID: f.cat.ids["customers"], ColumnID: 1}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, s, st := plan(t)
			change(f)
			_, err := s.RunCollationStep(ctx, st)
			if !errors.Is(err, errTablesChanged) || len(f.ran) != 0 {
				t.Fatalf("err %v ran %v", err, f.ran)
			}
		})
	}
	t.Run("auto statistic", func(t *testing.T) {
		f, s, st := plan(t)
		f.cat.stat("customers", "_WA_Sys_name", true, false, "name")
		if _, err := s.RunCollationStep(ctx, st); err != nil || len(f.ran) != 1 || !strings.Contains(f.ran[0], "DROP STATISTICS [dbo].[customers].[_WA_Sys_name];") {
			t.Fatalf("err %v ran %v", err, f.ran)
		}
	})
}

// "Already converted" also needs the extended properties the unit adds.
func TestRunConvertAlreadyNeedsTheProperties(t *testing.T) {
	ctx := context.Background()
	f := newConvertFake(salesCatalog().prop(1, "orders", "PK_orders", "MS_Description", "nvarchar"))
	s := convertSession(f)
	p, err := s.ConvertColumns(ctx, Params{Database: "sales", Collation: toColl})
	if err != nil || !p.Ready() {
		t.Fatalf("err %v checks %s", err, verdicts(p))
	}
	st := p.CollationSteps()[1]
	if len(st.Unit.Tables[1].ExtendedProperties) != 1 {
		t.Fatalf("unit %+v", st.Unit.Tables[1])
	}
	f.cat.column("orders.code", func(c *mssql.CharColumn) { c.Collation = toColl })
	f.cat.column("lines.order_code", func(c *mssql.CharColumn) { c.Collation = toColl })
	if out, err := s.RunCollationStep(ctx, st); err != nil || !strings.HasPrefix(out, "already converted") {
		t.Fatalf("%q %v", out, err)
	}
	f.cat.cat.ExtendedProperties = nil
	if _, err := s.RunCollationStep(ctx, st); !errors.Is(err, errUnitState) || len(f.ran) != 0 {
		t.Fatalf("property missing: %v ran %v", err, f.ran)
	}
}
