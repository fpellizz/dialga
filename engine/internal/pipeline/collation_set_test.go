package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// collRDS is fakeRDS with one database whose collation the batch changes.
type collRDS struct {
	*fakeRDS
	cmu       sync.Mutex
	collation string
}

func (c *collRDS) CollationState(context.Context, string) (mssql.CollationState, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: c.collation,
		State: "ONLINE", Containment: "NONE"}, UserAccess: "MULTI_USER", CanAlter: true, Exists: true}, nil
}

func (c *collRDS) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{Name: name}, true, nil
}

func (c *collRDS) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{Server: "SQL_Latin1_General_CP1_CI_AS"}, nil
}

func (c *collRDS) CollationBlockers(context.Context, string) ([]mssql.CollationBlocker, error) {
	return nil, nil
}

func (c *collRDS) NameClashes(context.Context, string, string) ([]string, error) { return nil, nil }

func (c *collRDS) CharColumns(context.Context, string) ([]mssql.CharColumn, error) { return nil, nil }

func (c *collRDS) Exec(ctx context.Context, stmt string) error {
	if strings.Contains(stmt, "COLLATE") {
		c.cmu.Lock()
		c.collation = "Latin1_General_100_CI_AS"
		c.cmu.Unlock()
	}
	return c.fakeRDS.Exec(ctx, stmt)
}

func TestOperationSpecSetCollation(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	c := &collRDS{fakeRDS: rds, collation: "SQL_Latin1_General_CP1_CI_AS"}
	env.OpenOps = func(context.Context) (*ops.Session, func(), error) {
		return &ops.Session{Name: "dev-sql", Target: testTarget, SQL: c}, func() {}, nil
	}
	prep := prepare(t, env, ops.OpSetCollation, ops.Params{Database: "sales", Collation: "Latin1_General_100_CI_AS"})
	spec, err := OperationSpec(env, prep, false)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Operation != ops.OpSetCollation || spec.Items[0].Name != "sales" || stepList(spec) != "set-collation:collation" {
		t.Fatalf("spec %+v: %s", spec, stepList(spec))
	}
	m := jobs.New(jobs.Options{})
	job, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	end, err := m.Wait(ctx, job.ID)
	if err != nil || end.State != jobs.Succeeded || end.Items[0].Steps[0].Detail != "sales is now Latin1_General_100_CI_AS" {
		t.Fatalf("job %v %v: %+v", end.State, err, end.Items)
	}
	if len(rds.execs) != 1 || rds.execs[0] != mssql.SetDatabaseCollation("sales", "Latin1_General_100_CI_AS") {
		t.Fatalf("ran %v", rds.execs)
	}
	// resumed after it ran (interrupted before the journal knew): the step is a no-op
	step, err := rebuildCollation(rebuildOf(t, env, jobs.StepInfo{ID: "set-collation", Title: "t"}, collationParams(t, prep.CollationSteps()[0])))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := step.Run(ctx, nil); err != nil || len(rds.execs) != 1 {
		t.Fatalf("second run: %v, execs %d", err, len(rds.execs))
	}
}
