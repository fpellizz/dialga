package mssql

import "strings"

// The statements that give a SQL login access to a database, and take it
// back. They have no counterpart in mssql-import.sh: the golden files
// (testdata/golden/access-*.sql) are written by hand. Like tsql.go they only
// format: callers validate the names first (package validate).

// MaskedPassword stands for the password of a new login in every statement
// that is shown, journaled or hashed into a digest; only the statement that
// runs carries the real one.
const MaskedPassword = "****"

// The roles a level of access is made of, in the order they are granted.
var (
	RolesRead      = []string{"db_datareader"}
	RolesReadWrite = []string{"db_datareader", "db_datawriter"}
	RolesOwner     = []string{"db_owner"}
	// RoleBackup lets a user back the database up (with the msdb grants of
	// BackupGrants for rds_backup_database).
	RoleBackup = "db_backupoperator"
)

// BackupObjects are the msdb objects a login needs to run a native backup
// and follow it, with the permission each one takes. rds_cancel_task is left
// out on purpose: it cancels any task of the instance, restores included.
var BackupObjects = []struct{ Object, Permission string }{
	{"rds_backup_database", "EXECUTE"},
	{"rds_task_status", "EXECUTE"},
	{"rds_fn_task_status", "SELECT"},
}

// CreateLogin creates a SQL login with the password policy of the instance
// on and no expiry. Pass MaskedPassword for the statement that is shown.
func CreateLogin(login, password string) string {
	return "CREATE LOGIN " + quoteIdent(login) + " WITH PASSWORD = " + Str(password) +
		", CHECK_POLICY = ON, CHECK_EXPIRATION = OFF;"
}

// CreateUser creates the user of login in db, named after the login.
func CreateUser(db, login string) string {
	return use(db) + "CREATE USER " + quoteIdent(login) + " FOR LOGIN " + quoteIdent(login) + ";"
}

// MapUser binds a user of db that has no login (an orphan after a restore)
// to login.
func MapUser(db, user, login string) string {
	return use(db) + "ALTER USER " + quoteIdent(user) + " WITH LOGIN = " + quoteIdent(login) + ";"
}

// AddRole makes user a member of role in db.
func AddRole(db, role, user string) string {
	return use(db) + "ALTER ROLE " + quoteIdent(role) + " ADD MEMBER " + quoteIdent(user) + ";"
}

// DropRole takes user out of role in db.
func DropRole(db, role, user string) string {
	return use(db) + "ALTER ROLE " + quoteIdent(role) + " DROP MEMBER " + quoteIdent(user) + ";"
}

// DropUser removes user from db; the login stays.
func DropUser(db, user string) string {
	return use(db) + "DROP USER " + quoteIdent(user) + ";"
}

// BackupGrants gives user of msdb (CreateUser("msdb", login) first when
// missing) what a native backup needs, one GRANT per line.
func BackupGrants(user string) string {
	return backupPermissions("GRANT", "TO", user)
}

// BackupRevokes takes the BackupGrants back.
func BackupRevokes(user string) string {
	return backupPermissions("REVOKE", "FROM", user)
}

func backupPermissions(verb, prep, user string) string {
	var b strings.Builder
	b.WriteString(use("msdb"))
	for i, o := range BackupObjects {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(verb + " " + o.Permission + " ON [dbo]." + quoteIdent(o.Object) + " " + prep + " " + quoteIdent(user) + ";")
	}
	return b.String()
}

func use(db string) string { return "USE " + quoteIdent(db) + ";\n" }
