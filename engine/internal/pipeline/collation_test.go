package pipeline

import (
	"slices"
	"testing"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

func TestUniqueIDs(t *testing.T) {
	var actions []core.Action
	for _, id := range []string{"create-user", "add-role", "drop-role", "add-role", "add-role", "drop-role", "msdb-user"} {
		actions = append(actions, core.Action{ID: id})
	}
	want := []string{"create-user", "add-role", "drop-role", "add-role#2", "add-role#3", "drop-role#2", "msdb-user"}
	if got := uniqueIDs(actions); !slices.Equal(got, want) {
		t.Fatalf("uniqueIDs = %v, want %v", got, want)
	}
	if got := uniqueIDs(nil); len(got) != 0 {
		t.Fatalf("uniqueIDs(nil) = %v", got)
	}
}

func collationParams(t *testing.T, st ops.CollationStep) CollationParams {
	t.Helper()
	stmt, err := st.Statement()
	if err != nil {
		t.Fatal(err)
	}
	return CollationParams{CollationStep: st, Statement: stmt}
}

func TestOperationCollationStepsRebuild(t *testing.T) {
	env, _, _ := testEnv(t, testTarget)
	set := ops.CollationStep{ID: ops.CollSetDatabase, Database: "sales", Collation: "Latin1_General_100_CI_AS", From: "SQL_Latin1_General_CP1_CI_AS"}
	unit := &mssql.ConversionUnit{Database: "sales", Collation: "Latin1_General_100_CI_AS", Tables: []mssql.TableConversion{{
		Schema: "dbo", Name: "orders",
		Columns: []mssql.ColumnChange{{Name: "code", Type: "nvarchar(50)", From: "SQL_Latin1_General_CP1_CI_AS", To: "Latin1_General_100_CI_AS"}},
	}}}
	convert := ops.CollationStep{ID: ops.CollConvert, Database: "sales", Collation: "Latin1_General_100_CI_AS", Unit: unit}
	for id, st := range map[string]ops.CollationStep{"set-collation": set, "convert#2": convert} {
		step, err := rebuildCollation(rebuildOf(t, env, jobs.StepInfo{ID: id, Title: "t"}, collationParams(t, st)))
		if err != nil || step == nil || step.ID() != id {
			t.Fatalf("%s: %v %v", id, step, err)
		}
	}
	edited := collationParams(t, set)
	edited.Statement += "; DROP DATABASE sales"
	badUnit := *unit
	badUnit.Database = "other"
	for name, p := range map[string]CollationParams{
		"edited statement": edited,
		"system database":  {CollationStep: ops.CollationStep{ID: ops.CollSetDatabase, Database: "master", Collation: "Latin1_General_100_CI_AS"}},
		"bad collation":    {CollationStep: ops.CollationStep{ID: ops.CollSetDatabase, Database: "sales", Collation: "Latin1 General"}},
		"unknown step":     {CollationStep: ops.CollationStep{ID: "drop", Database: "sales", Collation: "Latin1_General_100_CI_AS"}},
		"unit of another":  {CollationStep: ops.CollationStep{ID: ops.CollConvert, Database: "sales", Collation: "Latin1_General_100_CI_AS", Unit: &badUnit}},
	} {
		if _, err := rebuildCollation(rebuildOf(t, env, jobs.StepInfo{ID: "x"}, p)); err == nil {
			t.Errorf("%s: rebuilt", name)
		}
	}
}
