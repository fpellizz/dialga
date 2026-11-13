//go:build integration

package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// collCFixture is the database of the collation design §10 C: a parent with a
// clustered primary key (PAGE compression, extended property), a child with
// a foreign key ON DELETE CASCADE NOT FOR REPLICATION, a filtered unique
// index with INCLUDE on another filegroup, an untrusted CHECK, a DEFAULT,
// statistics with a filter; an audit table with a disabled foreign key; an
// unrelated table (its own unit).
const collCFixture = `ALTER DATABASE coll_c ADD FILEGROUP fg_ix;
GO
ALTER DATABASE coll_c ADD FILE (NAME = N'coll_c_ix', FILENAME = N'/var/opt/mssql/data/coll_c_ix.ndf', SIZE = 8MB) TO FILEGROUP fg_ix;
GO
USE coll_c;
GO
CREATE SCHEMA [o'sales];
GO
CREATE TABLE dbo.parent (
  code varchar(20) NOT NULL,
  name nvarchar(100) NULL,
  label varchar(50) NOT NULL CONSTRAINT DF_parent_label DEFAULT ('new'),
  n int NOT NULL CONSTRAINT CK_parent_n CHECK (n >= 0),
  CONSTRAINT PK_parent PRIMARY KEY CLUSTERED (code) WITH (DATA_COMPRESSION = PAGE, FILLFACTOR = 90)
);
CREATE NONCLUSTERED INDEX IX_parent_n ON dbo.parent (n DESC) WITH (ALLOW_PAGE_LOCKS = OFF);
CREATE TABLE dbo.child (
  id int IDENTITY NOT NULL CONSTRAINT PK_child PRIMARY KEY CLUSTERED,
  parent_code varchar(20) NULL,
  ref varchar(30) NULL,
  note nvarchar(200) NULL,
  CONSTRAINT FK_child_parent FOREIGN KEY (parent_code) REFERENCES dbo.parent (code) ON DELETE CASCADE NOT FOR REPLICATION
);
CREATE UNIQUE NONCLUSTERED INDEX UX_child_ref ON dbo.child (ref ASC, id DESC) INCLUDE (note) WHERE ref IS NOT NULL
  WITH (FILLFACTOR = 80, PAD_INDEX = ON) ON fg_ix;
ALTER TABLE dbo.child WITH NOCHECK ADD CONSTRAINT CK_child_ref CHECK (ref <> '');
CREATE STATISTICS st_child_note ON dbo.child (note, ref) WHERE note IS NOT NULL WITH NORECOMPUTE;
CREATE TABLE [o'sales].[audit log] (
  id int NOT NULL PRIMARY KEY,
  [parent]]code] varchar(20) NULL CONSTRAINT [FK audit] REFERENCES dbo.parent (code)
);
ALTER TABLE [o'sales].[audit log] NOCHECK CONSTRAINT [FK audit];
CREATE TABLE dbo.lonely (city varchar(40) NULL, n int NULL);
GO
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'the parent''s key', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'parent', @level2type = N'CONSTRAINT', @level2name = N'PK_parent';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'one ref per child', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'child', @level2type = N'INDEX', @level2name = N'UX_child_ref';
EXEC sys.sp_addextendedproperty @name = N'owner', @value = N'sales', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'child', @level2type = N'CONSTRAINT', @level2name = N'FK_child_parent';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'not empty', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'child', @level2type = N'CONSTRAINT', @level2name = N'CK_child_ref';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'starts new', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'parent', @level2type = N'CONSTRAINT', @level2name = N'DF_parent_label';
GO
INSERT dbo.parent (code, name, n) VALUES ('a1', N'Ann', 1), ('b2', N'Bob', 2);
INSERT dbo.child (parent_code, ref, note) VALUES ('a1', 'r1', N'x'), ('b2', NULL, NULL), (NULL, 'r3', N'y');
INSERT [o'sales].[audit log] VALUES (1, 'zz');
INSERT dbo.lonely VALUES ('Roma', 1);
GO
USE master;`

const collTarget = "Latin1_General_100_CS_AS"

