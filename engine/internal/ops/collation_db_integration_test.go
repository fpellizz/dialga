//go:build integration

package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// collSQL is the real SQL session, except for CharColumns, which is the
// read side's job (work package A): here a local query stands in for it.
type collSQL struct{ mssql.Session }

func (c collSQL) CharColumns(ctx context.Context, db string) ([]mssql.CharColumn, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT s.name, t.name, c.name, ty.name, c.collation_name
		FROM [`+db+`].sys.columns c JOIN [`+db+`].sys.tables t ON t.object_id = c.object_id
		JOIN [`+db+`].sys.schemas s ON s.schema_id = t.schema_id
		JOIN [`+db+`].sys.types ty ON ty.user_type_id = c.user_type_id
		WHERE c.collation_name IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []mssql.CharColumn
	for rows.Next() {
		var col mssql.CharColumn
		if err := rows.Scan(&col.Schema, &col.Table, &col.Column, &col.Type, &col.Collation); err != nil {
			return nil, err
		}
		col.BaseType = col.Type
		out = append(out, col)
	}
	return out, rows.Err()
}

const (
	ciOld = "SQL_Latin1_General_CP1_CI_AS"
	csNew = "Latin1_General_100_CS_AS"
)

// collDB creates a database owned by admin (like one the RDS master user
// restored) with the collation and runs the script in it as sa.
func collDB(t *testing.T, name, collation, script string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`IF DB_ID(N'` + name + `') IS NOT NULL BEGIN ALTER DATABASE [` + name + `] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE [` + name + `]; END`,
		`CREATE DATABASE [` + name + `] COLLATE ` + collation,
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`IF DB_ID(N'` + name + `') IS NOT NULL BEGIN ALTER DATABASE [` + name + `] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE [` + name + `]; END`)
	})
	if script != "" {
		if err := runScript(ctx, sa, "USE ["+name+"];\nGO\n"+script+"\nGO\nUSE master;"); err != nil {
			t.Fatal(err)
		}
	}
}

var grantServerState sync.Once

// collSession is a session as the RDS master user has it: VIEW SERVER STATE,
// which the mock admin lacks. Only the sessions check needs it (without it
// sys.dm_exec_sessions shows only the session itself): CollationState reads
// catalog views only.
func collSession(t *testing.T) *Session {
	t.Helper()
	grantServerState.Do(func() {
		if _, err := sa.Exec(`GRANT VIEW SERVER STATE TO admin`); err != nil {
			t.Fatal(err)
		}
	})
	s := goldenSession(nil)
	s.SQL = collSQL{mssql.Session{DB: admin}}
	return s
}

func stateOf(t *testing.T, db string) mssql.CollationState {
	t.Helper()
	st, err := mssql.Session{DB: sa}.CollationState(context.Background(), db)
	if err != nil || !st.Exists {
		t.Fatalf("state of %s: %+v %v", db, st, err)
	}
	return st
}

