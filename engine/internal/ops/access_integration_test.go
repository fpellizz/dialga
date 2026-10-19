//go:build integration

package ops

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// accessMock gives god what the RDS master user has for these operations
// (login management, users and grants in msdb) and the rds_task_status of
// the backup permissions; the mock of mock-rds.sql has none of it.
const accessMock = `GRANT ALTER ANY LOGIN TO god;
GO
USE msdb;
GO
IF OBJECT_ID(N'dbo.rds_task_status') IS NULL EXEC (N'CREATE PROCEDURE dbo.rds_task_status @db_name sysname = NULL, @task_id int = NULL AS SELECT 1');
GRANT ALTER ANY USER TO god;
GRANT VIEW DEFINITION TO god;
GRANT EXECUTE ON dbo.rds_backup_database TO god WITH GRANT OPTION;
GRANT EXECUTE ON dbo.rds_task_status TO god WITH GRANT OPTION;
GRANT SELECT ON dbo.rds_fn_task_status TO god WITH GRANT OPTION;
GO
USE master;
GO`

// bestEffortAccess is the real SQL session, except that databases god cannot
// read do not hold back the msdb cleanup (the mock instance has some that
// belong to other tests).
type bestEffortAccess struct{ mssql.Session }

func (b bestEffortAccess) BackupOperators(ctx context.Context, login string) ([]string, []string, error) {
	used, _, err := b.Session.BackupOperators(ctx, login)
	return used, nil, err
}