// newCollDB creates db (owned by admin, like a database restored by the
// master user) with collation, then runs script in it.
func newCollDB(t *testing.T, db, collation, script string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{`DROP DATABASE IF EXISTS ` + db, `CREATE DATABASE ` + db + ` COLLATE ` + collation} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = sa.Exec(`IF DB_ID(N'` + db + `') IS NOT NULL BEGIN ALTER DATABASE ` + db + ` SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ` + db + ` END`)
	})
	if err := runScript(ctx, admin, script); err != nil {
		t.Fatal(err)
	}
}

// catalogShape is the catalog of a database by names, without object ids
// and collations: what a conversion must keep identical.
func catalogShape(t *testing.T, db string) (shape string, collations map[string]string) {
	t.Helper()
	cat, err := mssql.Session{DB: admin}.ConversionCatalog(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	table := func(id int64) string { return cat.Tables[id].Schema + "." + cat.Tables[id].Name }
	type shapeOf struct {
		Columns, Indexes, FKs, Checks, Defaults, Stats, Props []string
		Tables                                                []string
	}
	var s shapeOf
	enc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	for id, tb := range cat.Tables {
		tb.Rows, tb.ReservedMB = 0, 0
		s.Tables = append(s.Tables, table(id)+enc(tb))
	}
	collations = map[string]string{}
	for _, c := range cat.Columns {
		collations[c.Schema+"."+c.Table+"."+c.Column] = c.Collation
		c.Collation, c.ObjectID = "", 0
		s.Columns = append(s.Columns, enc(c))
	}
	names := map[[2]int64]string{} // class 1: constraint id; class 7: table, index id
	for _, ix := range cat.Indexes {
		ix.ObjectID, ix.ConstraintID = 0, 0
		s.Indexes = append(s.Indexes, enc(ix))
	}
	for _, ix := range cat.Indexes {
		names[[2]int64{7, ix.ObjectID<<16 | int64(ix.IndexID)}] = table(ix.ObjectID) + " index " + ix.Def.Name
		if ix.ConstraintID != 0 {
			names[[2]int64{1, ix.ConstraintID}] = table(ix.ObjectID) + " " + ix.Def.Name
		}
	}
	for _, fk := range cat.ForeignKeys {
		names[[2]int64{1, fk.ObjectID}] = table(fk.ParentID) + " " + fk.Def.Name
		fk.ObjectID, fk.ParentID, fk.RefID = 0, 0, 0
		s.FKs = append(s.FKs, enc(fk))
	}
	for _, c := range cat.Checks {
		names[[2]int64{1, c.ObjectID}] = table(c.ParentID) + " " + c.Def.Name
		c.ObjectID, c.ParentID = 0, 0
		s.Checks = append(s.Checks, enc(c))
	}
	for _, d := range cat.Defaults {
		names[[2]int64{1, d.ObjectID}] = table(d.ParentID) + " " + d.Def.Name
		d.ObjectID, d.ParentID = 0, 0
		s.Defaults = append(s.Defaults, enc(d))
	}
	for _, st := range cat.Stats {
		if st.AutoCreated {
			continue // dropped, SQL Server creates them again when it needs them
		}
		st.ObjectID, st.StatsID = 0, 0
		s.Stats = append(s.Stats, enc(st))
	}
	for _, e := range cat.ExtendedProperties {
		k := [2]int64{1, e.MajorID}
		if e.Class == 7 {
			k = [2]int64{7, e.MajorID<<16 | int64(e.MinorID)}
		}
		s.Props = append(s.Props, names[k]+" "+e.Name+"="+e.Value+" ("+e.BaseType+")")
	}
	for _, l := range [][]string{s.Tables, s.Columns, s.Indexes, s.FKs, s.Checks, s.Defaults, s.Stats, s.Props} {
		slices.Sort(l)
	}
	out, _ := json.MarshalIndent(s, "", " ")
	return string(out), collations
}

func mustReady(t *testing.T, p *Prepared, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready() {
		t.Fatalf("checks %+v", p.Checks)
	}
}

func convSession() *Session {
	s := goldenSession(nil)
	return s
}

func TestIntegrationConvertColumns(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_c", "SQL_Latin1_General_CP1_CI_AS", collCFixture)
	before, collBefore := catalogShape(t, "coll_c")
	s := convSession()
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_c", Collation: collTarget})
	mustReady(t, p, err)
	// the parent, its children and the audit table are one unit; lonely is another
	if p.Collation == nil {
		t.Fatalf("checks %+v", p.Checks)
	}
	if ids(p) != "convert,convert" || p.Collation.Units != 2 || p.Collation.Tables != 4 || p.Plan.Confirmation() != "coll_c" {
		t.Fatalf("actions %s summary %+v\n%s", ids(p), p.Collation, statements(p))
	}
	for _, step := range []string{"uniqueness:ok", "references:ok", "code-page:ok"} {
		if !strings.Contains(verdicts(p), step) {
			t.Fatalf("checks %s", verdicts(p))
		}
	}
	t.Logf("plan:\n%s", statements(p))
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "coll_c"}); err != nil {
		t.Fatalf("execute: %v\n%s", err, statements(p))
	}
	after, collAfter := catalogShape(t, "coll_c")
	if before != after {
		t.Fatalf("the catalog changed beyond the collation\nbefore: %s\nafter: %s", before, after)
	}
	for col, c := range collAfter {
		if collBefore[col] != "SQL_Latin1_General_CP1_CI_AS" || c != collTarget {
			t.Errorf("%s: %s → %s", col, collBefore[col], c)
		}
	}
	// the data is there and the constraints work as before
	var n int
	if err := admin.QueryRow(`SELECT COUNT(*) FROM coll_c.dbo.child c JOIN coll_c.dbo.parent p ON p.code = c.parent_code`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("join: %d %v", n, err)
	}
	if _, err := admin.Exec(`INSERT coll_c.dbo.child (parent_code) VALUES ('nope')`); err == nil {
		t.Fatal("the foreign key does not check any more")
	}
	if _, err := admin.Exec(`DELETE coll_c.dbo.parent WHERE code = 'a1'`); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	// case sensitive now: 'A1' and 'a1' are two keys
	if _, err := admin.Exec(`INSERT coll_c.dbo.parent (code, n) VALUES ('B2', 3)`); err != nil {
		t.Fatalf("CS key: %v", err)
	}
	// a second run has nothing left to do, and each step is a no-op
	again, err := s.ConvertColumns(ctx, Params{Database: "coll_c", Collation: collTarget})
	if err != nil || len(again.Plan.Actions) != 0 || !strings.Contains(verdicts(again), "columns:warning") {
		t.Fatalf("again: %v %s", err, verdicts(again))
	}
	for _, st := range p.CollationSteps() {
		if out, err := s.RunCollationStep(ctx, st); err != nil || !strings.HasPrefix(out, "already converted") {
			t.Fatalf("rerun: %q %v", out, err)
		}
	}
}

