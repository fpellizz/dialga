package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

func (f *fakeSQL) Logins(context.Context, string) ([]mssql.Login, error) { return f.logins, f.accessEr }

func (f *fakeSQL) DatabaseAccess(_ context.Context, db, _ string) (mssql.DatabaseAccess, error) {
	return f.access[db], f.accessEr
}

func (f *fakeSQL) MsdbUser(_ context.Context, login string) (mssql.MsdbUser, error) {
	return f.msdb[login], f.accessEr
}

func (f *fakeSQL) BackupOperators(context.Context, string) ([]string, []string, error) {
	return f.used, f.unread, f.accessEr
}

func (f *fakeSQL) UserSchemas(_ context.Context, _, user string) ([]string, error) {
	return f.schemas[user], f.accessEr
}

const goodPassword = "Tr0ub4dor&3-xyz!"

func user(name, login string, roles ...string) mssql.DatabaseUser {
	return mssql.DatabaseUser{Name: name, Login: login, Roles: append([]string{}, roles...),
		Level: mssql.AccessLevel(roles), Managed: true}
}

// accessFake: sales is online with ana (read), bea (readwrite, backup in
// msdb too), an orphan "old" and dbo; logins ana, bea, carl, old2.
func accessFake() *fakeSQL {
	bea := user("bea", "bea", "db_datareader", "db_datawriter", "db_backupoperator")
	bea.Backup = true
	dbo := user("dbo", "admin")
	dbo.Managed = false
	old := user("old", "")
	old.Orphan = true
	return &fakeSQL{
		states: map[string]string{"sales": "ONLINE", "off": "RESTORING"},
		logins: []mssql.Login{{Name: "admin", Master: true}, {Name: "ana"}, {Name: "bea"}, {Name: "carl"}, {Name: "old"}},
		access: map[string]mssql.DatabaseAccess{"sales": {Users: []mssql.DatabaseUser{
			dbo, user("ana", "ana", "db_datareader"), bea, old}}},
		msdb: map[string]mssql.MsdbUser{"bea": {Exists: true, Name: "bea", Grants: 3}},
	}
}

func ids(p *Prepared) string {
	var out []string
	for _, a := range p.Plan.Actions {
		out = append(out, a.ID)
	}
	return strings.Join(out, ",")
}

func statements(p *Prepared) string {
	var out []string
	for _, a := range p.Plan.Actions {
		out = append(out, a.Statement)
	}
	return strings.Join(out, "\n")
}

