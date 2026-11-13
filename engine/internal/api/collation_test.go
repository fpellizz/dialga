package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

const collServer = "SQL_Latin1_General_CP1_CI_AS"

// collationInstance is fakeInstance with the collation reads.
type collationInstance struct {
	*fakeInstance
	err error
}

func (f *collationInstance) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{Server: collServer, Tempdb: collServer}, f.err
}

func (f *collationInstance) DatabaseCollations(context.Context) ([]mssql.DatabaseCollation, error) {
	return []mssql.DatabaseCollation{
		{Name: "off", State: "OFFLINE", Containment: "NONE"},
		{Name: "sales", Collation: "Latin1_General_CS_AS", State: "ONLINE", Containment: "NONE"},
	}, f.err
}

func (f *collationInstance) CollationState(_ context.Context, db string) (mssql.CollationState, error) {
	switch db {
	case "sales":
		return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: db, Collation: "Latin1_General_CS_AS",
			State: "ONLINE", Containment: "NONE"}, Exists: true}, f.err
	case "off":
		return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: db, State: "OFFLINE"}, Exists: true}, f.err
	}
	return mssql.CollationState{}, f.err
}

func (f *collationInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{Name: name, CodePage: 1252}, true, nil
}

func (f *collationInstance) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return []mssql.CharColumn{
		{Schema: "dbo", Table: "t", Column: "a", Type: "varchar", BaseType: "varchar", MaxLength: 10, Collation: "Latin1_General_CS_AS"},
		{Schema: "dbo", Table: "t", Column: "b", Type: "text", BaseType: "text", MaxLength: 16, Collation: collServer},
	}, f.err
}

func (f *collationInstance) Collations(context.Context) ([]mssql.CollationInfo, error) {
	return []mssql.CollationInfo{{Name: "Latin1_General_CS_AS"}, {Name: "Cyrillic_General_CI_AS"}, {Name: "Latin1_General_100_BIN2"}}, f.err
}

func newCollationInstance() *collationInstance {
	return &collationInstance{fakeInstance: &fakeInstance{}}
}

func TestCollationViews(t *testing.T) {
	doc := testConfig
	srv := newOpsServer(t, &doc, testEnv, newCollationInstance(), fakeObjects{})

	rec := doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/collation", "")
	var o ops.CollationOverview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &o) != nil || o.Target != "dev-sql" || o.Server != collServer ||
		!o.ServerFixed || len(o.Databases) != 2 || !o.Databases[1].MismatchServer || !o.Databases[1].TempTableRisk {
		t.Fatalf("overview: status %d, body %s", rec.Code, rec.Body)
	}

	rec = doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/databases/sales/collation", "")
	var v ops.DatabaseCollationView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Database != "sales" || v.CharColumns != 2 ||
		len(v.Groups) != 2 || !v.Groups[0].Default || len(v.Columns) != 1 || v.Columns[0].Unsupported == "" {
		t.Fatalf("database: status %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"unsupported"`) || strings.Contains(rec.Body.String(), "ObjectID") {
		t.Fatalf("column shape: %s", rec.Body)
	}

	rec = doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/collations", "")
	var l CollationList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || len(l.Collations) != 3 {
		t.Fatalf("collations: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/collations?q=latin1", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || len(l.Collations) != 2 {
		t.Fatalf("filtered collations: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/collations?q=zzz", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"collations":[]`) {
		t.Fatalf("no match: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestCollationViewErrors(t *testing.T) {
	doc := testConfig
	srv := newOpsServer(t, &doc, testEnv, newCollationInstance(), fakeObjects{})
	for path, want := range map[string]int{
		"/api/v1/targets/dev-sql/databases/nope/collation":                 http.StatusNotFound,
		"/api/v1/targets/dev-sql/databases/off/collation":                  http.StatusConflict,
		"/api/v1/targets/dev-sql/databases/tempdb/collation":               http.StatusBadRequest,
		"/api/v1/targets/other/databases/sales/collation":                  http.StatusNotFound,
		"/api/v1/targets/other/collation":                                  http.StatusNotFound,
		"/api/v1/targets/other/collations":                                 http.StatusNotFound,
		"/api/v1/targets/dev-sql/collations?q=" + strings.Repeat("x", 129): http.StatusBadRequest,
	} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", path, rec.Code, want, rec.Body)
		}
	}

	// SQL errors are 502
	failing := newCollationInstance()
	failing.err = errors.New("boom")
	srv = newOpsServer(t, &doc, testEnv, failing, fakeObjects{})
	for _, path := range []string{"/api/v1/targets/dev-sql/collation", "/api/v1/targets/dev-sql/collations",
		"/api/v1/targets/dev-sql/databases/sales/collation"} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != http.StatusBadGateway {
			t.Errorf("%s with a SQL error: status %d", path, rec.Code)
		}
	}

	// the password source cannot give a password: 422
	srv = newOpsServer(t, &doc, nil, newCollationInstance(), fakeObjects{})
	for _, path := range []string{"/api/v1/targets/dev-sql/collation", "/api/v1/targets/dev-sql/collations",
		"/api/v1/targets/dev-sql/databases/sales/collation"} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s without a password: status %d", path, rec.Code)
		}
	}
	if srv.opened {
		t.Error("a session opened without a password")
	}
}

func TestCollationViewsNeedThePrompt(t *testing.T) {
	doc := promptConfig
	srv := newOpsServer(t, &doc, nil, newCollationInstance(), fakeObjects{})
	for _, path := range []string{"/api/v1/targets/dev-sql/collation", "/api/v1/targets/dev-sql/collations",
		"/api/v1/targets/dev-sql/databases/sales/collation"} {
		rec := doJSON(srv.h, http.MethodGet, path, "")
		if rec.Code != http.StatusPreconditionRequired || rec.Header().Get("Dialga-Password-Target") != "dev-sql" {
			t.Errorf("%s: status %d, header %q", path, rec.Code, rec.Header().Get("Dialga-Password-Target"))
		}
	}
}
