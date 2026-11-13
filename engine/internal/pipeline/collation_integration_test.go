//go:build integration

package pipeline

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// stopAfterExec is the SQL of the master user, except that the first
// statement it runs (the first unit, committed) is followed by the engine
// stopping: the step never reports it done.
type stopAfterExec struct {
	mssql.Session
	stop      *atomic.Bool
	committed chan struct{}
}

func (s stopAfterExec) Exec(ctx context.Context, stmt string) error {
	if err := s.Session.Exec(ctx, stmt); err != nil {
		return err
	}
	if s.stop.CompareAndSwap(true, false) {
		close(s.committed)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// A convert-columns of two units, interrupted after the first one
// committed: the resume finds the first already converted (no-op) and runs
// the second. If the second table changed meanwhile (its column is longer
// now), the resume refuses it and changes nothing: running the journaled
// unit would put the old type back.
func TestIntegrationResumeConvertColumns(t *testing.T) {
	in := startSQLServer(t)
	ctx := context.Background()
	const to = "Latin1_General_100_CS_AS"
	collation := func(t *testing.T, db, table, col string) (coll string, length int) {
		t.Helper()
		if err := in.sa.QueryRow(`SELECT collation_name, max_length FROM `+db+`.sys.columns WHERE object_id = OBJECT_ID(N'`+db+`.dbo.`+table+`') AND name = @c`,
			sql.Named("c", col)).Scan(&coll, &length); err != nil {
			t.Fatal(err)
		}
		return coll, length
	}
	// interrupt plans db, runs the job until the first unit committed and
	// the engine stopped, then runs between and resumes it.
	interrupt := func(t *testing.T, db string, between func()) jobs.Job {
		t.Helper()
		if _, err := in.admin.ExecContext(ctx, `CREATE DATABASE `+db+` COLLATE SQL_Latin1_General_CP1_CI_AS`); err != nil {
			t.Fatal(err)
		}
		if err := runScript(ctx, in.admin, `USE `+db+`;
GO
CREATE TABLE dbo.a (code varchar(10) NOT NULL CONSTRAINT PK_a PRIMARY KEY, name nvarchar(50) NULL);
CREATE TABLE dbo.b (name varchar(40) NULL);
INSERT dbo.a VALUES ('x', N'one');
INSERT dbo.b VALUES ('two');
GO
USE master;`); err != nil {
			t.Fatal(err)
		}
		stop := &atomic.Bool{}
		committed := make(chan struct{})
		open := func(context.Context) (*ops.Session, func(), error) {
			return &ops.Session{Name: "it", Target: testTarget, SQL: stopAfterExec{Session: mssql.Session{DB: in.admin}, stop: stop, committed: committed}},
				func() {}, nil
		}
		env := Env{Name: "it", Target: testTarget, TaskInterval: 300 * time.Millisecond, OpenOps: open}
		sess, _, _ := open(ctx)
		prep, err := sess.Build(ctx, ops.OpConvertColumns, ops.Params{Database: db, Collation: to})
		if err != nil || !prep.Ready() || len(prep.CollationSteps()) != 2 {
			t.Fatalf("plan: %v %+v", err, prep)
		}
		spec, err := OperationSpec(env, prep, false)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		m, err := jobs.Open(dir, jobs.Options{})
		if err != nil {
			t.Fatal(err)
		}
		stop.Store(true)
		job, err := m.Start(spec)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-committed:
		case <-time.After(time.Minute):
			t.Fatal("the first unit never ran")
		}
		m = restartOn(t, dir, m)
		stopped, err := m.Get(job.ID)
		aCode, _ := collation(t, db, "a", "code")
		bName, _ := collation(t, db, "b", "name")
		if err != nil || stopped.State != jobs.Interrupted || aCode != to || bName == to {
			t.Fatalf("after the stop: %s %v, a.code %s, b.name %s", stopped.State, err, aCode, bName)
		}
		if between != nil {
			between()
		}
		return resumeAndWait(t, m, job.ID, env)
	}

	t.Run("resumes", func(t *testing.T) {
		done := interrupt(t, "coll_res", nil)
		steps := done.Items[0].Steps
		if done.State != jobs.Succeeded || len(steps) != 2 || steps[0].ID != "convert" || steps[1].ID != "convert#2" ||
			!strings.HasPrefix(steps[0].Detail, "already converted") || !strings.Contains(steps[1].Detail, "converted to "+to) {
			t.Fatalf("resumed job %s: %+v", done.State, steps)
		}
		if c, _ := collation(t, "coll_res", "a", "name"); c != to {
			t.Fatal("a.name not converted")
		}
		if c, _ := collation(t, "coll_res", "b", "name"); c != to {
			t.Fatal("b.name not converted")
		}
	})

	t.Run("the table changed meanwhile", func(t *testing.T) {
		done := interrupt(t, "coll_chg", func() {
			if _, err := in.admin.ExecContext(ctx, `ALTER TABLE coll_chg.dbo.b ALTER COLUMN name varchar(80) COLLATE SQL_Latin1_General_CP1_CI_AS NULL`); err != nil {
				t.Fatal(err)
			}
		})
		steps := done.Items[0].Steps
		if done.State != jobs.Failed || len(steps) != 2 || !strings.HasPrefix(steps[0].Detail, "already converted") ||
			!strings.Contains(done.Items[0].Error, "the tables changed since the plan: nothing done, preview again") {
			t.Fatalf("resumed job %s: %+v %q", done.State, steps, done.Items[0].Error)
		}
		if c, n := collation(t, "coll_chg", "b", "name"); c != "SQL_Latin1_General_CP1_CI_AS" || n != 80 {
			t.Fatalf("b.name is %s, %d bytes: changed", c, n)
		}
	})
}