func TestValidateAccess(t *testing.T) {
	tests := []struct {
		name string
		op   string
		p    Params
		want string
	}{
		{"grant", OpGrant, Params{Database: "sales", Login: "ana.x", Level: "read"}, ""},
		{"no level", OpGrant, Params{Database: "sales", Login: "ana"}, "invalid level"},
		{"master", OpGrant, Params{Database: "sales", Login: "Admin", Level: "read"}, "master user"},
		{"sa", OpGrant, Params{Database: "sales", Login: "SA", Level: "read"}, "never changes"},
		{"rdsa", OpRevoke, Params{Database: "sales", Login: "rdsa", Remove: "user"}, "never changes"},
		{"hash login", OpGrant, Params{Database: "sales", Login: "##MS_x##", Level: "owner"}, "invalid login"},
		{"bad name", OpGrant, Params{Database: "sales", Login: "a b", Level: "read"}, "invalid login"},
		{"system db", OpGrant, Params{Database: "master", Login: "ana", Level: "read"}, "system database"},
		{"revoke", OpRevoke, Params{Database: "sales", Login: "ana", Remove: "user"}, ""},
		{"revoke remove", OpRevoke, Params{Database: "sales", Login: "ana", Remove: "all"}, "invalid remove"},
		{"revoke level", OpRevoke, Params{Database: "sales", Login: "ana", Remove: "level", Level: "read"}, "level is for grant"},
		{"fix", OpFixOrphan, Params{Database: "sales", User: "old"}, ""},
		{"fix dbo", OpFixOrphan, Params{Database: "sales", User: "dbo"}, "never changes"},
		{"fix login", OpFixOrphan, Params{Database: "sales", User: "old", Login: "sa"}, "never changes"},
		{"fix no user", OpFixOrphan, Params{Database: "sales"}, "invalid login or user"},
		{"restore with login", OpDrop, Params{Database: "sales", Login: "ana"}, "are for grant"},
		{"grant with key", OpGrant, Params{Database: "sales", Login: "ana", Level: "read", Key: "x"}, "not a backup key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.op, tt.p, testTarget)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckLoginPassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		goodPassword: true, "": false, "Short1!": false, "alllowercaseonly": false, "lower and UPPER ok": true,
		"lower1234567890": false, "LOWER1234567890": false, "Lower1234567890": true, strings.Repeat("aA1", 50): false,
	} {
		err := CheckLoginPassword(pw)
		if (err == nil) != ok {
			t.Errorf("CheckLoginPassword(%q) = %v, want ok=%v", pw, err, ok)
		}
		if err != nil && pw != "" && strings.Contains(err.Error(), pw) {
			t.Errorf("the error quotes the password: %v", err)
		}
		if err != nil && !errors.Is(err, ErrLoginPassword) {
			t.Errorf("error %v is not ErrLoginPassword", err)
		}
	}
}

func TestGrantNewUserAndBackup(t *testing.T) {
	f := accessFake()
	f.msdb = nil
	s := newSession(f, nil)
	p, err := s.Grant(context.Background(), Params{Database: "sales", Login: "carl", Level: "readwrite", Backup: true})
	if err != nil || !p.Ready() {
		t.Fatalf("Grant = %v ready %v checks %v", err, p.Ready(), p.Checks)
	}
	if got, want := ids(p), "create-user,add-role,add-role,add-role,msdb-user,backup-grants"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}
	if p.Plan.Confirmation() != "" {
		t.Errorf("confirmation = %q", p.Plan.Confirmation())
	}
	st := statements(p)
	for _, want := range []string{mssql.CreateUser("sales", "carl"), mssql.AddRole("sales", "db_datawriter", "carl"),
		mssql.AddRole("sales", "db_backupoperator", "carl"), mssql.CreateUser("msdb", "carl"), mssql.BackupGrants("carl")} {
		if !strings.Contains(st, want) {
			t.Errorf("plan lacks %q", want)
		}
	}
	if len(f.ran) != 0 {
		t.Errorf("the plan ran statements: %v", f.ran)
	}
	// Execute runs them in order.
	if _, err := p.Execute(context.Background(), ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 6 {
		t.Fatalf("ran %d statements, want 6: %v", len(f.ran), f.ran)
	}
}

func TestGrantMovesLevelAndIsIdempotent(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	// ana: read -> owner drops db_datareader, adds db_owner.
	p, _ := s.Grant(context.Background(), Params{Database: "sales", Login: "ana", Level: "owner"})
	if got, want := ids(p), "drop-role,add-role"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}
	// bea already readwrite + backup: nothing to do, a notice.
	p, _ = s.Grant(context.Background(), Params{Database: "sales", Login: "bea", Level: "readwrite", Backup: true})
	if len(p.Plan.Actions) != 0 || !p.Ready() || !strings.Contains(verdicts(p), "access:warning") {
		t.Fatalf("idempotent grant: actions %d ready %v checks %s", len(p.Plan.Actions), p.Ready(), verdicts(p))
	}
	// a user with the backup role but one missing msdb grant gets only the grants.
	f.msdb["bea"] = mssql.MsdbUser{Exists: true, Name: "bea", Grants: 2}
	p, _ = s.Grant(context.Background(), Params{Database: "sales", Login: "bea", Level: "readwrite", Backup: true})
	if got := ids(p); got != "backup-grants" {
		t.Fatalf("ids = %s, want backup-grants", got)
	}
}

