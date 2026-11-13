package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// collInstance is fakeInstance with a database in a collation.
type collInstance struct {
	*fakeInstance
	collation string
	blockers  []mssql.CollationBlocker
}

func (c *collInstance) CollationState(context.Context, string) (mssql.CollationState, error) {
	return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: c.collation,
		State: "ONLINE", Containment: "NONE"}, UserAccess: "MULTI_USER", CanAlter: true, Exists: true}, nil
}

func (c *collInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{Name: name}, true, nil
}

func (c *collInstance) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{Server: "SQL_Latin1_General_CP1_CI_AS"}, nil
}

func (c *collInstance) CollationBlockers(context.Context, string) ([]mssql.CollationBlocker, error) {
	return c.blockers, nil
}

func (c *collInstance) NameClashes(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (c *collInstance) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return nil, nil
}

// withCollation makes a open c as the instance of the target.
func withCollation(a *app, c *collInstance) {
	a.openOps = func(_ context.Context, name string, t config.Target, _ string) (*ops.Session, func(), error) {
		return &ops.Session{Name: name, Target: t, SQL: c}, func() {}, nil
	}
	a.readConfirm = func(string) (string, error) { return "", errors.New("stdin is not a terminal") }
}

func newCollInstance() *collInstance {
	return &collInstance{fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE"}}, collation: "SQL_Latin1_General_CP1_CI_AS"}
}

func TestCollationSetDryRun(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	c := newCollInstance()
	withCollation(a, c)
	code, out, _ := run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS")
	if code != 0 || len(c.ran) != 0 {
		t.Fatalf("code %d, ran %v:\n%s", code, c.ran, out)
	}
	for _, want := range []string{
		"Pre-flight checks of set-collation on dev-sql", "Collation", "SQL_Latin1_General_CP1_CI_AS → Latin1_General_100_CI_AS",
		"ALTER DATABASE [sales] COLLATE Latin1_General_100_CI_AS;", "Rerun with --apply to run it.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCollationSetPreflightFailedExits2(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	c := newCollInstance()
	c.blockers = []mssql.CollationBlocker{{Kind: "check-constraint", Schema: "dbo", Object: "t.ck"}}
	withCollation(a, c)
	code, out, _ := run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS", "-o", "json")
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != exitCheckFailed || doc.Ready || doc.Operation != "set-collation" {
		t.Fatalf("code %d, doc %+v, %v:\n%s", code, doc, err, out)
	}
	code, out, _ = run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS", "--apply")
	if code != exitCheckFailed || len(c.ran) != 0 || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("apply: code %d, ran %v:\n%s", code, c.ran, out)
	}
}

func TestCollationSetApplyNeedsTheDatabaseName(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	c := newCollInstance()
	withCollation(a, c)
	if code, _, _ := run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS", "--apply"); code == 0 || len(c.ran) != 0 {
		t.Fatalf("without confirmation: code %d, ran %v", code, c.ran)
	}
	if code, _, _ := run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS", "--apply", "--confirm", "SALES"); code == 0 || len(c.ran) != 0 {
		t.Fatalf("wrong confirmation: code %d, ran %v", code, c.ran)
	}
	code, out, _ := run(a, "collation", "set", "sales", "Latin1_General_100_CI_AS", "--apply", "--confirm", "sales", "-o", "json")
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || !doc.Applied || doc.JobID == "" ||
		len(c.ran) != 1 || c.ran[0] != mssql.SetDatabaseCollation("sales", "Latin1_General_100_CI_AS") {
		t.Fatalf("code %d, doc %+v, %v, ran %v:\n%s", code, doc, err, c.ran, out)
	}
}

func TestCollationSetBadArguments(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withCollation(a, newCollInstance())
	for _, args := range [][]string{{"sales"}, {"sales", "x; DROP"}, {"master", "Latin1_General_100_CI_AS"}} {
		if code, _, _ := run(a, append([]string{"collation", "set"}, args...)...); code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
}
