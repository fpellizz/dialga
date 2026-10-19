package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

// accessInstance is fakeInstance with logins and users.
type accessInstance struct {
	*fakeInstance
	logins []mssql.Login
	users  []mssql.DatabaseUser
}

func (f *accessInstance) Logins(context.Context, string) ([]mssql.Login, error) { return f.logins, nil }

func (f *accessInstance) DatabaseAccess(context.Context, string, string) (mssql.DatabaseAccess, error) {
	return mssql.DatabaseAccess{Users: f.users, WindowsPrincipals: 2}, nil
}

func newAccessInstance() *accessInstance {
	return &accessInstance{
		fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE", "off": "RESTORING"}},
		logins:       []mssql.Login{{Name: "admin", Master: true}, {Name: "ana"}},
		users: []mssql.DatabaseUser{
			{Name: "ana", Login: "ana", Roles: []string{"db_datareader"}, Level: "read", Managed: true},
			{Name: "old", Orphan: true, Roles: []string{}, Level: "none", Managed: true},
		},
	}
}

func TestLoginsAndAccessViews(t *testing.T) {
	doc := testConfig
	srv := newOpsServer(t, &doc, testEnv, newAccessInstance(), fakeObjects{})
	rec := doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/logins", "")
	var l LoginList
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &l) != nil || len(l.Logins) != 2 || !l.Logins[0].Master {
		t.Fatalf("logins: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodGet, "/api/v1/targets/dev-sql/databases/sales/access", "")
	var v ops.AccessView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Target != "dev-sql" ||
		v.Database != "sales" || len(v.Users) != 2 || !v.Users[1].Orphan || v.WindowsPrincipals != 2 {
		t.Fatalf("access: status %d, body %s", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{
		"/api/v1/targets/dev-sql/databases/nope/access":   http.StatusNotFound,
		"/api/v1/targets/dev-sql/databases/off/access":    http.StatusBadGateway,
		"/api/v1/targets/dev-sql/databases/tempdb/access": http.StatusBadRequest,
		"/api/v1/targets/other/databases/sales/access":    http.StatusNotFound,
		"/api/v1/targets/other/logins":                    http.StatusNotFound,
	} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
	srv = newOpsServer(t, &doc, nil, newAccessInstance(), fakeObjects{})
	for _, path := range []string{"/api/v1/targets/dev-sql/logins", "/api/v1/targets/dev-sql/databases/sales/access"} {
		if rec := doJSON(srv.h, http.MethodGet, path, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s without a password: status %d", path, rec.Code)
		}
	}
}

func TestAccessPlansPreviewAndErrors(t *testing.T) {
	doc := testConfig
	f := newAccessInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/grant", `{"database":"sales","login":"dora","level":"read","createLogin":true}`)
	var p PlanPreview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || !p.Ready || len(p.Plan.Actions) != 3 || p.Confirmation != "" {
		t.Fatalf("grant preview: status %d, body %s", rec.Code, rec.Body)
	}
	if p.Plan.Actions[0].ID != "create-login" || p.Plan.Actions[0].Statement != mssql.CreateLogin("dora", mssql.MaskedPassword) ||
		p.Request.Login != "dora" || !p.Request.CreateLogin {
		t.Fatalf("preview %+v", p)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/revoke", `{"database":"sales","login":"ana","remove":"user"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Confirmation != "ana" {
		t.Fatalf("revoke preview: status %d, body %s", rec.Code, rec.Body)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/fix-orphan", `{"database":"sales","user":"old"}`)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Ready {
		t.Fatalf("fix-orphan without the login: status %d, body %s", rec.Code, rec.Body)
	}
	// the password is not a field of a preview
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/grant", `{"database":"sales","login":"dora","level":"read","loginPassword":"Tr0ub4dor&3-xyz!"}`)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "Tr0ub4dor") {
		t.Fatalf("password in a preview: status %d, body %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"master":    `{"database":"sales","login":"admin","level":"read"}`,
		"sa":        `{"database":"sales","login":"sa","level":"read"}`,
		"bad level": `{"database":"sales","login":"ana","level":"root"}`,
		"bad name":  `{"database":"sales","login":"a;b","level":"read"}`,
		"remove":    `{"database":"sales","login":"ana","level":"read","remove":"user"}`,
	} {
		if rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/grant", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if len(f.ran) != 0 {
		t.Errorf("a preview ran %v", f.ran)
	}
}

// The password of a new login reaches the one statement that creates it, and
// no answer, event or journal entry of the engine.
func TestGrantCreateLoginPasswordSafety(t *testing.T) {
	const pw = "Tr0ub4dor&3-xyz!"
	doc := testConfig
	f := newAccessInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	grant := func(password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ExecuteRequest{Database: "sales", Login: "dora", Level: "readwrite", CreateLogin: true, LoginPassword: password})
		return doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/grant/execute", string(body))
	}
	for _, bad := range []string{"", "short1A!"} {
		rec := grant(bad)
		if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "short1A!") {
			t.Fatalf("password %q: status %d, body %s", bad, rec.Code, rec.Body)
		}
	}
	if len(f.ran) != 0 {
		t.Fatalf("something ran without a valid password: %v", f.ran)
	}
	rec := grant(pw)
	names, data := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "job step step step step step step step step done" {
		t.Fatalf("status %d, events %v\n%s", rec.Code, names, rec.Body)
	}
	if len(f.ran) != 4 || f.ran[0] != mssql.CreateLogin("dora", pw) {
		t.Fatalf("ran %v", f.ran)
	}
	var done PlanDone
	if json.Unmarshal([]byte(data[len(data)-1]), &done) != nil || !done.OK {
		t.Fatalf("done %s", data[len(data)-1])
	}
	// the repeated add-role keeps its plan id in the stream
	if !strings.Contains(rec.Body.String(), `"step":"add-role"`) || strings.Contains(rec.Body.String(), "#2") {
		t.Errorf("step ids in the stream:\n%s", rec.Body)
	}
	all := rec.Body.String()
	job, err := srv.jobs.Get(done.JobID)
	if err != nil {
		t.Fatal(err)
	}
	jj, _ := json.Marshal(job)
	all += string(jj)
	sub, err := srv.jobs.Subscribe(done.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range sub.Replay {
		b, _ := json.Marshal(ev)
		all += string(b)
	}
	sub.Close()
	if strings.Contains(all, pw) {
		t.Fatalf("the password is in the stream, job or events:\n%s", all)
	}
	if !strings.Contains(all, mssql.CreateLogin("dora", mssql.MaskedPassword)) {
		t.Errorf("the job lacks the masked statement:\n%s", string(jj))
	}
	// and not in the files of the journal, if the manager keeps one
	if dir, err := jobs.DefaultDir(); err == nil {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if data, _ := os.ReadFile(p); strings.Contains(string(data), pw) {
					t.Errorf("journal file %s has the password", p)
				}
			}
			return nil
		})
	}
}

func TestExecuteRevokeUserNeedsTheLogin(t *testing.T) {
	doc := testConfig
	f := newAccessInstance()
	srv := newOpsServer(t, &doc, testEnv, f, fakeObjects{})
	rec := doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/revoke/execute", `{"database":"sales","login":"ana","remove":"user","confirm":"sales"}`)
	if rec.Code != http.StatusUnprocessableEntity || len(f.ran) != 0 {
		t.Fatalf("wrong confirmation: status %d ran %v", rec.Code, f.ran)
	}
	rec = doJSON(srv.h, http.MethodPost, "/api/v1/targets/dev-sql/plans/revoke/execute", `{"database":"sales","login":"ana","remove":"user","confirm":"ana"}`)
	names, _ := events(t, rec.Body.String())
	if rec.Code != http.StatusOK || strings.Join(names, " ") != "job step step done" || len(f.ran) != 1 || !strings.Contains(f.ran[0], "DROP USER [ana]") {
		t.Fatalf("status %d, events %v, ran %v\n%s", rec.Code, names, f.ran, rec.Body)
	}
}