func TestIntegrationConvertColumnsDataChecks(t *testing.T) {
	ctx := context.Background()
	s := convSession()
	t.Run("uniqueness", func(t *testing.T) {
		newCollDB(t, "coll_u", collTarget, `USE coll_u;
GO
CREATE TABLE dbo.t (code varchar(10) NOT NULL CONSTRAINT PK_t PRIMARY KEY);
INSERT dbo.t VALUES ('a'), ('A');
GO
USE master;`)
		p, err := s.ConvertColumns(ctx, Params{Database: "coll_u", Collation: "SQL_Latin1_General_CP1_CI_AS"})
		if err != nil || p.Ready() || !strings.Contains(verdicts(p), "uniqueness:failed") {
			t.Fatalf("err %v plan %+v", err, p)
		}
	})
	t.Run("code-page", func(t *testing.T) {
		newCollDB(t, "coll_p", "Latin1_General_CI_AS", `USE coll_p;
GO
CREATE TABLE dbo.accented (city varchar(40) NULL);
CREATE TABLE dbo.plain (city varchar(40) NULL);
INSERT dbo.accented VALUES (N'Café'), ('Roma');
INSERT dbo.plain VALUES ('Roma');
GO
USE master;`)
		p, err := s.ConvertColumns(ctx, Params{Database: "coll_p", Collation: "Cyrillic_General_CI_AS"})
		if err != nil || p.Ready() {
			t.Fatalf("err %v plan %+v", err, p)
		}
		for _, c := range p.Checks {
			if c.Step == "code-page" && (!strings.Contains(c.Detail, "dbo.accented.city") || strings.Contains(c.Detail, "plain")) {
				t.Fatalf("code-page check %+v", c)
			}
		}
		// What the check relies on: converting through the collation of the
		// target uses its code page. 'é' is not in 1251: Windows maps it to
		// its best fit 'e' (not '?'), a loss all the same.
		var got string
		if err := admin.QueryRow(`SELECT CAST(CAST(CAST(N'Café' AS nvarchar(max)) COLLATE Cyrillic_General_CI_AS AS varchar(max)) AS nvarchar(max))`).Scan(&got); err != nil || got == "Café" {
			t.Fatalf("conversion through Cyrillic_General_CI_AS: %q %v", got, err)
		}
		t.Logf("'Café' through Cyrillic_General_CI_AS: %q", got)
		// and the plain table alone converts
		if p, err := s.ConvertColumns(ctx, Params{Database: "coll_p", Collation: "Cyrillic_General_CI_AS",
			Tables: []mssql.TableRef{{Schema: "dbo", Name: "plain"}}}); err != nil || !p.Ready() {
			t.Fatalf("plain: %v %s", err, verdicts(p))
		}
	})
	t.Run("references", func(t *testing.T) {
		newCollDB(t, "coll_o", "SQL_Latin1_General_CP1_CI_AS", `USE coll_o;
GO
CREATE TABLE dbo.parent (code varchar(10) NOT NULL CONSTRAINT PK_parent PRIMARY KEY);
CREATE TABLE dbo.child (code varchar(10) NULL CONSTRAINT FK_child REFERENCES dbo.parent (code));
INSERT dbo.parent VALUES ('abc');
INSERT dbo.child VALUES ('ABC');
GO
USE master;`)
		p, err := s.ConvertColumns(ctx, Params{Database: "coll_o", Collation: collTarget})
		if err != nil || p.Ready() || !strings.Contains(verdicts(p), "references:failed") {
			t.Fatalf("err %v plan %+v", err, p)
		}
	})
}