func TestGrantRefusals(t *testing.T) {
	tests := []struct {
		name string
		in   Params
		want string // the failed check and part of its detail
	}{
		{"no login", Params{Database: "sales", Login: "ghost", Level: "read"}, "login:ghost does not exist"},
		{"login exists", Params{Database: "sales", Login: "ana", Level: "read", CreateLogin: true}, "login:ana already exists"},
		{"offline", Params{Database: "off", Login: "ana", Level: "read"}, "database:off is RESTORING"},
		{"missing db", Params{Database: "nope", Login: "ana", Level: "read"}, "database:nope not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := newSession(accessFake(), nil).Grant(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Ready() || len(p.Plan.Actions) != 0 {
				t.Fatalf("ready %v actions %d", p.Ready(), len(p.Plan.Actions))
			}
			var failed []string
			for _, c := range p.Checks {
				if c.Status == "failed" {
					failed = append(failed, c.Step+":"+c.Detail)
				}
			}
			if !strings.Contains(strings.Join(failed, "|"), tt.want) {
				t.Fatalf("failed checks %v lack %q", failed, tt.want)
			}
		})
	}
	// a user of the same name bound to another login
	f := accessFake()
	f.access["sales"] = mssql.DatabaseAccess{Users: append(f.access["sales"].Users, user("carl", "ana"))}
	p, _ := newSession(f, nil).Grant(context.Background(), Params{Database: "sales", Login: "carl", Level: "read"})
	if p.Ready() || !strings.Contains(verdicts(p), "user:failed") {
		t.Fatalf("user bound to another login: %s", verdicts(p))
	}
}

// An orphan of the same name (restored from another instance) is bound to
// the login, created here or existing, and keeps its roles.
func TestGrantBindsOrphanOfTheSameName(t *testing.T) {
	f := accessFake()
	lost := user("lost", "", "db_datawriter")
	lost.Orphan = true
	f.access["sales"] = mssql.DatabaseAccess{Users: append(f.access["sales"].Users, lost)}
	s := newSession(f, nil)

	p, err := s.Grant(context.Background(), Params{Database: "sales", Login: "lost", Level: "readwrite", CreateLogin: true})
	if err != nil || !p.Ready() {
		t.Fatalf("Grant = %v ready %v: %s", err, p.Ready(), verdicts(p))
	}
	if got := ids(p); got != "create-login,map-user,add-role" {
		t.Fatalf("ids = %s, want create-login,map-user,add-role (db_datawriter kept)", got)
	}

	p, err = s.Grant(context.Background(), Params{Database: "sales", Login: "old", Level: "read"})
	if err != nil || !p.Ready() {
		t.Fatalf("Grant = %v ready %v: %s", err, p.Ready(), verdicts(p))
	}
	if got := ids(p); got != "map-user,add-role" {
		t.Fatalf("ids = %s, want map-user,add-role", got)
	}
}

