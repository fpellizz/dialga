package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// collInstance is fakeInstance with a database in a collation.
type collInstance struct {
	*fakeInstance
	collation string
}

func (c *collInstance) CollationState(context.Context, string) (mssql.CollationState, error) {
	return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: c.collation,
		State: "ONLINE", Containment: "NONE"}, UserAccess: "MULTI_USER", CanAlter: true, Exists: true}, nil
}

func (c *collInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{Name: name}, name != "Nope", nil
}

func (c *collInstance) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{Server: "SQL_Latin1_General_CP1_CI_AS"}, nil
}

func (c *collInstance) CollationBlockers(context.Context, string) ([]mssql.CollationBlocker, error) {
	return nil, nil
}

func (c *collInstance) NameClashes(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (c *collInstance) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return nil, nil
}

func newCollInstance() *collInstance {
	return &collInstance{fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE"}}, collation: "SQL_Latin1_General_CP1_CI_AS"}
}

func TestSetCollationPreviewAndExecute(t *testing.T) {
	doc := testConfig
	f := newCollInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	const path = "/api/v1/targets/dev-sql/plans/set-collation"
	rec := doJSON(srv.h, http.MethodPost, path, `{"database":"sales","collation":"Latin1_General_100_CI_AS"}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || !p.Ready || p.Confirmation != "sales" ||
		p.Collation == nil || p.Collation.From != "SQL_Latin1_General_CP1_CI_AS" || p.Collation.To != "Latin1_General_100_CI_AS" ||
		p.Request.Collation != "Latin1_General_100_CI_AS" || len(p.Plan.Actions) != 1 {
		t.Fatalf("preview: status %d, body %s", rec.Code, rec.Body)
	}
	if len(f.ran) != 0 {
		t.Fatalf("a preview ran %v", f.ran)
	}

	exec := func(body string) *httptest.ResponseRecorder {
		return doJSON(srv.h, http.MethodPost, path+"/execute", body)
	}
	// 422: the confirmation is the database name
	if rec := exec(`{"database":"sales","collation":"Latin1_General_100_CI_AS","confirm":"nope","digest":"` + p.Digest + `"}`); rec.Code != http.StatusUnprocessableEntity || len(f.ran) != 0 {
		t.Fatalf("wrong confirmation: status %d ran %v", rec.Code, f.ran)
	}
	// 412: the digest is not the plan's
	if rec := exec(`{"database":"sales","collation":"Latin1_General_100_CI_AS","confirm":"sales","digest":"0123"}`); rec.Code != http.StatusPreconditionFailed || len(f.ran) != 0 {
		t.Fatalf("stale digest: status %d ran %v", rec.Code, f.ran)
	}
	// 412: a pre-flight that fails (unknown collation)
	if rec := exec(`{"database":"sales","collation":"Nope","confirm":"sales"}`); rec.Code != http.StatusPreconditionFailed || len(f.ran) != 0 {
		t.Fatalf("unknown collation: status %d ran %v", rec.Code, f.ran)
	}
	// 400: bad names, fields of another operation
	for name, body := range map[string]string{
		"no collation": `{"database":"sales"}`,
		"bad name":     `{"database":"sales","collation":"x; DROP DATABASE sales"}`,
		"tables":       `{"database":"sales","collation":"Latin1_General_100_CI_AS","tables":[{"schema":"dbo","name":"t"}]}`,
		"skip":         `{"database":"sales","collation":"Latin1_General_100_CI_AS","skipDataChecks":true}`,
	} {
		if rec := doJSON(srv.h, http.MethodPost, path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	rec = exec(`{"database":"sales","collation":"Latin1_General_100_CI_AS","confirm":"sales","digest":"` + p.Digest + `"}`)
	names, _ := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "job notice step step done" || len(f.ran) != 1 ||
		f.ran[0] != mssql.SetDatabaseCollation("sales", "Latin1_General_100_CI_AS") {
		t.Fatalf("execute: status %d, events %v, ran %v\n%s", rec.Code, names, f.ran, rec.Body)
	}
}

// A ready plan with no action (already at the target) answers a completed
// no-op: the warnings, then done with ok and no job.
func TestExecuteNothingToDo(t *testing.T) {
	doc := testConfig
	f := newCollInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/set-collation/execute",
		`{"database":"sales","collation":"SQL_Latin1_General_CP1_CI_AS"}`)
	names, data := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "notice done" || len(f.ran) != 0 {
		t.Fatalf("status %d, events %v, ran %v\n%s", rec.Code, names, f.ran, rec.Body)
	}
	var done PlanDone
	if err := json.Unmarshal([]byte(data[1]), &done); err != nil || !done.OK || done.JobID != "" || done.Result != NothingToDo {
		t.Fatalf("done %+v %v", done, err)
	}
	if n := len(srv.jobs.List()); n != 0 {
		t.Fatalf("%d job(s) recorded", n)
	}
}
