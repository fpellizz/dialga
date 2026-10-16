package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// recorder builds actions that record their runs and fail when told.
type recorder struct{ ran []string }

func (r *recorder) action(id string, risk Risk, fail error) Action {
	return Action{
		ID: id, Title: "Action " + id, Kind: KindSQL, Risk: risk,
		Statement: "exec dbo.do_" + id + ";",
		Run: func(context.Context) (string, error) {
			r.ran = append(r.ran, id)
			return "done " + id, fail
		},
	}
}

func statuses(events []Event) string {
	var parts []string
	for _, e := range events {
		parts = append(parts, e.Step+":"+string(e.Status))
	}
	return strings.Join(parts, " ")
}

func TestConfirmation(t *testing.T) {
	r := &recorder{}
	change := r.action("a", Change, nil)
	drop := r.action("b", Destructive, nil)
	tests := []struct {
		name string
		plan Plan
		want string
	}{
		{"change on a normal target", Plan{Target: "dev", Actions: []Action{change}}, ""},
		{"destructive asks for the subject", Plan{Target: "dev", Subject: "sales", Actions: []Action{change, drop}}, "sales"},
		{"protected asks for the target", Plan{Target: "prod", Protected: true, Subject: "sales", Actions: []Action{drop}}, "prod"},
		{"protected change too", Plan{Target: "prod", Protected: true, Actions: []Action{change}}, "prod"},
		{"empty plan", Plan{Target: "prod", Protected: true}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.Confirmation(); got != tc.want {
				t.Fatalf("Confirmation() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	p := Plan{
		Operation: "drop", Target: "dev-sql", Instance: "dev-sqlserver", Subject: "sales",
		Actions: []Action{{
			ID: "drop", Title: "Drop database sales", Kind: KindSQL, Risk: Destructive,
			Statement: "exec msdb.dbo.rds_drop_database\n     @db_name = N'sales';",
		}},
	}
	var b bytes.Buffer
	if err := p.Preview(&b); err != nil {
		t.Fatal(err)
	}
	want := `Dry run: drop on dev-sql (instance dev-sqlserver). Nothing is changed.
  1. Drop database sales  [destructive]
       exec msdb.dbo.rds_drop_database
            @db_name = N'sales';
Running it asks to retype "sales".
`
	if b.String() != want {
		t.Fatalf("preview:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestExecuteRunsInOrder(t *testing.T) {
	r := &recorder{}
	p := Plan{Target: "dev", Actions: []Action{r.action("a", Change, nil), r.action("b", Change, nil)}}
	var events []Event
	if err := p.Execute(context.Background(), "", func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.ran, ",") != "a,b" || statuses(events) != "a:running a:ok b:running b:ok" || events[1].Detail != "done a" {
		t.Fatalf("ran %v, events %v", r.ran, events)
	}
}

func TestExecuteStopsAtFirstFailure(t *testing.T) {
	r := &recorder{}
	boom := errors.New("boom")
	p := Plan{Target: "dev", Actions: []Action{
		r.action("a", Change, nil), r.action("b", Change, boom), r.action("c", Change, nil),
	}}
	var events []Event
	err := p.Execute(context.Background(), "", func(e Event) { events = append(events, e) })
	if !errors.Is(err, boom) || strings.Join(r.ran, ",") != "a,b" {
		t.Fatalf("err %v, ran %v", err, r.ran)
	}
	if got := statuses(events); got != "a:running a:ok b:running b:failed c:skipped" {
		t.Fatalf("events %s", got)
	}
}

func TestExecuteNeedsTheConfirmation(t *testing.T) {
	r := &recorder{}
	p := Plan{Target: "prod", Protected: true, Actions: []Action{r.action("a", Change, nil)}}
	for _, wrong := range []string{"", "Prod", "dev"} {
		if err := p.Execute(context.Background(), wrong, func(Event) {}); !errors.Is(err, ErrNotConfirmed) {
			t.Fatalf("confirmed %q: err %v", wrong, err)
		}
	}
	if len(r.ran) != 0 {
		t.Fatalf("an action ran without confirmation: %v", r.ran)
	}
	if err := p.Execute(context.Background(), "prod", func(Event) {}); err != nil || len(r.ran) != 1 {
		t.Fatalf("confirmed: err %v, ran %v", err, r.ran)
	}
}

func TestExecuteWithoutRunnerOrAfterCancel(t *testing.T) {
	p := Plan{Target: "dev", Actions: []Action{{ID: "x", Title: "Preview only", Risk: Change}}}
	if err := p.Execute(context.Background(), "", func(Event) {}); err == nil || !strings.Contains(err.Error(), "no runner") {
		t.Fatalf("no runner: err %v", err)
	}
	r := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = Plan{Target: "dev", Actions: []Action{r.action("a", Change, nil)}}
	if err := p.Execute(ctx, "", func(Event) {}); !errors.Is(err, context.Canceled) || len(r.ran) != 0 {
		t.Fatalf("cancelled: err %v, ran %v", err, r.ran)
	}
}

func TestPlanJSONHasNoRunner(t *testing.T) {
	r := &recorder{}
	data, err := json.Marshal(Plan{Operation: "restore", Target: "dev", Actions: []Action{r.action("a", Change, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"statement":"exec dbo.do_a;"`) || strings.Contains(string(data), "Run") {
		t.Fatalf("json %s", data)
	}
}