// The password reaches the one statement that runs and nowhere else.
func TestGrantCreateLoginKeepsThePasswordOut(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	p, err := s.Grant(context.Background(), Params{Database: "sales", Login: "dora", Level: "read", CreateLogin: true})
	if err != nil || !p.Ready() {
		t.Fatalf("Grant = %v ready %v", err, p.Ready())
	}
	if !p.NeedsLoginPassword() || ids(p) != "create-login,create-user,add-role" {
		t.Fatalf("ids %s needs %v", ids(p), p.NeedsLoginPassword())
	}
	var preview strings.Builder
	_ = p.Plan.Preview(&preview)
	planJSON, _ := json.Marshal(p.Plan)
	checks, _ := json.Marshal(p.Checks)
	digest := p.Digest()
	for name, text := range map[string]string{"preview": preview.String(), "plan": string(planJSON), "checks": string(checks)} {
		if strings.Contains(text, goodPassword) {
			t.Errorf("%s has the password", name)
		}
	}
	if !strings.Contains(preview.String(), mssql.CreateLogin("dora", mssql.MaskedPassword)) {
		t.Errorf("preview lacks the masked statement:\n%s", preview.String())
	}
	// Execute needs it and checks the policy before anything runs.
	for _, bad := range []string{"", "short"} {
		if _, err := p.Execute(context.Background(), ExecOptions{LoginPassword: bad}); !errors.Is(err, ErrLoginPassword) {
			t.Fatalf("password %q: %v", bad, err)
		}
	}
	if len(f.ran) != 0 {
		t.Fatalf("something ran without a valid password: %v", f.ran)
	}
	var events []core.Event
	if _, err := p.Execute(context.Background(), ExecOptions{LoginPassword: goodPassword, OnStep: func(e core.Event) { events = append(events, e) }}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 3 || f.ran[0] != mssql.CreateLogin("dora", goodPassword) {
		t.Fatalf("ran %v", f.ran)
	}
	for _, s := range f.ran[1:] {
		if strings.Contains(s, goodPassword) {
			t.Errorf("another statement has the password: %s", s)
		}
	}
	if ev, _ := json.Marshal(events); strings.Contains(string(ev), goodPassword) {
		t.Errorf("events have the password: %s", ev)
	}
	if p.Digest() != digest || p.loginPassword != "" {
		t.Errorf("digest changed or the password was kept (%q)", p.loginPassword)
	}
}

func TestCreateLoginErrorIsScrubbed(t *testing.T) {
	f := accessFake()
	f.execErr = errors.New("Msg 15118: password " + goodPassword + " rejected")
	s := newSession(f, nil)
	_, err := s.RunAccessStep(context.Background(), AccessStep{ID: AccCreateLogin, Login: "dora"}, goodPassword)
	if err == nil || strings.Contains(err.Error(), goodPassword) || !strings.Contains(err.Error(), mssql.MaskedPassword) {
		t.Fatalf("error = %v", err)
	}
	// Without the password (a resumed job) it fails without touching SQL, unless the login is there.
	f2 := accessFake()
	if _, err := newSession(f2, nil).RunAccessStep(context.Background(), AccessStep{ID: AccCreateLogin, Login: "dora"}, ""); !errors.Is(err, ErrLoginPassword) || len(f2.ran) != 0 {
		t.Fatalf("no password: %v ran %v", err, f2.ran)
	}
	if d, err := newSession(f2, nil).RunAccessStep(context.Background(), AccessStep{ID: AccCreateLogin, Login: "ana"}, ""); err != nil || !strings.Contains(d, "already exists") {
		t.Fatalf("existing login: %q %v", d, err)
	}
}

func TestRevokeLevelAndBackup(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	p, _ := s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveLevel})
	if got := ids(p); got != "drop-role,drop-role" {
		t.Fatalf("level ids = %s", got)
	}
	// backup, nobody else uses it: role, revokes, msdb user dropped
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveBackup})
	if got, want := ids(p), "drop-role,backup-revokes,msdb-drop-user"; got != want {
		t.Fatalf("backup ids = %s, want %s (%v)", got, want, p.Checks)
	}
	// another database still uses it: only the role
	f.used = []string{"sales", "hr"}
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveBackup})
	if got := ids(p); got != "drop-role" || !strings.Contains(verdicts(p), "msdb:warning") {
		t.Fatalf("shared backup ids = %s checks %s", got, verdicts(p))
	}
	// unreadable databases: be conservative
	f.used, f.unread = nil, []string{"sa_only"}
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveBackup})
	if got := ids(p); got != "drop-role" || !strings.Contains(verdicts(p), "msdb:warning") {
		t.Fatalf("unreadable ids = %s checks %s", got, verdicts(p))
	}
	// the msdb user has other permissions: revoke the grants, keep the user
	f.unread = nil
	f.msdb["bea"] = mssql.MsdbUser{Exists: true, Name: "bea", Grants: 3, Others: 1}
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveBackup})
	if got := ids(p); got != "drop-role,backup-revokes" {
		t.Fatalf("others ids = %s", got)
	}
	// nothing to do
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "ana", Remove: RemoveBackup})
	if len(p.Plan.Actions) != 0 || !p.Ready() {
		t.Fatalf("ana backup: %v %s", ids(p), verdicts(p))
	}
}

