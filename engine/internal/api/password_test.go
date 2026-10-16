package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/zalando/go-keyring"
)

// fake secret, only used by these tests
const fakePW = "fake-pw-1"

var promptConfig = strings.Replace(testConfig, `"env:TEST_SQL_PASSWORD"`, "prompt", 1)

func putPW(h http.Handler, query string, body string) *httptest.ResponseRecorder {
	return send(h, http.MethodPut, "/api/v1/targets/dev-sql/password"+query, body)
}

func targetPassword(t *testing.T, h http.Handler) PasswordInfo {
	t.Helper()
	var list TargetList
	rec := do(h, http.MethodGet, "/api/v1/targets")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Targets) != 1 {
		t.Fatalf("targets: %v %s", err, rec.Body)
	}
	return list.Targets[0].Password
}

func TestPasswordFlow(t *testing.T) {
	keyring.MockInit()
	doc := promptConfig
	fake := &fakeDatabases{}
	h := newDatabasesServer(t, &doc, nil, fake, fakeDescribe{})

	if got := targetPassword(t, h); got != (PasswordInfo{Source: "prompt", Available: false, CanRemember: true}) {
		t.Fatalf("before: %+v", got)
	}
	rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases")
	if rec.Code != http.StatusPreconditionRequired || rec.Header().Get("Dialga-Password-Target") != "dev-sql" ||
		strings.TrimSpace(rec.Body.String()) != "SQL password needed for target dev-sql" {
		t.Fatalf("428: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if rec := putPW(h, "", `{"password":"`+fakePW+`","remember":false}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if got := targetPassword(t, h); !got.Available {
		t.Fatalf("after put: %+v", got)
	}
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases"); rec.Code != http.StatusOK || fake.password != fakePW {
		t.Fatalf("databases: %d %s (password %q)", rec.Code, rec.Body, fake.password)
	}
	if _, err := keyring.Get("dialga", "dev-sql"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("remember=false reached the keyring: %v", err)
	}
	if rec := send(h, http.MethodDelete, "/api/v1/targets/dev-sql/password", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases"); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("after delete: %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, "/api/v1/targets/dev-sql/password", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete of nothing: %d", rec.Code)
	}
}