func TestIntegrationAccess(t *testing.T) {
	ctx := context.Background()
	if err := runScript(ctx, sa, accessMock); err != nil {
		t.Fatal(err)
	}
	// acc_db belongs to god, like a database restored by the master user.
	for _, stmt := range []string{`DROP DATABASE IF EXISTS acc_db`, `CREATE DATABASE acc_db`} {
		if _, err := god.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = god.Exec(`DROP DATABASE IF EXISTS acc_db`)
		for _, l := range []string{"acc_dora", "acc_orph", "acc_orph2"} {
			_, _ = sa.Exec(`IF SUSER_ID(N'` + l + `') IS NOT NULL DROP LOGIN ` + l)
		}
		_ = runScript(ctx, sa, "USE msdb;\nGO\nIF USER_ID(N'acc_dora') IS NOT NULL DROP USER acc_dora\nGO")
	})
	reads := mssql.Session{DB: god}
	s := goldenSession(nil)
	s.SQL = bestEffortAccess{mssql.Session{DB: god}}
	run := func(p *Prepared, err error, password string) {
		t.Helper()
		if err != nil || !p.Ready() {
			t.Fatalf("%s: err %v checks %+v", p.Plan.Operation, err, p.Checks)
		}
		if _, err := p.Execute(ctx, ExecOptions{LoginPassword: password, Confirm: p.Plan.Confirmation()}); err != nil {
			t.Fatalf("%s: %v", p.Plan.Operation, err)
		}
	}
	user := func(db, name string) *mssql.DatabaseUser {
		t.Helper()
		a, err := reads.DatabaseAccess(ctx, db, "god")
		if err != nil {
			t.Fatal(err)
		}
		return findUser(a.Users, name)
	}

	// grant: a new login with readwrite and backup, as the master user would.
	const password = "Acc-Dora-9xQ!zt7"
	p, err := s.Grant(ctx, Params{Database: "acc_db", Login: "acc_dora", Level: "readwrite", Backup: true, CreateLogin: true})
	if err == nil && strings.Contains(statements(p), password) {
		t.Fatal("the plan has the password")
	}
	run(p, err, password)
	u := user("acc_db", "acc_dora")
	if u == nil || u.Orphan || u.Login != "acc_dora" || u.Level != "readwrite" || !u.Backup || !slices.Contains(u.Roles, "db_backupoperator") {
		t.Fatalf("after grant: %+v", u)
	}
	if m, err := reads.MsdbUser(ctx, "acc_dora"); err != nil || !m.Exists || m.Grants != 3 || m.Others != 0 {
		t.Fatalf("msdb user %+v, %v", m, err)
	}
	// the new login can really connect with the password it was created with
	var one int
	if err := openAs(t, "acc_dora", password).QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the new login cannot connect: %v", err)
	}
	// idempotent: a second grant has nothing to do
	if p, err := s.Grant(ctx, Params{Database: "acc_db", Login: "acc_dora", Level: "readwrite", Backup: true}); err != nil || len(p.Plan.Actions) != 0 {
		t.Fatalf("second grant: %v %v", err, ids(p))
	}
	// down to read, a level change
	p, err = s.Grant(ctx, Params{Database: "acc_db", Login: "acc_dora", Level: "read"})
	if got := ids(p); got != "drop-role" {
		t.Fatalf("read after readwrite: %s", got)
	}
	run(p, err, "")
	if u := user("acc_db", "acc_dora"); u.Level != "read" || !u.Backup {
		t.Fatalf("after level change: %+v", u)
	}

	// the conservative path: a database god cannot read holds the msdb grants back
	strict := goldenSession(nil)
	p, err = strict.Revoke(ctx, Params{Database: "acc_db", Login: "acc_dora", Remove: RemoveBackup})
	if err != nil || ids(p) != "drop-role" || !strings.Contains(verdicts(p), "msdb:warning") {
		t.Fatalf("strict revoke backup: %v ids %s checks %s", err, ids(p), verdicts(p))
	}

	// revoke backup: the role, the msdb permissions and the msdb user go
	p, err = s.Revoke(ctx, Params{Database: "acc_db", Login: "acc_dora", Remove: RemoveBackup})
	if got := ids(p); got != "drop-role,backup-revokes,msdb-drop-user" {
		t.Fatalf("revoke backup: %s (%+v)", got, p.Checks)
	}
	run(p, err, "")
	if u := user("acc_db", "acc_dora"); u.Backup || u.BackupPartial || u.Level != "read" {
		t.Fatalf("after revoke backup: %+v", u)
	}
	if m, _ := reads.MsdbUser(ctx, "acc_dora"); m.Exists {
		t.Fatalf("msdb user is still there: %+v", m)
	}
	// revoke level, then the user; the login stays
	p, err = s.Revoke(ctx, Params{Database: "acc_db", Login: "acc_dora", Remove: RemoveLevel})
	run(p, err, "")
	if u := user("acc_db", "acc_dora"); u.Level != "none" {
		t.Fatalf("after revoke level: %+v", u)
	}
	p, err = s.Revoke(ctx, Params{Database: "acc_db", Login: "acc_dora", Remove: RemoveUser})
	if err != nil || p.Plan.Confirmation() != "acc_dora" || p.Plan.Actions[0].Risk != core.Destructive {
		t.Fatalf("revoke user plan: %v %q", err, p.Plan.Confirmation())
	}
	run(p, err, "")
	if user("acc_db", "acc_dora") != nil {
		t.Fatal("the user is still in acc_db")
	}
	if logins, err := s.Logins(ctx); err != nil || !slices.ContainsFunc(logins, func(l mssql.Login) bool { return l.Name == "acc_dora" }) {
		t.Fatalf("the login must stay: %v %v", logins, err)
	}

	// fix-orphan: a user whose login was dropped, bound to another login
	for _, stmt := range []string{`CREATE LOGIN acc_orph WITH PASSWORD = 'Orph-1-xYz-987!', CHECK_POLICY = OFF`,
		`CREATE LOGIN acc_orph2 WITH PASSWORD = 'Orph-2-xYz-987!', CHECK_POLICY = OFF`} {
		if _, err := sa.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := god.ExecContext(ctx, `USE acc_db; CREATE USER acc_orph FOR LOGIN acc_orph;`); err != nil {
		t.Fatal(err)
	}
	if _, err := sa.ExecContext(ctx, `DROP LOGIN acc_orph`); err != nil {
		t.Fatal(err)
	}
	if u := user("acc_db", "acc_orph"); u == nil || !u.Orphan {
		t.Fatalf("expected an orphan: %+v", u)
	}
	p, err = s.FixOrphan(ctx, Params{Database: "acc_db", User: "acc_orph", Login: "acc_orph2"})
	run(p, err, "")
	if u := user("acc_db", "acc_orph"); u.Orphan || u.Login != "acc_orph2" {
		t.Fatalf("after fix-orphan: %+v", u)
	}
}

// openAs connects as a login the test created.
func openAs(t *testing.T, login, password string) *sql.DB {
	t.Helper()
	db, err := openPlain(context.Background(), instanceAddr, login, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