func TestIntegrationSetCollation(t *testing.T) {
	ctx := context.Background()
	s := collSession(t)
	build := func(db, coll string) *Prepared {
		t.Helper()
		p, err := s.SetCollation(ctx, Params{Database: db, Collation: coll})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	checkOf := func(p *Prepared, step string) string {
		for _, ev := range p.Checks {
			if ev.Step == step {
				return string(ev.Status) + ": " + ev.Detail
			}
		}
		return ""
	}

	t.Run("success", func(t *testing.T) {
		collDB(t, "cs_ok", ciOld, `CREATE TABLE dbo.t (id int PRIMARY KEY, name varchar(20));`)
		p := build("cs_ok", csNew)
		if !p.Ready() || ids(p) != "set-collation" {
			t.Fatalf("not ready: %+v", p.Checks)
		}
		if got := checkOf(p, "columns"); !strings.Contains(got, "1 column(s) keep "+ciOld) {
			t.Errorf("columns check: %s", got)
		}
		if _, err := p.Execute(ctx, ExecOptions{Confirm: "cs_ok"}); err != nil {
			t.Fatal(err)
		}
		st := stateOf(t, "cs_ok")
		if st.Collation != csNew || st.UserAccess != "MULTI_USER" {
			t.Fatalf("after: %+v", st)
		}
		// existing columns keep their collation
		var col string
		if err := sa.QueryRow(`SELECT collation_name FROM cs_ok.sys.columns WHERE object_id = OBJECT_ID(N'cs_ok.dbo.t') AND name = 'name'`).Scan(&col); err != nil || col != ciOld {
			t.Fatalf("column collation %q %v", col, err)
		}
		// idempotent: a second plan has nothing to do, a second run of the step too
		if p2 := build("cs_ok", csNew); len(p2.Plan.Actions) != 0 || !strings.Contains(checkOf(p2, "collation"), "nothing to do") {
			t.Fatalf("second plan: %+v", p2.Checks)
		}
		if d, err := s.RunCollationStep(ctx, p.CollationSteps()[0]); err != nil || !strings.Contains(d, "already") {
			t.Fatalf("second run: %q %v", d, err)
		}
	})

	t.Run("sessions are killed", func(t *testing.T) {
		collDB(t, "cs_sess", ciOld, `CREATE TABLE dbo.t (id int);`)
		conn, err := sa.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.ExecContext(ctx, `USE cs_sess`); err != nil {
			t.Fatal(err)
		}
		p := build("cs_sess", csNew)
		if !p.Ready() || !strings.HasPrefix(checkOf(p, "sessions"), "warning") {
			t.Fatalf("checks %+v", p.Checks)
		}
		if _, err := p.Execute(ctx, ExecOptions{Confirm: "cs_sess"}); err != nil {
			t.Fatal(err)
		}
		if stateOf(t, "cs_sess").Collation != csNew {
			t.Fatal("not changed")
		}
		if _, err := conn.ExecContext(ctx, `SELECT 1`); err == nil {
			t.Fatal("the other session was not disconnected")
		}
	})

	t.Run("schema-bound view with a literal blocks", func(t *testing.T) {
		collDB(t, "cs_view", ciOld, `CREATE TABLE dbo.t (id int PRIMARY KEY, name varchar(20));
GO
CREATE VIEW dbo.v WITH SCHEMABINDING AS SELECT id, 'x' AS k FROM dbo.t;`)
		p := build("cs_view", csNew)
		if p.Ready() || !strings.HasPrefix(checkOf(p, "dependents"), "failed") || !strings.Contains(checkOf(p, "dependents"), "dbo.v") {
			t.Fatalf("checks %+v", p.Checks)
		}
		if len(p.Plan.Actions) != 0 {
			t.Error("actions in a refused plan")
		}
	})

	t.Run("failure past the pre-flight reopens the database", func(t *testing.T) {
		collDB(t, "cs_fail", ciOld, `CREATE TABLE dbo.t (id int, s varchar(5), CONSTRAINT ck_s CHECK ([s]='A'));`)
		step := CollationStep{ID: CollSetDatabase, Database: "cs_fail", Collation: csNew, From: ciOld}
		_, err := s.RunCollationStep(ctx, step)
		if err == nil || !strings.Contains(err.Error(), "5075") || !strings.Contains(err.Error(), "open to all sessions again") {
			t.Fatalf("error %v", err)
		}
		if st := stateOf(t, "cs_fail"); st.UserAccess != "MULTI_USER" || st.Collation != ciOld {
			t.Fatalf("after the failure: %+v", st)
		}
	})

	t.Run("names that collide", func(t *testing.T) {
		collDB(t, "cs_names", csNew, `CREATE TABLE dbo.T (id int);
GO
CREATE TABLE dbo.t (id int);`)
		p := build("cs_names", "Latin1_General_100_CI_AS")
		if p.Ready() || !strings.HasPrefix(checkOf(p, "names"), "failed") || !strings.Contains(checkOf(p, "names"), "dbo.") {
			t.Fatalf("checks %+v", p.Checks)
		}
		// and SQL Server really refuses it
		_, err := s.RunCollationStep(ctx, CollationStep{ID: CollSetDatabase, Database: "cs_names", Collation: "Latin1_General_100_CI_AS", From: csNew})
		if err == nil {
			t.Fatal("SQL Server accepted colliding names")
		}
		if st := stateOf(t, "cs_names"); st.UserAccess != "MULTI_USER" {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("errors of the instance", func(t *testing.T) {
		p := build("no_such_db", csNew)
		if p.Ready() {
			t.Fatal("ready for a missing database")
		}
		if _, err := s.SetCollation(ctx, Params{Database: "cs_ok", Collation: "No_Such_Collation"}); err != nil {
			t.Fatal(err)
		}
	})
}

// Which objects does SQL Server really refuse ALTER DATABASE ... COLLATE
// for? The blocker query must say the same, object by object: a heuristic
// that is too strict refuses harmless changes, too loose lets the batch fail
// (it reopens the database, but the run fails).
func TestIntegrationSetCollationHeuristic(t *testing.T) {
	ctx := context.Background()
	s := collSession(t)
	cases := []struct {
		name   string
		script string
		// blocks: SQL Server refuses the change; kind: what the blocker query must call it
		blocks bool
		kind   string
	}{
		{"schema-bound view on int columns", `CREATE TABLE dbo.t (id int PRIMARY KEY, qty int);
GO
CREATE VIEW dbo.v WITH SCHEMABINDING AS SELECT id, qty FROM dbo.t;`, true, "schema-bound"},
		{"schema-bound view with a literal", `CREATE TABLE dbo.t (id int PRIMARY KEY);
GO
CREATE VIEW dbo.v WITH SCHEMABINDING AS SELECT id, 'x' AS k FROM dbo.t;`, true, "schema-bound"},
		{"schema-bound view on a char column", `CREATE TABLE dbo.t (id int PRIMARY KEY, name varchar(20));
GO
CREATE VIEW dbo.v WITH SCHEMABINDING AS SELECT id, name FROM dbo.t;`, true, "schema-bound"},
		{"check on int", `CREATE TABLE dbo.t (id int, qty int, CONSTRAINT ck CHECK (qty > 0));`, true, "check-constraint"},
		{"check on a char column", `CREATE TABLE dbo.t (id int, s varchar(5), CONSTRAINT ck CHECK ([s]='A'));`, true, "check-constraint"},
		{"check with a literal on an int", `CREATE TABLE dbo.t (id int, qty int, CONSTRAINT ck CHECK (qty > 0 AND 'a' = 'a'));`, true, "check-constraint"},
		{"computed column on int", `CREATE TABLE dbo.t (id int, qty int, dbl AS qty * 2);`, true, "computed-column"},
		{"computed column with a literal", `CREATE TABLE dbo.t (id int, c AS 'x' + CONVERT(varchar(5), id));`, true, "computed-column"},
		{"computed column on a char column", `CREATE TABLE dbo.t (id int, name varchar(20), up AS UPPER(name));`, true, "computed-column"},
		{"inline table function on int", `CREATE TABLE dbo.t (id int);
GO
CREATE FUNCTION dbo.f() RETURNS TABLE WITH SCHEMABINDING AS RETURN SELECT id FROM dbo.t;`, true, "schema-bound"},
		{"inline table function with a char column", `CREATE TABLE dbo.t (id int, name varchar(20));
GO
CREATE FUNCTION dbo.f() RETURNS TABLE WITH SCHEMABINDING AS RETURN SELECT name FROM dbo.t;`, true, "schema-bound"},
		{"multi-statement function with a char column", `CREATE FUNCTION dbo.f() RETURNS @r TABLE (name varchar(20)) AS BEGIN RETURN END;`, true, "table-function"},
		{"scalar function with a char parameter, schema-bound", `CREATE FUNCTION dbo.f(@s varchar(5)) RETURNS int WITH SCHEMABINDING AS BEGIN RETURN LEN(@s) END;`, true, "schema-bound"},
		{"scalar function on int, schema-bound", `CREATE FUNCTION dbo.f(@i int) RETURNS int WITH SCHEMABINDING AS BEGIN RETURN @i + 1 END;`, true, "schema-bound"},
		{"disabled check", `CREATE TABLE dbo.t (id int, qty int, CONSTRAINT ck CHECK (qty > 0));
ALTER TABLE dbo.t NOCHECK CONSTRAINT ck;`, true, "check-constraint"},
		{"multi-statement function on int, not schema-bound", `CREATE FUNCTION dbo.f() RETURNS @r TABLE (id int) AS BEGIN RETURN END;`, false, ""},
		{"inline function with a char column, not schema-bound", `CREATE TABLE dbo.t (id int, name varchar(20));
GO
CREATE FUNCTION dbo.f() RETURNS TABLE AS RETURN SELECT name FROM dbo.t;`, false, ""},
		{"plain view with a literal (not schema-bound)", `CREATE TABLE dbo.t (id int);
GO
CREATE VIEW dbo.v AS SELECT id, 'x' AS k FROM dbo.t;`, false, ""},
		{"index on a char column", `CREATE TABLE dbo.t (id int, name varchar(20));
CREATE INDEX ix ON dbo.t (name);`, false, ""},
		{"default with a literal", `CREATE TABLE dbo.t (id int, name varchar(20) DEFAULT 'x');`, false, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := fmt.Sprintf("cs_h%d", i)
			collDB(t, db, ciOld, tc.script)
			blockers, err := s.SQL.CollationBlockers(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]bool{}
			for _, b := range blockers {
				kinds[b.Kind] = true
			}
			_, runErr := s.RunCollationStep(ctx, CollationStep{ID: CollSetDatabase, Database: db, Collation: csNew, From: ciOld})
			t.Logf("SQL Server refuses: %v (%v); blockers: %v", runErr != nil, trimErr(runErr), blockers)
			if got := runErr != nil; got != tc.blocks {
				t.Errorf("SQL Server refuses = %v, the test expected %v", got, tc.blocks)
			}
			if tc.blocks != (len(blockers) > 0) {
				t.Errorf("blockers %+v do not match what SQL Server does (refuses %v)", blockers, tc.blocks)
			}
			if tc.kind != "" && !kinds[tc.kind] {
				t.Errorf("no %s blocker in %+v", tc.kind, blockers)
			}
			if st := stateOf(t, db); st.UserAccess != "MULTI_USER" {
				t.Errorf("%s left %s", db, st.UserAccess)
			}
		})
	}
}

func trimErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