func TestPasswordRemember(t *testing.T) {
	keyring.MockInit()
	doc := promptConfig
	h := newDatabasesServer(t, &doc, nil, &fakeDatabases{}, fakeDescribe{})
	if rec := putPW(h, "", `{"password":"`+fakePW+`","remember":true}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if v, err := keyring.Get("dialga", "dev-sql"); err != nil || v != fakePW {
		t.Fatalf("keyring: %q %v", v, err)
	}
	// A new engine session finds it in the keyring.
	h2 := newDatabasesServer(t, &doc, nil, &fakeDatabases{}, fakeDescribe{})
	if got := targetPassword(t, h2); !got.Available {
		t.Fatalf("remembered: %+v", got)
	}
	if rec := do(h2, http.MethodGet, "/api/v1/targets/dev-sql/databases"); rec.Code != http.StatusOK {
		t.Fatalf("with the keyring entry: %d", rec.Code)
	}
	if rec := send(h2, http.MethodDelete, "/api/v1/targets/dev-sql/password", ""); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if _, err := keyring.Get("dialga", "dev-sql"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("delete left the entry: %v", err)
	}
}

func TestPasswordPutErrors(t *testing.T) {
	doc := promptConfig
	env := testConfig
	tests := []struct {
		name  string
		doc   *string
		path  string
		body  string
		noKey bool
		want  int
		text  string
	}{
		{"empty", &doc, "dev-sql", `{"password":"","remember":false}`, false, 400, ""},
		{"too long", &doc, "dev-sql", `{"password":"` + strings.Repeat("a", 1025) + `","remember":false}`, false, 400, ""},
		{"unknown field", &doc, "dev-sql", `{"password":"x","remember":false,"other":1}`, false, 400, ""},
		{"unknown target", &doc, "nope", `{"password":"x","remember":false}`, false, 404, ""},
		{"env source", &env, "dev-sql", `{"password":"x","remember":false}`, false, 409, "TEST_SQL_PASSWORD"},
		{"no keyring", &doc, "dev-sql", `{"password":"x","remember":true}`, true, 501, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noKey {
				keyring.MockInitWithError(errors.New("no service"))
				t.Cleanup(keyring.MockInit)
			} else {
				keyring.MockInit()
			}
			h := newDatabasesServer(t, tc.doc, nil, &fakeDatabases{}, fakeDescribe{})
			rec := send(h, http.MethodPut, "/api/v1/targets/"+tc.path+"/password", tc.body)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status %d (want %d), body %q", rec.Code, tc.want, rec.Body)
			}
			if strings.Contains(rec.Body.String(), `"x"`) {
				t.Fatal("the answer echoes the password")
			}
		})
	}
	// 501 stores nothing, not even in memory.
	keyring.MockInitWithError(errors.New("no service"))
	t.Cleanup(keyring.MockInit)
	h := newDatabasesServer(t, &doc, nil, &fakeDatabases{}, fakeDescribe{})
	putPW(h, "", `{"password":"x","remember":true}`)
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases"); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("a refused password was kept: %d", rec.Code)
	}
	// Without a keyring, memory only still works and the GUI is told.
	if rec := putPW(h, "", `{"password":"x","remember":false}`); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	if got := targetPassword(t, h); got.CanRemember || !got.Available {
		t.Fatalf("%+v", got)
	}
}

func TestPasswordVerify(t *testing.T) {
	keyring.MockInit()
	doc := promptConfig
	fake := &fakeDatabases{openErr: mssql.Error{Number: 18456, Message: "Login failed for user 'admin'."}}
	h := newDatabasesServer(t, &doc, nil, fake, fakeDescribe{})
	rec := putPW(h, "?verify=1", `{"password":"`+fakePW+`","remember":true}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Login failed") {
		t.Fatalf("bad login: %d %s", rec.Code, rec.Body)
	}
	if _, err := keyring.Get("dialga", "dev-sql"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("a failed login was stored: %v", err)
	}
	if rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/databases"); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("a failed login was kept in memory: %d", rec.Code)
	}
	fake.openErr = errors.New("nothing listening")
	if rec := putPW(h, "?verify=1", `{"password":"`+fakePW+`","remember":false}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable: %d", rec.Code)
	}
	fake.openErr = nil
	if rec := putPW(h, "?verify=1", `{"password":"`+fakePW+`","remember":true}`); rec.Code != http.StatusNoContent || fake.password != fakePW {
		t.Fatalf("good login: %d %s", rec.Code, rec.Body)
	}
	if v, _ := keyring.Get("dialga", "dev-sql"); v != fakePW {
		t.Fatal("verified password not stored")
	}
}

func TestPasswordNeededEverywhere(t *testing.T) {
	keyring.MockInit()
	doc := promptConfig
	h := newDatabasesServer(t, &doc, nil, &fakeDatabases{}, fakeDescribe{})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/targets/dev-sql/databases", ""},
		{"GET", "/api/v1/targets/dev-sql/tasks", ""},
		{"POST", "/api/v1/targets/dev-sql/plans/restore", `{"database":"sales","key":"sales.bak"}`},
		{"POST", "/api/v1/jobs", `{"target":"dev-sql","kind":"export","items":[{"database":"sales"}],"dryRun":true}`},
	} {
		rec := send(h, c.method, c.path, c.body)
		if rec.Code != http.StatusPreconditionRequired || rec.Header().Get("Dialga-Password-Target") != "dev-sql" {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	var o Overview
	rec := do(h, http.MethodGet, "/api/v1/targets/dev-sql/overview")
	if rec.Code == http.StatusOK {
		_ = json.Unmarshal(rec.Body.Bytes(), &o)
		if o.SQL != overviewSQLSkipped || !strings.Contains(o.SQLDetail, "SQL password needed") {
			t.Errorf("overview %+v", o)
		}
	}
}

func TestCheckStreamSaysThePasswordIsNeeded(t *testing.T) {
	keyring.MockInit()
	doc := promptConfig
	h, fake := newTestServer(t, &doc, nil)
	rec := send(h, http.MethodPost, "/api/v1/targets/dev-sql/checks", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SQL password needed for target dev-sql") || fake.password != "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCORSExposesThePasswordHeader(t *testing.T) {
	doc := promptConfig
	h := newDatabasesServer(t, &doc, nil, &fakeDatabases{}, fakeDescribe{})
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/targets/dev-sql/password", nil)
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Origin", "tauri://localhost")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("preflight: %v", rec.Header())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Host = "127.0.0.1:5000"
	req.Header.Set("Origin", "tauri://localhost")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Expose-Headers") != "Dialga-Password-Target" {
		t.Fatalf("headers: %v", rec.Header())
	}
}