func TestIntegrationConvertColumnsRefusals(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_r", "SQL_Latin1_General_CP1_CI_AS", `USE coll_r;
GO
CREATE TABLE dbo.temporal (id int NOT NULL PRIMARY KEY, name varchar(20) NULL,
  vf datetime2 GENERATED ALWAYS AS ROW START NOT NULL, vt datetime2 GENERATED ALWAYS AS ROW END NOT NULL,
  PERIOD FOR SYSTEM_TIME (vf, vt)) WITH (SYSTEM_VERSIONING = ON (HISTORY_TABLE = dbo.temporal_history));
CREATE TABLE dbo.computed (name varchar(20) NULL, upper_name AS UPPER(name));
CREATE TABLE dbo.bound (name varchar(20) NULL, n int NULL);
CREATE TABLE dbo.bound_int (name varchar(20) NULL, n int NULL);
CREATE TABLE dbo.texts (name varchar(20) NULL, body text NULL);
GO
CREATE VIEW dbo.v_bound WITH SCHEMABINDING AS SELECT name FROM dbo.bound;
GO
CREATE VIEW dbo.v_bound_int WITH SCHEMABINDING AS SELECT n FROM dbo.bound_int;
GO
USE master;`)
	s := convSession()
	for table, reason := range map[string]string{
		"temporal":         "system-versioned",
		"temporal_history": "system-versioned",
		"computed":         "computed column upper_name reads a converted column",
		"bound":            "referenced by the schema-bound view dbo.v_bound",
		"texts":            "column body: text/ntext",
	} {
		p, err := s.ConvertColumns(ctx, Params{Database: "coll_r", Collation: collTarget, Tables: []mssql.TableRef{{Schema: "dbo", Name: table}}})
		if err != nil || p.Ready() || len(p.Collation.Refused) != 1 || !strings.Contains(p.Collation.Refused[0].Reason, reason) {
			t.Errorf("%s: err %v refused %+v checks %s", table, err, p.Collation, verdicts(p))
		}
	}
	// A schema-bound view on another column of the table does not block
	// ALTER COLUMN, and the catalog lists it by column: the table converts.
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_r", Collation: collTarget, Tables: []mssql.TableRef{{Schema: "dbo", Name: "bound_int"}}})
	if err != nil || !p.Ready() {
		t.Fatalf("bound_int: %v %+v", err, p.Checks)
	}
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "coll_r"}); err != nil {
		t.Fatalf("bound_int: %v", err)
	}
	// the whole database: the refused tables are left alone, the others convert
	p, err = s.ConvertColumns(ctx, Params{Database: "coll_r", Collation: collTarget})
	if err != nil || !p.Ready() || !strings.Contains(verdicts(p), "refused:warning") || len(p.Collation.Refused) != 5 {
		t.Fatalf("whole database: %v %s %+v", err, verdicts(p), p.Collation)
	}
}

// A unit is atomic: data that changed after the preview makes it fail and
// roll back, leaving the tables, the keys and the foreign keys as they were.
func TestIntegrationConvertColumnsAtomic(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_a", collTarget, `USE coll_a;
GO
CREATE TABLE dbo.parent (code varchar(10) NOT NULL CONSTRAINT PK_parent PRIMARY KEY);
CREATE TABLE dbo.child (code varchar(10) NULL CONSTRAINT FK_child REFERENCES dbo.parent (code));
INSERT dbo.parent VALUES ('a');
INSERT dbo.child VALUES ('a');
GO
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'key', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'parent', @level2type = N'CONSTRAINT', @level2name = N'PK_parent';
GO
USE master;`)
	before, _ := catalogShape(t, "coll_a")
	s := convSession()
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_a", Collation: "SQL_Latin1_General_CP1_CI_AS"})
	mustReady(t, p, err)
	// after the preview: a key that collides once case is ignored
	if _, err := admin.Exec(`INSERT coll_a.dbo.parent VALUES ('A')`); err != nil {
		t.Fatal(err)
	}
	_, err = p.Execute(ctx, ExecOptions{Confirm: "coll_a"})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("execute: %v", err)
	}
	if after, coll := catalogShape(t, "coll_a"); after != before || coll["dbo.parent.code"] != collTarget || coll["dbo.child.code"] != collTarget {
		t.Fatalf("not rolled back: %v\nbefore %s\nafter %s", coll, before, after)
	}
	var n int
	if err := admin.QueryRow(`SELECT COUNT(*) FROM coll_a.dbo.parent`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows %d %v", n, err)
	}
}

// A unit waits LOCK_TIMEOUT for its tables, then fails with nothing changed.
func TestIntegrationConvertColumnsLockTimeout(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_l", "SQL_Latin1_General_CP1_CI_AS", `USE coll_l;
GO
CREATE TABLE dbo.busy (name varchar(20) NULL);
INSERT dbo.busy VALUES ('x');
GO
USE master;`)
	s := convSession()
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_l", Collation: collTarget})
	mustReady(t, p, err)
	conn, err := sa.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT COUNT(*) FROM coll_l.dbo.busy WITH (TABLOCKX, HOLDLOCK)`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Execute(ctx, ExecOptions{Confirm: "coll_l"})
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "blocked by other sessions for 30 s: nothing changed") {
		t.Fatalf("execute: %v", err)
	}
	if d := time.Since(start); d < 25*time.Second || d > 90*time.Second {
		t.Errorf("waited %s", d)
	}
	if _, coll := catalogShape(t, "coll_l"); coll["dbo.busy.name"] != "SQL_Latin1_General_CP1_CI_AS" {
		t.Fatalf("changed: %v", coll)
	}
	var open int
	if err := admin.QueryRow(`SELECT @@TRANCOUNT`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("transaction left open: %d %v", open, err)
	}
}

