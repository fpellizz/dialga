package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// collFake adds to fakeSQL the collation reads of set-collation.
type collFake struct {
	*fakeSQL
	state    mssql.CollationState
	known    map[string]mssql.CollationInfo
	server   mssql.ServerCollation
	blockers []mssql.CollationBlocker
	clashes  []string
	cols     []mssql.CharColumn
	colsErr  error
	// afterExec lets a test change state when a statement runs
	afterExec func(c *collFake, stmt string) error
	// ctxErrs is ctx.Err() of every Exec, as it was called
	ctxErrs []error
}

func (c *collFake) CollationState(context.Context, string) (mssql.CollationState, error) {
	return c.state, nil
}

func (c *collFake) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	info, ok := c.known[name]
	return info, ok, nil
}

func (c *collFake) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return c.server, nil
}

func (c *collFake) CollationBlockers(context.Context, string) ([]mssql.CollationBlocker, error) {
	return c.blockers, nil
}

func (c *collFake) NameClashes(context.Context, string, string) ([]string, error) {
	return c.clashes, nil
}

func (c *collFake) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return c.cols, c.colsErr
}

func (c *collFake) Exec(ctx context.Context, stmt string) error {
	c.ctxErrs = append(c.ctxErrs, ctx.Err())
	if err := c.fakeSQL.Exec(ctx, stmt); err != nil {
		return err
	}
	if c.afterExec != nil {
		return c.afterExec(c, stmt)
	}
	return nil
}

const (
	collOld = "SQL_Latin1_General_CP1_CI_AS"
	collNew = "Latin1_General_100_CI_AS"
)

func newCollFake() *collFake {
	return &collFake{
		fakeSQL: &fakeSQL{states: map[string]string{"sales": "ONLINE", "off": "OFFLINE"}},
		state: mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: collOld,
			State: "ONLINE", Containment: "NONE"}, UserAccess: "MULTI_USER", CanAlter: true, Exists: true},
		known: map[string]mssql.CollationInfo{
			collNew:                            {Name: collNew},
			collOld:                            {Name: collOld, Legacy: true},
			"Latin1_General_100_CI_AS_SC_UTF8": {Name: "Latin1_General_100_CI_AS_SC_UTF8", UTF8: true},
		},
		server: mssql.ServerCollation{Server: collOld, Tempdb: collOld},
	}
}

func (c *collFake) session() *Session {
	s := newSession(c.fakeSQL, nil)
	s.SQL = c
	return s
}

