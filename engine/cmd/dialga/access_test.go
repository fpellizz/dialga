package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
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
	return mssql.DatabaseAccess{Users: f.users, WindowsPrincipals: 1}, nil
}

func newAccessInstance() *accessInstance {
	return &accessInstance{
		fakeInstance: &fakeInstance{states: map[string]string{"sales": "ONLINE"}},
		logins:       []mssql.Login{{Name: "admin", Master: true}, {Name: "ana", CreatedAt: "2026-10-01T09:00:00"}},
		users: []mssql.DatabaseUser{
			{Name: "ana", Login: "ana", Roles: []string{"db_datareader"}, Level: "read", Managed: true},
			{Name: "old", Orphan: true, Roles: []string{}, Level: "none", Managed: true},
		},
	}
}

func withAccess(a *app, f *accessInstance) *bool {
	opened := withOps(a, f.fakeInstance, fakeObjects{})
	a.openOps = func(_ context.Context, name string, t config.Target, _ string) (*ops.Session, func(), error) {
		*opened = true
		return &ops.Session{Name: name, Target: t, SQL: f}, func() {}, nil
	}
	return opened
}

func TestAccessListAndLogins(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	withAccess(a, newAccessInstance())
	code, out, _ := run(a, "access", "list", "sales")
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	for _, want := range []string{"Users of sales on dev-sql", "ana", "read", "orphan: no login", "1 Windows user"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = run(a, "access", "list", "sales", "-o", "json")
	var v ops.AccessView
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || len(v.Users) != 2 || v.Database != "sales" {
		t.Fatalf("json: code %d %v %+v", code, err, v)
	}
	if code, _, errOut := run(a, "access", "list", "nope"); code != exitCheckFailed || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing db: code %d %q", code, errOut)
	}
	code, out, _ = run(a, "access", "logins")
	if code != 0 || !strings.Contains(out, "master user") || !strings.Contains(out, "2026-10-01T09:00:00") {
		t.Fatalf("logins: code %d\n%s", code, out)
	}
}

func TestAccessGrantDryRunNeedsNoPassword(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := newAccessInstance()
	withAccess(a, f)
	code, out, errOut := run(a, "access", "grant", "sales", "dora", "--level", "read", "--create-login", "--generate-password")
	if code != 0 || len(f.ran) != 0 {
		t.Fatalf("code %d ran %v\n%s", code, f.ran, out)
	}
	if !strings.Contains(out, "CREATE LOGIN [dora] WITH PASSWORD = N'****'") || strings.Contains(out+errOut, "Password of login") {
		t.Fatalf("dry run output:\n%s%s", out, errOut)
	}
}

func TestAccessGrantApplyKeepsThePasswordOut(t *testing.T) {
	const pw = "Tr0ub4dor&3-xyz!"
	a, _, _ := testApp(t, testConfig)
	a.stdin = strings.NewReader(pw + "\n")
	f := newAccessInstance()
	withAccess(a, f)
	code, out, errOut := run(a, "access", "grant", "sales", "dora", "--level", "readwrite", "--create-login", "--password-stdin", "--apply", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d\n%s%s", code, out, errOut)
	}
	if len(f.ran) != 4 || f.ran[0] != mssql.CreateLogin("dora", pw) {
		t.Fatalf("ran %v", f.ran)
	}
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || !doc.Applied || doc.JobID == "" || doc.GeneratedPassword != "" {
		t.Fatalf("doc %+v err %v", doc, err)
	}
	if strings.Contains(out+errOut, pw) {
		t.Fatalf("the password is in the output:\n%s%s", out, errOut)
	}
	// nor in the journal of the job
	dir, _ := jobs.DefaultDir()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), pw) {
				t.Errorf("the journal file %s has the password", p)
			}
		}
		return nil
	})
}

