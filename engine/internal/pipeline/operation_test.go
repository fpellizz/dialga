package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// prepare runs the pre-flight of op on the fake instance.
func prepare(t *testing.T, env Env, op string, p ops.Params) *ops.Prepared {
	t.Helper()
	sess, closeOps, err := env.OpenOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeOps()
	prep, err := sess.Build(context.Background(), op, p)
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Ready() {
		t.Fatalf("not ready: %+v", prep.Checks)
	}
	return prep
}

func stepList(spec jobs.Spec) string {
	var s []string
	for _, st := range spec.Items[0].Steps {
		kind := ""
		if rs, ok := st.(jobs.Resumable); ok {
			kind = strings.TrimPrefix(rs.Kind(), "pipeline.")
		}
		s = append(s, st.ID()+":"+kind)
	}
	return strings.Join(s, ",")
}

func TestOperationSpecSteps(t *testing.T) {
	tests := []struct {
		op     string
		p      ops.Params
		noWait bool
		item   string
		steps  string
	}{
		{ops.OpRestore, ops.Params{Database: "sales", Key: "sales.bak"}, false, "sales", "restore:task,wait:wait"},
		{ops.OpRestore, ops.Params{Database: "sales", Key: "sales.bak"}, true, "sales", "restore:task"},
		{ops.OpExport, ops.Params{Database: "old"}, false, "old", "export:task,wait:wait"},
		{ops.OpFinish, ops.Params{Database: "hr"}, false, "hr", "finish:sql"},
		{ops.OpCancel, ops.Params{TaskID: 7}, false, "task 7", "cancel:sql"},
		{ops.OpDrop, ops.Params{Database: "old"}, false, "old", "drop:sql"},
	}
	for _, tc := range tests {
		t.Run(tc.op+"/"+tc.item, func(t *testing.T) {
			// A fresh instance per case: the job started below changes it (the
			// restore brings sales ONLINE), and the next case must not see that.
			env, rds, s3 := testEnv(t, testTarget)
			s3.Put(testBucket, "import/sales.bak", []byte("x"), "")
			rds.states["hr"] = "RESTORING"
			rds.states["old"] = "ONLINE"
			rds.tasks[7] = &mssql.Task{ID: 7, Type: "RESTORE_DB", Database: "old", Lifecycle: mssql.InProgress}
			prep := prepare(t, env, tc.op, tc.p)
			spec, err := OperationSpec(env, prep, tc.noWait)
			if err != nil {
				t.Fatal(err)
			}
			if spec.Kind != jobs.Operation || spec.Operation != tc.op || spec.Target != "dev-sql" || spec.Items[0].Name != tc.item ||
				stepList(spec) != tc.steps || spec.InstanceKey != instanceKeyOf(env) {
				t.Fatalf("spec %+v: %s", spec, stepList(spec))
			}
			// The first step carries the exact T-SQL of the plan action.
			if first := spec.Items[0].Steps[0]; first.Title() != prep.Plan.Actions[0].Title {
				t.Fatalf("title %q, plan %q", first.Title(), prep.Plan.Actions[0].Title)
			}
			m := jobs.New(jobs.Options{})
			job, err := m.Start(spec)
			if err != nil {
				t.Fatalf("the spec does not start: %v", err)
			}
			// The job must not outlive the case.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := m.Wait(ctx, job.ID); err != nil {
				t.Fatalf("the job does not end: %v", err)
			}
		})
	}
}

func TestOperationSpecRestoreOverAnExistingDatabase(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	s3.Put(testBucket, "import/sales.bak", []byte("x"), "")
	rds.states["sales"] = "ONLINE"
	prep := prepare(t, env, ops.OpRestore, ops.Params{Database: "sales", Key: "sales.bak", Replace: ops.ReplaceOptions{Mode: ops.ReplaceDrop}})
	spec, err := OperationSpec(env, prep, false)
	if err != nil || len(spec.Items[0].Steps) != len(prep.Plan.Actions) {
		t.Fatalf("%v: %s, %d actions", err, stepList(spec), len(prep.Plan.Actions))
	}
	for i, st := range spec.Items[0].Steps {
		if st.ID() != prep.Plan.Actions[i].ID || st.Title() != prep.Plan.Actions[i].Title {
			t.Fatalf("step %d: %s %q, action %s %q", i, st.ID(), st.Title(), prep.Plan.Actions[i].ID, prep.Plan.Actions[i].Title)
		}
	}
	if _, err := OperationSpec(env, prep, true); err == nil {
		t.Fatal("a replace without waiting")
	}
}

func TestOperationRunsAndKeepsTheTask(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	s3.Put(testBucket, "import/sales.bak", []byte("x"), "")
	var seen []mssql.Task
	env.OnTask = func(task mssql.Task) { seen = append(seen, task) }
	prep := prepare(t, env, ops.OpRestore, ops.Params{Database: "sales", Key: "sales.bak"})
	spec, err := OperationSpec(env, prep, false)
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.New(jobs.Options{})
	job, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	end, err := m.Wait(context.Background(), job.ID)
	if err != nil || end.State != jobs.Succeeded || rds.states["sales"] != "ONLINE" || len(rds.statements) != 1 {
		t.Fatalf("job %s, %v", end.State, err)
	}
	if n := len(seen); n == 0 || seen[n-1].Lifecycle != mssql.Success || seen[n-1].Info == "" {
		t.Fatalf("the full task did not reach the caller: %+v", seen)
	}
	if end.Items[0].Task == nil || end.Items[0].Task.Lifecycle != mssql.Success {
		t.Fatalf("item %+v", end.Items[0])
	}
}