func TestRevokeUser(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	p, _ := s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveUser})
	if got, want := ids(p), "drop-user,backup-revokes,msdb-drop-user"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}
	if p.Plan.Confirmation() != "bea" || p.Plan.Actions[0].Risk != core.Destructive {
		t.Fatalf("confirmation %q risk %s", p.Plan.Confirmation(), p.Plan.Actions[0].Risk)
	}
	s.Target.Protected = true
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "bea", Remove: RemoveUser})
	if p.Plan.Confirmation() != "dev-sql" {
		t.Fatalf("protected confirmation = %q", p.Plan.Confirmation())
	}
	s.Target.Protected = false
	p.Plan.Protected = false
	// a user that owns a schema cannot be dropped
	f.schemas = map[string][]string{"ana": {"reporting", "stage"}}
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "ana", Remove: RemoveUser})
	if p.Ready() || !strings.Contains(p.Checks[len(p.Checks)-1].Detail, "reporting, stage") {
		t.Fatalf("schemas: ready %v checks %v", p.Ready(), p.Checks)
	}
	// no user of that login
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "carl", Remove: RemoveUser})
	if p.Ready() {
		t.Fatal("revoke of a login without a user is ready")
	}
	// the dbo user is never touched
	p, _ = s.Revoke(context.Background(), Params{Database: "sales", Login: "admin2", Remove: RemoveLevel})
	if p.Ready() {
		t.Fatal("revoke without a user is ready")
	}
}

func TestFixOrphan(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	p, err := s.FixOrphan(context.Background(), Params{Database: "sales", User: "old"})
	if err != nil || !p.Ready() || ids(p) != "map-user" || p.Plan.Actions[0].Statement != mssql.MapUser("sales", "old", "old") {
		t.Fatalf("FixOrphan = %v ready %v %s %s", err, p.Ready(), ids(p), statements(p))
	}
	if p.Params.Login != "old" {
		t.Errorf("login default = %q", p.Params.Login)
	}
	p, _ = s.FixOrphan(context.Background(), Params{Database: "sales", User: "old", Login: "carl"})
	if !p.Ready() || p.Plan.Actions[0].Statement != mssql.MapUser("sales", "old", "carl") {
		t.Fatalf("with login: %v", p.Checks)
	}
	for name, in := range map[string]Params{
		"not orphan":    {Database: "sales", User: "ana"},
		"no user":       {Database: "sales", User: "zed"},
		"login missing": {Database: "sales", User: "old", Login: "ghost"},
		"login bound":   {Database: "sales", User: "old", Login: "ana"},
	} {
		if p, _ := s.FixOrphan(context.Background(), in); p.Ready() || len(p.Plan.Actions) != 0 {
			t.Errorf("%s: ready %v actions %d", name, p.Ready(), len(p.Plan.Actions))
		}
	}
}

func TestAccessStepsAreIdempotent(t *testing.T) {
	f := accessFake()
	s := newSession(f, nil)
	for _, st := range []AccessStep{
		{ID: AccCreateUser, Database: "sales", Login: "ana"},
		{ID: AccAddRole, Database: "sales", User: "ana", Role: "db_datareader"},
		{ID: AccDropRole, Database: "sales", User: "ana", Role: "db_owner"},
		{ID: AccDropUser, Database: "sales", User: "ghost"},
		{ID: AccMsdbUser, Login: "bea"},
		{ID: AccMapUser, Database: "sales", User: "ana", Login: "ana"},
	} {
		if d, err := s.RunAccessStep(context.Background(), st, ""); err != nil || d == "" {
			t.Errorf("%s: %q %v", st.ID, d, err)
		}
	}
	if len(f.ran) != 0 {
		t.Errorf("no-ops ran statements: %v", f.ran)
	}
	// a user of that name bound to another login is a clash, not a no-op
	if _, err := s.RunAccessStep(context.Background(), AccessStep{ID: AccCreateUser, Database: "sales", Login: "bea2"}, ""); err != nil {
		t.Errorf("create-user of a free name: %v", err)
	}
	f.access["sales"] = mssql.DatabaseAccess{Users: append(f.access["sales"].Users, user("carl", "ana"))}
	if _, err := s.RunAccessStep(context.Background(), AccessStep{ID: AccCreateUser, Database: "sales", Login: "carl"}, ""); err == nil {
		t.Error("create-user over a user of another login succeeded")
	}
}