func TestAccessGrantGeneratedPasswordShownOnce(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := newAccessInstance()
	withAccess(a, f)
	code, out, errOut := run(a, "access", "grant", "sales", "dora", "--level", "read", "--create-login", "--generate-password", "--apply")
	if code != 0 || len(f.ran) != 3 {
		t.Fatalf("code %d ran %v\n%s%s", code, f.ran, out, errOut)
	}
	pw := strings.TrimPrefix(f.ran[0], "CREATE LOGIN [dora] WITH PASSWORD = N'")
	pw, _, _ = strings.Cut(pw, "'")
	if len(pw) != 20 || ops.CheckLoginPassword(pw) != nil {
		t.Fatalf("generated password %q", pw)
	}
	if strings.Count(errOut, pw) != 1 || strings.Contains(out, pw) {
		t.Fatalf("password must be on stderr once, not stdout:\nout %s\nerr %s", out, errOut)
	}
	// JSON: in the result, not on stderr
	a, _, _ = testApp(t, testConfig)
	f = newAccessInstance()
	withAccess(a, f)
	code, out, errOut = run(a, "access", "grant", "sales", "dora", "--level", "read", "--create-login", "--generate-password", "--apply", "-o", "json")
	var doc opResult
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || len(doc.GeneratedPassword) != 20 || strings.Contains(errOut, doc.GeneratedPassword) {
		t.Fatalf("json: code %d %v %+v\n%s", code, err, doc, errOut)
	}
}

func TestAccessPasswordFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"create without a password": {"grant", "sales", "dora", "--level", "read", "--create-login", "--apply"},
		"both":                      {"grant", "sales", "dora", "--level", "read", "--create-login", "--generate-password", "--password-stdin", "--apply"},
		"without create-login":      {"grant", "sales", "ana", "--level", "read", "--generate-password"},
		"weak stdin":                {"grant", "sales", "dora", "--level", "read", "--create-login", "--password-stdin", "--apply"},
		"no level":                  {"grant", "sales", "ana"},
		"bad level":                 {"grant", "sales", "ana", "--level", "root"},
		"master":                    {"grant", "sales", "admin", "--level", "read"},
		"revoke no remove":          {"revoke", "sales", "ana"},
	} {
		a, _, _ := testApp(t, testConfig)
		a.stdin = strings.NewReader("weak\n")
		f := newAccessInstance()
		opened := withAccess(a, f)
		if code, _, _ := run(a, append([]string{"access"}, args...)...); code != exitFailure || *opened || len(f.ran) != 0 {
			t.Errorf("%s: code %d opened %v ran %v", name, code, *opened, f.ran)
		}
	}
}

func TestAccessRevokeUserConfirmsTheLogin(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := newAccessInstance()
	f.logins = append(f.logins, mssql.Login{Name: "ana"})
	withAccess(a, f)
	if code, _, errOut := run(a, "access", "revoke", "sales", "ana", "--remove", "user", "--apply"); code != exitFailure || len(f.ran) != 0 || !strings.Contains(errOut, "pass --confirm") {
		t.Fatalf("no confirm: code %d ran %v %q", code, f.ran, errOut)
	}
	if code, out, _ := run(a, "access", "revoke", "sales", "ana", "--remove", "user", "--apply", "--confirm", "ana"); code != 0 || len(f.ran) != 1 || !strings.Contains(f.ran[0], "DROP USER [ana]") {
		t.Fatalf("confirmed: code %d ran %v\n%s", code, f.ran, out)
	}
}

func TestAccessFixOrphan(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	f := newAccessInstance()
	f.logins = append(f.logins, mssql.Login{Name: "old"})
	withAccess(a, f)
	if code, out, _ := run(a, "access", "fix-orphan", "sales", "old"); code != 0 || len(f.ran) != 0 || !strings.Contains(out, "ALTER USER [old] WITH LOGIN = [old]") {
		t.Fatalf("dry run: code %d ran %v\n%s", code, f.ran, out)
	}
	if code, _, _ := run(a, "access", "fix-orphan", "sales", "old", "--apply"); code != 0 || len(f.ran) != 1 {
		t.Fatalf("apply: code %d ran %v", code, f.ran)
	}
	if code, _, _ := run(a, "access", "fix-orphan", "sales", "ana"); code != exitCheckFailed {
		t.Fatalf("not an orphan: code %d", code)
	}
}
