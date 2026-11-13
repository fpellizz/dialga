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
// the second.
func TestIntegrationResumeConvertColumns(t *testing.T) {
	in := startSQLServer(t)
	ctx := context.Background()
	if err := runScript(ctx, in.sa, `GRANT VIEW SERVER STATE TO admin;`); err != nil {
		t.Fatal(err)
	}
	if _, err := in.admin.ExecContext(ctx, `CREATE DATABASE coll_res COLLATE SQL_Latin1_General_CP1_CI_AS`); err != nil {
		t.Fatal(err)
	}
	if err := runScript(ctx, in.admin, `USE coll_res;
GO
CREATE TABLE dbo.a (code varchar(10) NOT NULL CONSTRAINT PK_a PRIMARY KEY, name nvarchar(50) NULL);
CREATE TABLE dbo.b (name varchar(40) NULL);
INSERT dbo.a VALUES ('x', N'one');
INSERT dbo.b VALUES ('two');
GO
USE master;`); err != nil {
		t.Fatal(err)
	}
	const to = "Latin1_General_100_CS_AS"
	stop := &atomic.Bool{}
	committed := make(chan struct{})
	open := func(context.Context) (*ops.Session, func(), error) {
		return &ops.Session{Name: "it", Target: testTarget, SQL: stopAfterExec{Session: mssql.Session{DB: in.admin}, stop: stop, committed: committed}},
			func() {}, nil
	}
	env := Env{Name: "it", Target: testTarget, TaskInterval: 300 * time.Millisecond, OpenOps: open}
	sess, _, _ := open(ctx)
	prep, err := sess.Build(ctx, ops.OpConvertColumns, ops.Params{Database: "coll_res", Collation: to})
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
	collation := func(table, col string) string {
		t.Helper()
		var c string
		if err := in.sa.QueryRow(`SELECT collation_name FROM coll_res.sys.columns WHERE object_id = OBJECT_ID(N'coll_res.dbo.`+table+`') AND name = @c`,
			sql.Named("c", col)).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	stopped, err := m.Get(job.ID)
	if err != nil || stopped.State != jobs.Interrupted || collation("a", "code") != to || collation("b", "name") == to {
		t.Fatalf("after the stop: %s %v, a.code %s, b.name %s", stopped.State, err, collation("a", "code"), collation("b", "name"))
	}
	done := resumeAndWait(t, m, job.ID, env)
	steps := done.Items[0].Steps
	if done.State != jobs.Succeeded || len(steps) != 2 || steps[0].ID != "convert" || steps[1].ID != "convert#2" ||
		!strings.HasPrefix(steps[0].Detail, "already converted") || !strings.Contains(steps[1].Detail, "converted to "+to) {
		t.Fatalf("resumed job %s: %+v", done.State, steps)
	}
	if collation("a", "name") != to || collation("b", "name") != to {
		t.Fatal("not converted")
	}
}