// A filter that names a converted column blocks ALTER COLUMN: the filtered
// index and statistic are dropped and created again, identical.
func TestIntegrationConvertColumnsFilteredIndex(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_f", "SQL_Latin1_General_CP1_CI_AS", `USE coll_f;
GO
CREATE TABLE dbo.t (id int NOT NULL CONSTRAINT PK_t PRIMARY KEY, [status] varchar(10) NULL);
CREATE INDEX IX ON dbo.t (id) WHERE [status] = 'A';
CREATE STATISTICS st_t ON dbo.t (id) WHERE [status] = 'B';
INSERT dbo.t VALUES (1, 'A'), (2, 'B');
GO
USE master;`)
	before, _ := catalogShape(t, "coll_f")
	s := convSession()
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_f", Collation: collTarget})
	mustReady(t, p, err)
	if p.Collation.Indexes != 1 || !strings.Contains(statements(p), "DROP INDEX [IX]") || !strings.Contains(statements(p), "DROP STATISTICS [dbo].[t].[st_t]") {
		t.Fatalf("summary %+v\n%s", p.Collation, statements(p))
	}
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "coll_f"}); err != nil {
		t.Fatalf("execute: %v\n%s", err, statements(p))
	}
	after, coll := catalogShape(t, "coll_f")
	if before != after || coll["dbo.t.status"] != collTarget {
		t.Fatalf("collation %v\nbefore %s\nafter %s", coll, before, after)
	}
}

// Change tracking needs the primary key of its table: a conversion that
// rebuilds it is refused; one that keeps it converts.
func TestIntegrationConvertColumnsChangeTracking(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_ct", "SQL_Latin1_General_CP1_CI_AS", `ALTER DATABASE coll_ct SET CHANGE_TRACKING = ON;
GO
USE coll_ct;
GO
CREATE TABLE dbo.by_code (code varchar(10) NOT NULL CONSTRAINT PK_by_code PRIMARY KEY, name varchar(20) NULL);
CREATE TABLE dbo.by_id (id int NOT NULL CONSTRAINT PK_by_id PRIMARY KEY, name varchar(20) NULL);
ALTER TABLE dbo.by_code ENABLE CHANGE_TRACKING;
ALTER TABLE dbo.by_id ENABLE CHANGE_TRACKING;
GO
USE master;`)
	s := convSession()
	p, err := s.ConvertColumns(ctx, Params{Database: "coll_ct", Collation: collTarget, Tables: []mssql.TableRef{{Schema: "dbo", Name: "by_code"}}})
	if err != nil || p.Ready() || len(p.Collation.Refused) != 1 || !strings.Contains(p.Collation.Refused[0].Reason, "change tracking needs the primary key PK_by_code") {
		t.Fatalf("by_code: %v %+v %s", err, p.Collation, verdicts(p))
	}
	p, err = s.ConvertColumns(ctx, Params{Database: "coll_ct", Collation: collTarget, Tables: []mssql.TableRef{{Schema: "dbo", Name: "by_id"}}})
	mustReady(t, p, err)
	if _, err := p.Execute(ctx, ExecOptions{Confirm: "coll_ct"}); err != nil {
		t.Fatalf("by_id: %v", err)
	}
	if _, coll := catalogShape(t, "coll_ct"); coll["dbo.by_id.name"] != collTarget || coll["dbo.by_code.code"] == collTarget {
		t.Fatalf("collations %v", coll)
	}
}

// sp_addextendedproperty fails with RAISERROR, which XACT_ABORT does not
// abort on: the TRY ... CATCH of the unit rolls everything back all the same.
func TestIntegrationConvertColumnsPropertyFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	newCollDB(t, "coll_x", "SQL_Latin1_General_CP1_CI_AS", `USE coll_x;
GO
CREATE TABLE dbo.t (code varchar(10) NOT NULL CONSTRAINT CK_t CHECK (code <> ''));
GO
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'not empty', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't', @level2type = N'CONSTRAINT', @level2name = N'CK_t';
GO
USE master;`)
	before, _ := catalogShape(t, "coll_x")
	p, err := convSession().ConvertColumns(ctx, Params{Database: "coll_x", Collation: collTarget})
	mustReady(t, p, err)
	u := *p.CollationSteps()[0].Unit
	tc := &u.Tables[0]
	if len(tc.ExtendedProperties) != 1 {
		t.Fatalf("unit %+v", u)
	}
	// the same property twice: the second sp_addextendedproperty fails
	tc.ExtendedProperties = append(tc.ExtendedProperties, tc.ExtendedProperties[0])
	err = mssql.Session{DB: admin}.Exec(ctx, mssql.ConvertUnit(u))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("exec: %v", err)
	}
	if after, coll := catalogShape(t, "coll_x"); after != before || coll["dbo.t.code"] != "SQL_Latin1_General_CP1_CI_AS" {
		t.Fatalf("not rolled back: %v\nbefore %s\nafter %s", coll, before, after)
	}
}