// An operation job is resumable like the other jobs: the restore that was
// queued before the interrupt is followed, not queued again.
func TestOperationJobResumes(t *testing.T) {
	env, rds, s3 := testEnv(t, testTarget)
	s3.Put(testBucket, "import/sales.bak", []byte("x"), "")
	rds.hold["sales"] = true
	prep := prepare(t, env, ops.OpRestore, ops.Params{Database: "sales", Key: "sales.bak"})
	spec, err := OperationSpec(env, prep, false)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m, err := jobs.Open(dir, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, "the wait", m, job.ID, func(j jobs.Job) bool {
		return len(j.Items[0].Steps) == 2 && j.Items[0].Steps[1].Status == jobs.StepRunning
	})
	m = restartOn(t, dir, m)
	if j, _ := m.Get(job.ID); j.State != jobs.Interrupted || j.Kind != jobs.Operation || j.Operation != ops.OpRestore {
		t.Fatalf("job %s %s %s", j.State, j.Kind, j.Operation)
	}
	rds.mu.Lock()
	rds.hold["sales"] = false
	rds.mu.Unlock()
	end := resumeAndWait(t, m, job.ID, env)
	if end.State != jobs.Succeeded || len(rds.statements) != 1 || rds.states["sales"] != "ONLINE" ||
		stepStatuses(end.Items[0]) != "restore:succeeded,wait:succeeded" {
		t.Fatalf("job %s, %d restore(s): %+v", end.State, len(rds.statements), end.Items)
	}
}

func TestOperationSQLStepsRebuild(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	tests := []struct {
		name string
		step jobs.StepInfo
		p    SQLParams
	}{
		{"finish", jobs.StepInfo{ID: "finish", Title: "Finish"}, SQLParams{Op: ops.StepFinish, Statement: mssql.FinishRestore("hr"), Database: "hr"}},
		{"cancel", jobs.StepInfo{ID: "cancel", Title: "Cancel"}, SQLParams{Op: ops.StepCancel, Statement: mssql.CancelTask(7), TaskID: 7}},
		{"drop", jobs.StepInfo{ID: "drop", Title: "Drop"}, SQLParams{Op: ops.StepDrop, Statement: mssql.DropDatabase("old"), Database: "old", Result: "old dropped"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			step, err := rebuildSQL(rebuildOf(t, env, tc.step, tc.p))
			if err != nil || step == nil || step.ID() != tc.step.ID {
				t.Fatalf("%v %v", step, err)
			}
			bad := tc.p
			bad.Statement += "; select 1"
			if _, err := rebuildSQL(rebuildOf(t, env, tc.step, bad)); err == nil {
				t.Fatal("a statement that was edited was rebuilt")
			}
		})
	}
	if _, err := rebuildSQL(rebuildOf(t, env, jobs.StepInfo{ID: "cancel"}, SQLParams{Op: ops.StepCancel, Statement: mssql.CancelTask(0)})); err == nil {
		t.Fatal("a cancel of no task")
	}
}

func TestOperationAccessStepsRebuild(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	good := AccessParams{AccessStep: ops.AccessStep{ID: ops.AccCreateLogin, Login: "dora"}, Statement: mssql.CreateLogin("dora", mssql.MaskedPassword)}
	step, err := rebuildAccess(rebuildOf(t, env, jobs.StepInfo{ID: "create-login", Title: "Create login dora"}, good))
	if err != nil || step == nil || step.ID() != "create-login" {
		t.Fatalf("%v %v", step, err)
	}
	// a repeated id keeps its "#n" as the id of the step
	again := AccessParams{AccessStep: ops.AccessStep{ID: ops.AccAddRole, Database: "sales", User: "dora", Role: "db_datawriter"},
		Statement: mssql.AddRole("sales", "db_datawriter", "dora")}
	if step, err = rebuildAccess(rebuildOf(t, env, jobs.StepInfo{ID: "add-role#2", Title: "t"}, again)); err != nil || step.ID() != "add-role#2" {
		t.Fatalf("%v %v", step, err)
	}
	for name, p := range map[string]AccessParams{
		"edited statement": {AccessStep: good.AccessStep, Statement: mssql.CreateLogin("dora", "Tr0ub4dor&3-xyz!")},
		"master login":     {AccessStep: ops.AccessStep{ID: ops.AccCreateLogin, Login: "admin"}, Statement: mssql.CreateLogin("admin", mssql.MaskedPassword)},
		"dbo":              {AccessStep: ops.AccessStep{ID: ops.AccDropUser, Database: "sales", User: "dbo"}, Statement: mssql.DropUser("sales", "dbo")},
		"system database":  {AccessStep: ops.AccessStep{ID: ops.AccDropUser, Database: "master", User: "x"}, Statement: mssql.DropUser("master", "x")},
		"unknown step":     {AccessStep: ops.AccessStep{ID: "drop-login", Login: "x"}},
		"unmanaged role":   {AccessStep: ops.AccessStep{ID: ops.AccAddRole, Database: "sales", User: "x", Role: "db_securityadmin"}},
	} {
		if _, err := rebuildAccess(rebuildOf(t, env, jobs.StepInfo{ID: "x"}, p)); err == nil {
			t.Errorf("%s: rebuilt", name)
		}
	}
}