func buildSet(t *testing.T, c *collFake, db, coll string) *Prepared {
	t.Helper()
	p, err := c.session().SetCollation(context.Background(), Params{Database: db, Collation: coll})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateSetCollation(t *testing.T) {
	for name, tc := range map[string]struct {
		p    Params
		want string
	}{
		"ok":           {Params{Database: "sales", Collation: collNew}, ""},
		"no collation": {Params{Database: "sales"}, "needs the collation"},
		"bad coll":     {Params{Database: "sales", Collation: "x; DROP"}, "collation"},
		"system db":    {Params{Database: "master", Collation: collNew}, "system database"},
		"tables":       {Params{Database: "sales", Collation: collNew, Tables: []mssql.TableRef{{Schema: "dbo", Name: "t"}}}, "convert-columns"},
		"skip":         {Params{Database: "sales", Collation: collNew, SkipDataChecks: true}, "convert-columns"},
		"login":        {Params{Database: "sales", Collation: collNew, Login: "x"}, "grant"},
	} {
		err := Validate(OpSetCollation, tc.p, testTarget)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestSetCollationPlan(t *testing.T) {
	c := newCollFake()
	p := buildSet(t, c, "sales", collNew)
	if !p.Ready() {
		t.Fatalf("not ready: %v", p.Checks)
	}
	if got := ids(p); got != "set-collation" {
		t.Fatalf("ids = %s", got)
	}
	a := p.Plan.Actions[0]
	if a.Risk != core.Destructive || a.Statement != mssql.SetDatabaseCollation("sales", collNew) {
		t.Fatalf("action %+v", a)
	}
	if p.Plan.Confirmation() != "sales" {
		t.Errorf("confirmation = %q", p.Plan.Confirmation())
	}
	if p.Collation == nil || p.Collation.From != collOld || p.Collation.To != collNew || p.Collation.Database != "sales" {
		t.Errorf("collation plan %+v", p.Collation)
	}
	steps := p.CollationSteps()
	if len(steps) != 1 || steps[0].From != collOld {
		t.Errorf("steps %+v", steps)
	}
	if len(c.ran) != 0 {
		t.Errorf("the plan ran: %v", c.ran)
	}
	want := "database:ok access:ok collation:ok permission:ok availability:ok dependents:ok names:ok tasks:ok sessions:ok server:warning columns:ok utf8:ok"
	if got := verdicts(p); got != want {
		t.Errorf("checks\n got %s\nwant %s", got, want)
	}
	// protected target: the target name too
	c2 := newCollFake()
	s := c2.session()
	s.Target.Protected = true
	pp, err := s.SetCollation(context.Background(), Params{Database: "sales", Collation: collNew})
	if err != nil || pp.Plan.Confirmation() == "" {
		t.Fatalf("protected: %v %q", err, pp.Plan.Confirmation())
	}
}

func TestSetCollationCanonicalName(t *testing.T) {
	c := newCollFake()
	c.known["latin1_general_100_ci_as"] = mssql.CollationInfo{Name: collNew}
	p := buildSet(t, c, "sales", "latin1_general_100_ci_as")
	if p.Params.Collation != collNew || !strings.Contains(p.Plan.Actions[0].Statement, "COLLATE "+collNew+";") {
		t.Fatalf("params %+v stmt %s", p.Params, p.Plan.Actions[0].Statement)
	}
}

func TestSetCollationChecks(t *testing.T) {
	tests := []struct {
		name   string
		change func(c *collFake)
		coll   string
		want   string // step:status that must be in verdicts
		ready  bool
		detail string
	}{
		{"not online", func(c *collFake) { c.fakeSQL.states["sales"] = "OFFLINE" }, collNew, "database:failed", false, "not ONLINE"},
		{"read only", func(c *collFake) { c.state.ReadOnly = true }, collNew, "access:failed", false, "read-only"},
		{"single user", func(c *collFake) { c.state.UserAccess = "SINGLE_USER" }, collNew, "access:failed", false, "maintaining"},
		{"restricted", func(c *collFake) { c.state.UserAccess = "RESTRICTED_USER" }, collNew, "access:failed", false, "RESTRICTED_USER"},
		{"unknown collation", nil, "Nope_CI", "collation:failed", false, "not a collation"},
		{"already", func(c *collFake) { c.state.Collation = collNew }, collNew, "collation:warning", true, "nothing to do"},
		{"no permission", func(c *collFake) { c.state.CanAlter = false }, collNew, "permission:failed", false, "cannot ALTER"},
		{"AG", func(c *collFake) { c.state.AvailabilityReplica = true }, collNew, "availability:failed", false, "Multi-AZ"},
		{"mirrored", func(c *collFake) { c.state.Mirrored = true }, collNew, "availability:failed", false, "Multi-AZ"},
		{"dependents", func(c *collFake) {
			c.blockers = []mssql.CollationBlocker{{Kind: "schema-bound", Schema: "dbo", Object: "v1"}}
		}, collNew, "dependents:failed", false, "schema-bound dbo.v1"},
		{"clashes", func(c *collFake) { c.clashes = []string{"object dbo.t"} }, collNew, "names:failed", false, "dbo.t"},
		{"tasks", func(c *collFake) { c.fakeSQL.active = map[string]int{"sales": 1} }, collNew, "tasks:failed", false, "running or queued"},
		{"sessions", func(c *collFake) { c.fakeSQL.sessions = map[string]int{"sales": 3} }, collNew, "sessions:warning", true, "3 session"},
		{"server match", func(c *collFake) { c.server.Server = collNew }, collNew, "server:ok", true, "matches the server"},
		{"contained", func(c *collFake) { c.state.Containment = "PARTIAL" }, collNew, "server:ok", true, "contained"},
		{"temp tables", nil, collNew, "server:warning", true, "Msg 468"},
		{"columns keep", func(c *collFake) {
			c.cols = []mssql.CharColumn{
				{Table: "a", Column: "x", Type: "varchar", BaseType: "varchar", Collation: collOld},
				{Table: "a", Column: "y", Type: "nvarchar", BaseType: "nvarchar", Collation: collOld},
				{Table: "a", Column: "z", Type: "text", BaseType: "text", Collation: collOld}, // unsupported
				{Table: "a", Column: "w", Type: "varchar", BaseType: "varchar", Collation: "Other_CS_AS"},
			}
		}, collNew, "columns:warning", true, "2 column(s) keep " + collOld},
		{"utf8", nil, "Latin1_General_100_CI_AS_SC_UTF8", "utf8:warning", true, "UTF-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCollFake()
			if tc.change != nil {
				tc.change(c)
			}
			p := buildSet(t, c, "sales", tc.coll)
			if !strings.Contains(verdicts(p), tc.want) {
				t.Fatalf("verdicts %s, want %s", verdicts(p), tc.want)
			}
			if p.Ready() != tc.ready {
				t.Errorf("ready = %v: %v", p.Ready(), p.Checks)
			}
			found := false
			for _, ev := range p.Checks {
				if strings.Contains(ev.Detail, tc.detail) {
					found = true
				}
			}
			if !found {
				t.Errorf("no check says %q: %v", tc.detail, p.Checks)
			}
			if tc.name == "already" && len(p.Plan.Actions) != 0 {
				t.Errorf("already: %d actions", len(p.Plan.Actions))
			}
			if !p.Ready() && len(p.Plan.Actions) != 0 {
				t.Errorf("a refused plan has actions")
			}
		})
	}
}

func TestSetCollationListsAtMostTenBlockers(t *testing.T) {
	c := newCollFake()
	for i := 0; i < 13; i++ {
		c.blockers = append(c.blockers, mssql.CollationBlocker{Kind: "check-constraint", Schema: "dbo", Object: "t.ck" + string(rune('a'+i))})
	}
	p := buildSet(t, c, "sales", collNew)
	var detail string
	for _, ev := range p.Checks {
		if ev.Step == "dependents" {
			detail = ev.Detail
		}
	}
	if !strings.Contains(detail, "and 3 more") || strings.Contains(detail, "ckm") {
		t.Errorf("detail %q", detail)
	}
}

func TestSetCollationReadErrorsAreReturned(t *testing.T) {
	c := newCollFake()
	c.colsErr = errors.New("boom")
	if _, err := c.session().SetCollation(context.Background(), Params{Database: "sales", Collation: collNew}); err == nil {
		t.Fatal("a failed read was swallowed")
	}
}

func TestRunSetCollation(t *testing.T) {
	ctx := context.Background()
	step := CollationStep{ID: CollSetDatabase, Database: "sales", Collation: collNew, From: collOld}

	t.Run("runs the batch", func(t *testing.T) {
		c := newCollFake()
		detail, err := c.session().RunCollationStep(ctx, step)
		if err != nil || detail != "sales is now "+collNew {
			t.Fatalf("%q %v", detail, err)
		}
		if len(c.ran) != 1 || c.ran[0] != mssql.SetDatabaseCollation("sales", collNew) {
			t.Fatalf("ran %v", c.ran)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		c := newCollFake()
		c.state.Collation = collNew
		detail, err := c.session().RunCollationStep(ctx, step)
		if err != nil || !strings.Contains(detail, "already") || len(c.ran) != 0 {
			t.Fatalf("%q %v ran %v", detail, err, c.ran)
		}
	})
	t.Run("already but left single user", func(t *testing.T) {
		c := newCollFake()
		c.state.Collation, c.state.UserAccess = collNew, "SINGLE_USER"
		if _, err := c.session().RunCollationStep(ctx, step); err != nil {
			t.Fatal(err)
		}
		if len(c.ran) != 1 || c.ran[0] != mssql.MultiUserIfSingle("sales") {
			t.Fatalf("ran %v", c.ran)
		}
	})
	t.Run("already and single user, not by this step", func(t *testing.T) {
		c := newCollFake()
		c.state.Collation, c.state.UserAccess = collNew, "SINGLE_USER"
		noFrom := step
		noFrom.From = ""
		for _, st := range []CollationStep{noFrom, {ID: CollSetDatabase, Database: "sales", Collation: collNew, From: collNew}} {
			detail, err := c.session().RunCollationStep(ctx, st)
			if err != nil || !strings.Contains(detail, "not by this step") {
				t.Fatalf("%q %v", detail, err)
			}
		}
		if len(c.ran) != 0 {
			t.Fatalf("ran %v", c.ran)
		}
	})
	t.Run("changed since the plan", func(t *testing.T) {
		c := newCollFake()
		c.state.Collation = "Other_CS_AS"
		_, err := c.session().RunCollationStep(ctx, step)
		if err == nil || !strings.Contains(err.Error(), "changed since the plan") || len(c.ran) != 0 {
			t.Fatalf("%v ran %v", err, c.ran)
		}
	})
	t.Run("missing", func(t *testing.T) {
		c := newCollFake()
		c.state = mssql.CollationState{}
		if _, err := c.session().RunCollationStep(ctx, step); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("failure reopens the database", func(t *testing.T) {
		c := newCollFake()
		// the cancelled context must not stop the reopening
		cctx, cancel := context.WithCancel(ctx)
		c.fakeSQL.onExec = func(_ *fakeSQL, stmt string) error {
			if strings.HasPrefix(stmt, "USE [master]") {
				cancel()
				return errors.New("Msg 5075")
			}
			return nil
		}
		_, err := c.session().RunCollationStep(cctx, step)
		if err == nil || !strings.Contains(err.Error(), "Msg 5075") || !strings.Contains(err.Error(), "open to all sessions again") {
			t.Fatalf("%v", err)
		}
		if len(c.ran) != 2 || c.ran[1] != mssql.MultiUserIfSingle("sales") || c.ctxErrs[1] != nil {
			t.Fatalf("ran %v ctx errors %v", c.ran, c.ctxErrs)
		}
	})
	t.Run("failure and the reopening fails", func(t *testing.T) {
		c := newCollFake()
		c.fakeSQL.execErr = errors.New("connection lost")
		_, err := c.session().RunCollationStep(ctx, step)
		if err == nil || !strings.Contains(err.Error(), "could not be reopened") || !strings.Contains(err.Error(), "SET MULTI_USER") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("execute through the plan", func(t *testing.T) {
		c := newCollFake()
		p := buildSet(t, c, "sales", collNew)
		if _, err := p.Execute(ctx, ExecOptions{Confirm: "sales"}); err != nil {
			t.Fatal(err)
		}
		if len(c.ran) != 1 {
			t.Fatalf("ran %v", c.ran)
		}
	})
}
