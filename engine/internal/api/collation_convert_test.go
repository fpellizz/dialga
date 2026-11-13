package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// convertInstance is fakeInstance with a database sales (default
// SQL_Latin1_General_CP1_CI_AS) and its tables customers and hist (temporal).
type convertInstance struct {
	*fakeInstance
	cat mssql.ConversionCatalog
}

const convertFrom, convertTo = "SQL_Latin1_General_CP1_CI_AS", "Latin1_General_100_CS_AS"

func newConvertInstance() *convertInstance {
	col := func(id int64, table string) mssql.CharColumn {
		return mssql.CharColumn{ObjectID: id, ColumnID: 1, Schema: "dbo", Table: table, Column: "name", Type: "nvarchar",
			BaseType: "nvarchar", MaxLength: 100, Nullable: true, Collation: convertFrom}
	}
	return &convertInstance{
		fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE"}},
		cat: mssql.ConversionCatalog{Major: 16,
			Tables: map[int64]mssql.CatalogTable{
				1: {Schema: "dbo", Name: "customers", ReservedMB: 1},
				2: {Schema: "dbo", Name: "hist", Temporal: 1},
			},
			Columns: []mssql.CharColumn{col(1, "customers"), col(2, "hist")}},
	}
}

func (f *convertInstance) CollationState(context.Context, string) (mssql.CollationState, error) {
	return mssql.CollationState{DatabaseCollation: mssql.DatabaseCollation{Name: "sales", Collation: convertFrom, State: "ONLINE"},
		UserAccess: "MULTI_USER", CanAlter: true, Exists: true}, nil
}

func (f *convertInstance) Collation(_ context.Context, name string) (mssql.CollationInfo, bool, error) {
	if name != convertFrom && name != convertTo {
		return mssql.CollationInfo{}, false, nil
	}
	return mssql.CollationInfo{Name: name, CodePage: 1252}, true, nil
}

func (f *convertInstance) ConversionCatalog(context.Context, string) (mssql.ConversionCatalog, error) {
	return f.cat, nil
}

func (f *convertInstance) Exec(ctx context.Context, stmt string) error {
	if err := f.fakeInstance.Exec(ctx, stmt); err != nil {
		return err
	}
	for i := range f.cat.Columns {
		if strings.Contains(stmt, "["+f.cat.Columns[i].Table+"] ALTER COLUMN") {
			f.cat.Columns[i].Collation = convertTo
		}
	}
	return nil
}

func TestConvertColumnsPreview(t *testing.T) {
	doc := testConfig
	f := newConvertInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	// the target defaults to the collation of the database: nothing to do
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/convert-columns", `{"database":"sales"}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || !p.Ready || len(p.Plan.Actions) != 0 ||
		p.Request.Collation != convertFrom {
		t.Fatalf("default: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/convert-columns", `{"database":"sales","collation":"`+convertTo+`"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || !p.Ready || len(p.Plan.Actions) != 1 ||
		p.Confirmation != "sales" || p.Collation == nil || p.Collation.Units != 1 || len(p.Collation.Refused) != 1 ||
		p.Collation.Refused[0].Name != "hist" || p.Request.Collation != convertTo {
		t.Fatalf("whole database: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/convert-columns",
		`{"database":"sales","collation":"`+convertTo+`","tables":[{"schema":"dbo","name":"hist"}]}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Ready {
		t.Fatalf("refused table: status %d, body %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"bad collation": `{"database":"sales","collation":"Latin1;DROP"}`,
		"bad table":     `{"database":"sales","tables":[{"schema":"dbo","name":""}]}`,
		"twice":         `{"database":"sales","tables":[{"schema":"dbo","name":"a"},{"schema":"DBO","name":"A"}]}`,
		"login":         `{"database":"sales","login":"ana"}`,
	} {
		if rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/convert-columns", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if len(f.ran) != 0 {
		t.Errorf("a preview ran %v", f.ran)
	}
}

func TestConvertColumnsExecute(t *testing.T) {
	doc := testConfig
	f := newConvertInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	const path = "/api/v1/targets/dev-sql/plans/convert-columns"
	rec := doJSON(srv.h, http.MethodPost, path, `{"database":"sales","collation":"`+convertTo+`"}`)
	var p PlanPreview
	if json.Unmarshal(rec.Body.Bytes(), &p) != nil {
		t.Fatal(rec.Body)
	}
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"wrong confirmation": {`{"database":"sales","collation":"` + convertTo + `","confirm":"dev-sql"}`, http.StatusUnprocessableEntity},
		"plan changed":       {`{"database":"sales","collation":"` + convertTo + `","confirm":"sales","digest":"0123"}`, http.StatusPreconditionFailed},
		"refused table":      {`{"database":"sales","collation":"` + convertTo + `","tables":[{"schema":"dbo","name":"hist"}],"confirm":"sales"}`, http.StatusPreconditionFailed},
	} {
		if rec := doJSON(srv.h, http.MethodPost, path+"/execute", tc.body); rec.Code != tc.code || len(f.ran) != 0 {
			t.Errorf("%s: status %d ran %v, body %s", name, rec.Code, f.ran, rec.Body)
		}
	}
	rec = doJSON(srv.h, http.MethodPost, path+"/execute", `{"database":"sales","collation":"`+convertTo+`","confirm":"sales","digest":"`+p.Digest+`"}`)
	names, data := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "job notice step step done" || len(f.ran) != 1 {
		t.Fatalf("status %d, events %v, ran %v\n%s", rec.Code, names, f.ran, rec.Body)
	}
	var done PlanDone
	if json.Unmarshal([]byte(data[len(data)-1]), &done) != nil || !done.OK || done.JobID == "" {
		t.Fatalf("done %s", data[len(data)-1])
	}
	job, err := srv.jobs.Get(done.JobID)
	if err != nil || job.Operation != "convert-columns" || job.Items[0].Steps[0].ID != "convert" ||
		!strings.Contains(job.Items[0].Steps[0].Detail, "converted to "+convertTo) {
		t.Fatalf("job %+v %v", job, err)
	}
}
