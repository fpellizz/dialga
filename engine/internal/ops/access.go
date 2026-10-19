package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// The access operations give a SQL login access to a database, take it back
// and bind a user left without a login (orphan) to one. They have no
// counterpart in mssql-import.sh. Their statements are those of
// mssql/access_tsql.go; the login itself is never dropped, and neither the
// master user of the target, sa, rdsa, the ##…## logins nor dbo, guest, sys
// and INFORMATION_SCHEMA are ever touched.

// The access operations, as the CLI commands and the API name them.
const (
	OpGrant     = "grant"
	OpRevoke    = "revoke"
	OpFixOrphan = "fix-orphan"
)

// What a revoke takes away (Params.Remove).
const (
	RemoveLevel  = "level"
	RemoveBackup = "backup"
	RemoveUser   = "user"
)

// The ids of the access actions: one per statement of access_tsql.go.
const (
	AccCreateLogin   = "create-login"
	AccCreateUser    = "create-user"
	AccMapUser       = "map-user"
	AccAddRole       = "add-role"
	AccDropRole      = "drop-role"
	AccDropUser      = "drop-user"
	AccMsdbUser      = "msdb-user"
	AccBackupGrants  = "backup-grants"
	AccBackupRevokes = "backup-revokes"
	AccMsdbDropUser  = "msdb-drop-user"
)

// IsAccess reports whether op changes who can use a database.
func IsAccess(op string) bool { return op == OpGrant || op == OpRevoke || op == OpFixOrphan }

// ErrLoginPassword means the password of a new login is missing or does not
// meet the policy. Its text never carries the password.
var ErrLoginPassword = errors.New("invalid password for the new login")

// CheckLoginPassword is the policy Dialga checks before it sends a password
// to SQL Server (which checks its own too): 12 to 128 characters, at least
// three of upper case, lower case, digits and symbols.
func CheckLoginPassword(pw string) error {
	if pw == "" {
		return fmt.Errorf("%w: the new login needs one", ErrLoginPassword)
	}
	n := len([]rune(pw))
	if n < 12 || n > 128 {
		return fmt.Errorf("%w: it must have 12 to 128 characters", ErrLoginPassword)
	}
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, c := range []bool{upper, lower, digit, symbol} {
		if c {
			classes++
		}
	}
	if classes < 3 {
		return fmt.Errorf("%w: use at least three of upper case, lower case, digits and symbols", ErrLoginPassword)
	}
	return nil
}

// AccessStep is one statement of an access operation, as the plan and the
// journal of a job record it: parameters, not the statement, so a resume
// builds it again (and refuses a journal that was edited).
type AccessStep struct {
	// ID is one of the Acc* ids.
	ID       string `json:"id"`
	Database string `json:"database,omitempty"`
	// Login is the login of create-login, create-user, map-user, msdb-user
	// (the login the user in msdb is made for).
	Login string `json:"login,omitempty"`
	// User is the user the statement changes: in Database, or in msdb for
	// the backup and msdb steps.
	User string `json:"user,omitempty"`
	Role string `json:"role,omitempty"`
}

// Statement is the T-SQL of the step. password is that of create-login: pass
// mssql.MaskedPassword for everything that is shown, journaled or hashed.
func (st AccessStep) Statement(password string) (string, error) {
	switch st.ID {
	case AccCreateLogin:
		return mssql.CreateLogin(st.Login, password), nil
	case AccCreateUser:
		return mssql.CreateUser(st.Database, st.Login), nil
	case AccMapUser:
		return mssql.MapUser(st.Database, st.User, st.Login), nil
	case AccAddRole:
		return mssql.AddRole(st.Database, st.Role, st.User), nil
	case AccDropRole:
		return mssql.DropRole(st.Database, st.Role, st.User), nil
	case AccDropUser:
		return mssql.DropUser(st.Database, st.User), nil
	case AccMsdbUser:
		return mssql.CreateUser("msdb", st.Login), nil
	case AccBackupGrants:
		return mssql.BackupGrants(st.User), nil
	case AccBackupRevokes:
		return mssql.BackupRevokes(st.User), nil
	case AccMsdbDropUser:
		return mssql.DropUser("msdb", st.User), nil
	}
	return "", fmt.Errorf("unknown access step %q", st.ID)
}

// Validate checks the names of the step, as for a new request: a journal is
// a file on disk.
func (st AccessStep) Validate(t config.Target) error {
	if _, err := st.Statement(mssql.MaskedPassword); err != nil {
		return err
	}
	if st.Database != "" {
		if err := validate.DBName(st.Database); err != nil {
			return err
		}
	}
	for _, n := range []string{st.Login, st.User} {
		if n == "" {
			continue
		}
		if err := validate.PrincipalName(n); err != nil {
			return err
		}
	}
	if st.Login != "" {
		if err := checkLoginName(st.Login, t.SQL.User); err != nil {
			return err
		}
	}
	if st.User != "" {
		if err := checkUserName(st.User, t.SQL.User); err != nil {
			return err
		}
	}
	if st.Role != "" && !slices.Contains(append(slices.Clone(mssql.LevelRoles), mssql.RoleBackup), st.Role) {
		return fmt.Errorf("role %q is not one Dialga manages", st.Role)
	}
	return nil
}

// checkLoginName refuses the logins Dialga never changes.
func checkLoginName(login, master string) error {
	switch {
	case mssql.ReservedLogin(login):
		return fmt.Errorf("%s is a login of SQL Server or RDS: Dialga never changes it", login)
	case master != "" && strings.EqualFold(login, master):
		return fmt.Errorf("%s is the master user of the target: Dialga never changes its access", login)
	}
	return nil
}

// checkUserName refuses the users Dialga never changes.
func checkUserName(user, master string) error {
	switch {
	case mssql.ReservedUser(user):
		return fmt.Errorf("%s is a user every database has: Dialga never changes it", user)
	case master != "" && strings.EqualFold(user, master):
		return fmt.Errorf("%s is the master user of the target: Dialga never changes its access", user)
	}
	return nil
}

// validateAccess is Validate for the access operations.
func validateAccess(op string, p Params, master string) error {
	if p.Key != "" || p.TaskID != 0 || p.Restore != (mssql.RestoreOptions{}) || p.Replace != (ReplaceOptions{}) {
		return fmt.Errorf("%s takes a database and a login, not a backup key, task or restore options", op)
	}
	if err := validate.DBName(p.Database); err != nil {
		return err
	}
	only := func(name string, set bool, ops ...string) error {
		if set && !slices.Contains(ops, op) {
			return fmt.Errorf("%s is for %s, not %s", name, strings.Join(ops, " and "), op)
		}
		return nil
	}
	for _, c := range []error{
		only("level", p.Level != "", OpGrant), only("backup", p.Backup, OpGrant), only("createLogin", p.CreateLogin, OpGrant),
		only("remove", p.Remove != "", OpRevoke), only("user", p.User != "", OpFixOrphan),
	} {
		if c != nil {
			return c
		}
	}
	login := p.Login
	switch op {
	case OpGrant:
		if !slices.Contains([]string{"read", "readwrite", "owner"}, p.Level) {
			return fmt.Errorf("invalid level %q: read, readwrite or owner", p.Level)
		}
	case OpRevoke:
		if !slices.Contains([]string{RemoveLevel, RemoveBackup, RemoveUser}, p.Remove) {
			return fmt.Errorf("invalid remove %q: level, backup or user", p.Remove)
		}
	case OpFixOrphan:
		if err := validate.PrincipalName(p.User); err != nil {
			return err
		}
		if err := checkUserName(p.User, master); err != nil {
			return err
		}
		if login == "" {
			login = p.User
		}
	}
	if err := validate.PrincipalName(login); err != nil {
		return err
	}
	return checkLoginName(login, master)
}

// NeedsLoginPassword reports whether the plan creates a login: Execute then
// needs ExecOptions.LoginPassword.
func (p *Prepared) NeedsLoginPassword() bool {
	return slices.ContainsFunc(p.Plan.Actions, func(a core.Action) bool { return a.ID == AccCreateLogin })
}

// AccessSteps are the steps of an access plan, as the job runs them.
func (p *Prepared) AccessSteps() []AccessStep { return p.access }

// addAccess appends the action of an access step. The statement of the plan
// carries the masked password; the one that runs gets the real one from
// Execute (p.loginPassword).
func (s *Session) addAccess(p *Prepared, st AccessStep, title string, risk core.Risk) {
	stmt, _ := st.Statement(mssql.MaskedPassword)
	p.access = append(p.access, st)
	p.Plan.Actions = append(p.Plan.Actions, core.Action{
		ID: st.ID, Title: title, Kind: core.KindSQL, Risk: risk, Statement: stmt,
		Run: func(ctx context.Context) (string, error) { return s.RunAccessStep(ctx, st, p.loginPassword) },
	})
}

// RunAccessStep runs one step. It reads what it is about to change first, so
// a step the instance already did (an interrupted job, a second run) is a
// no-op instead of an error. password is only for create-login; any error
// that quotes it is scrubbed.
func (s *Session) RunAccessStep(ctx context.Context, st AccessStep, password string) (string, error) {
	if done, err := s.accessDone(ctx, st); err != nil || done != "" {
		return done, err
	}
	if st.ID == AccCreateLogin {
		if err := CheckLoginPassword(password); err != nil {
			return "", fmt.Errorf("%w (it is never kept: run the grant again with it)", err)
		}
	}
	stmt, err := st.Statement(password)
	if err != nil {
		return "", err
	}
	if err := s.SQL.Exec(ctx, stmt); err != nil {
		if password != "" {
			err = errors.New(strings.ReplaceAll(err.Error(), password, mssql.MaskedPassword))
		}
		return "", err
	}
	return accessDetail(st), nil
}

func accessDetail(st AccessStep) string {
	switch st.ID {
	case AccCreateLogin:
		return "login " + st.Login + " created"
	case AccCreateUser:
		return "user " + st.Login + " created in " + st.Database
	case AccMapUser:
		return "user " + st.User + " bound to login " + st.Login
	case AccAddRole:
		return st.User + " added to " + st.Role
	case AccDropRole:
		return st.User + " removed from " + st.Role
	case AccDropUser:
		return "user " + st.User + " dropped from " + st.Database
	case AccMsdbUser:
		return "user " + st.Login + " created in msdb"
	case AccBackupGrants:
		return "backup permissions granted to " + st.User + " in msdb"
	case AccBackupRevokes:
		return "backup permissions revoked from " + st.User + " in msdb"
	case AccMsdbDropUser:
		return "user " + st.User + " dropped from msdb"
	}
	return ""
}

// accessDone says (non-empty) when the instance already has what the step
// asks for, or fails when it holds something the step would clash with.
func (s *Session) accessDone(ctx context.Context, st AccessStep) (string, error) {
	user := func(db, name string) (*mssql.DatabaseUser, error) {
		a, err := s.SQL.DatabaseAccess(ctx, db, s.Target.SQL.User)
		if err != nil {
			return nil, err
		}
		return findUser(a.Users, name), nil
	}
	switch st.ID {
	case AccCreateLogin:
		logins, err := s.SQL.Logins(ctx, s.Target.SQL.User)
		if err != nil {
			return "", err
		}
		if slices.ContainsFunc(logins, func(l mssql.Login) bool { return strings.EqualFold(l.Name, st.Login) }) {
			return "login " + st.Login + " already exists", nil
		}
	case AccCreateUser, AccMapUser:
		name := st.User
		if st.ID == AccCreateUser {
			name = st.Login
		}
		u, err := user(st.Database, name)
		if err != nil {
			return "", err
		}
		switch {
		case u == nil:
		case strings.EqualFold(u.Login, st.Login):
			return "user " + name + " is already bound to " + st.Login, nil
		case st.ID == AccCreateUser:
			return "", fmt.Errorf("user %s already exists in %s and is not bound to login %s", name, st.Database, st.Login)
		}
	case AccMsdbUser:
		m, err := s.SQL.MsdbUser(ctx, st.Login)
		if err != nil {
			return "", err
		}
		if m.Exists {
			return "user " + m.Name + " already exists in msdb", nil
		}
	case AccDropUser, AccAddRole, AccDropRole:
		u, err := user(st.Database, st.User)
		if err != nil {
			return "", err
		}
		switch {
		case st.ID == AccDropUser && u == nil:
			return "user " + st.User + " is already gone from " + st.Database, nil
		case st.ID == AccDropRole && (u == nil || !slices.Contains(u.Roles, st.Role)):
			return st.User + " is not in " + st.Role, nil
		case st.ID == AccAddRole && u != nil && slices.Contains(u.Roles, st.Role):
			return st.User + " is already in " + st.Role, nil
		}
	case AccMsdbDropUser:
		u, err := user("msdb", st.User)
		if err != nil {
			return "", err
		}
		if u == nil {
			return "user " + st.User + " is already gone from msdb", nil
		}
	}
	return "", nil
}

func findUser(users []mssql.DatabaseUser, name string) *mssql.DatabaseUser {
	for i := range users {
		if strings.EqualFold(users[i].Name, name) {
			return &users[i]
		}
	}
	return nil
}

// userOfLogin finds the user bound to login.
func userOfLogin(users []mssql.DatabaseUser, login string) *mssql.DatabaseUser {
	for i := range users {
		if !users[i].Orphan && users[i].Login != "" && strings.EqualFold(users[i].Login, login) {
			return &users[i]
		}
	}
	return nil
}

// accessCtx is what the three builders share: the prepared operation and the
// users of the database, after the database check passed.
func (s *Session) accessBase(ctx context.Context, op string, in Params) (*Prepared, []mssql.DatabaseUser, error) {
	if err := validateAccess(op, in, s.Target.SQL.User); err != nil {
		return nil, nil, err
	}
	p := s.prepare(op, in)
	if err := s.checkDatabase(ctx, p, in.Database, "ONLINE", "only an online database can change who uses it"); err != nil {
		return nil, nil, err
	}
	if !p.Ready() {
		return p, nil, nil
	}
	a, err := s.SQL.DatabaseAccess(ctx, in.Database, s.Target.SQL.User)
	if err != nil {
		return nil, nil, err
	}
	return p, a.Users, nil
}

// Grant prepares the access of login to db: the level (db_datareader,
// db_datawriter or db_owner, the roles of the other levels dropped), with
// backup the right to run rds_backup_database, and with createLogin the
// login itself (its password is given to Execute and never shown). The
// user is the one already bound to the login, else a new one named after it.
func (s *Session) Grant(ctx context.Context, in Params) (*Prepared, error) {
	p, users, err := s.accessBase(ctx, OpGrant, in)
	if err != nil || !p.Ready() {
		return p, err
	}
	db, login := in.Database, in.Login
	const lstep, llabel = "login", "Login"
	logins, err := s.SQL.Logins(ctx, s.Target.SQL.User)
	if err != nil {
		return nil, err
	}
	exists := slices.ContainsFunc(logins, func(l mssql.Login) bool { return strings.EqualFold(l.Name, login) })
	switch {
	case in.CreateLogin && exists:
		p.fail(lstep, llabel, login+" already exists: grant without creating it")
	case in.CreateLogin:
		p.ok(lstep, llabel, login+" does not exist: it will be created (its password is not shown)")
	case !exists:
		p.fail(lstep, llabel, login+" does not exist on the instance: grant with createLogin to create it")
	default:
		p.ok(lstep, llabel, login+" exists")
	}

	const ustep, ulabel = "user", "Database user"
	bound := userOfLogin(users, login)
	byName := findUser(users, login)
	user := login
	var have []string
	createUser, bindOrphan := false, false
	switch {
	case bound != nil && !bound.Managed:
		p.fail(ustep, ulabel, "user "+bound.Name+" of "+login+" is managed by SQL Server or RDS: Dialga never changes it")
	case bound != nil:
		user, have = bound.Name, bound.Roles
		p.ok(ustep, ulabel, "user "+bound.Name+" is bound to "+login)
	case byName != nil && byName.Orphan && !byName.Managed:
		p.fail(ustep, ulabel, "user "+byName.Name+" is managed by SQL Server or RDS: Dialga never changes it")
	case byName != nil && byName.Orphan:
		// The usual case after a restore from another instance: the user came
		// with the database, its login did not. Bind it (to the login created
		// here, or to the one that exists), keeping its roles.
		user, have = byName.Name, byName.Roles
		bindOrphan = true
		p.ok(ustep, ulabel, "user "+byName.Name+" has no login (orphan): it will be bound to "+login)
	case byName != nil:
		p.fail(ustep, ulabel, "user "+byName.Name+" exists in "+db+" and is bound to another login ("+byName.Login+")")
	default:
		createUser = true
		p.ok(ustep, ulabel, "no user of "+login+" in "+db+": it will be created")
	}
	if !p.Ready() {
		return p, nil
	}

	if in.CreateLogin {
		s.addAccess(p, AccessStep{ID: AccCreateLogin, Login: login}, "Create login "+login, core.Change)
	}
	if createUser {
		s.addAccess(p, AccessStep{ID: AccCreateUser, Database: db, Login: login}, fmt.Sprintf("Create user %s in %s", login, db), core.Change)
	}
	if bindOrphan {
		s.addAccess(p, AccessStep{ID: AccMapUser, Database: db, User: user, Login: login},
			fmt.Sprintf("Bind user %s of %s to login %s", user, db, login), core.Change)
	}
	want := mssql.LevelRolesOf(in.Level)
	for _, role := range mssql.LevelRoles {
		if !slices.Contains(want, role) && slices.Contains(have, role) {
			s.addAccess(p, AccessStep{ID: AccDropRole, Database: db, User: user, Role: role},
				fmt.Sprintf("Remove %s from %s in %s", user, role, db), core.Change)
		}
	}
	for _, role := range want {
		if !slices.Contains(have, role) {
			s.addAccess(p, AccessStep{ID: AccAddRole, Database: db, User: user, Role: role},
				fmt.Sprintf("Add %s to %s in %s", user, role, db), core.Change)
		}
	}
	if in.Backup {
		if err := s.grantBackup(ctx, p, db, login, user, have, in.CreateLogin); err != nil {
			return nil, err
		}
	}
	if len(p.Plan.Actions) == 0 {
		p.warn("access", "Access", fmt.Sprintf("nothing to do: %s already has the %s access to %s%s", user, in.Level, db, map[bool]string{true: " and can back it up"}[in.Backup]))
	}
	return p, nil
}

// grantBackup adds what rds_backup_database needs: db_backupoperator in db
// and, in msdb, a user of the login with the BackupObjects permissions.
func (s *Session) grantBackup(ctx context.Context, p *Prepared, db, login, user string, have []string, fresh bool) error {
	if !slices.Contains(have, mssql.RoleBackup) {
		s.addAccess(p, AccessStep{ID: AccAddRole, Database: db, User: user, Role: mssql.RoleBackup},
			fmt.Sprintf("Add %s to %s in %s", user, mssql.RoleBackup, db), core.Change)
	}
	var m mssql.MsdbUser
	if !fresh { // a login that does not exist yet has no user in msdb
		var err error
		if m, err = s.SQL.MsdbUser(ctx, login); err != nil {
			return err
		}
	}
	msdbName := login
	if m.Exists {
		msdbName = m.Name
	} else {
		s.addAccess(p, AccessStep{ID: AccMsdbUser, Login: login}, "Create user "+login+" in msdb", core.Change)
	}
	if m.Grants < len(mssql.BackupObjects) {
		s.addAccess(p, AccessStep{ID: AccBackupGrants, User: msdbName}, "Grant "+msdbName+" the backup permissions in msdb", core.Change)
	}
	p.ok("backup", "Backup", "rds_backup_database runs for "+login+" with db_backupoperator and the msdb permissions")
	return nil
}

// Revoke prepares taking access back from login in db: remove=level drops
// the level roles, backup drops db_backupoperator and the msdb permissions
// (when no other database still lets the login back up), user drops the
// user (destructive: it asks to retype the login). The login stays.
func (s *Session) Revoke(ctx context.Context, in Params) (*Prepared, error) {
	p, users, err := s.accessBase(ctx, OpRevoke, in)
	if err != nil || !p.Ready() {
		return p, err
	}
	db, login := in.Database, in.Login
	const ustep, ulabel = "user", "Database user"
	u := userOfLogin(users, login)
	switch {
	case u == nil && in.Remove == RemoveBackup:
		// No user here: the msdb permissions may still be left behind.
		p.warn(ustep, ulabel, "no user of "+login+" in "+db)
	case u == nil:
		p.fail(ustep, ulabel, "no user of "+login+" in "+db+": nothing to take back")
	case !u.Managed:
		p.fail(ustep, ulabel, "user "+u.Name+" of "+login+" is managed by SQL Server or RDS: Dialga never changes it")
	default:
		p.ok(ustep, ulabel, "user "+u.Name+" is bound to "+login)
	}
	if !p.Ready() {
		return p, nil
	}
	var roles []string
	name := login
	if u != nil {
		roles, name = u.Roles, u.Name
	}
	switch in.Remove {
	case RemoveLevel:
		for _, role := range mssql.LevelRoles {
			if slices.Contains(roles, role) {
				s.addAccess(p, AccessStep{ID: AccDropRole, Database: db, User: name, Role: role},
					fmt.Sprintf("Remove %s from %s in %s", name, role, db), core.Change)
			}
		}
		if len(p.Plan.Actions) == 0 {
			p.warn("access", "Access", fmt.Sprintf("nothing to do: %s has no level role in %s", name, db))
		}
	case RemoveBackup:
		if slices.Contains(roles, mssql.RoleBackup) {
			s.addAccess(p, AccessStep{ID: AccDropRole, Database: db, User: name, Role: mssql.RoleBackup},
				fmt.Sprintf("Remove %s from %s in %s", name, mssql.RoleBackup, db), core.Change)
		}
		if err := s.revokeBackup(ctx, p, db, login); err != nil {
			return nil, err
		}
		if len(p.Plan.Actions) == 0 {
			p.warn("access", "Access", fmt.Sprintf("nothing to do: %s cannot back %s up", login, db))
		}
	case RemoveUser:
		owned, err := s.SQL.UserSchemas(ctx, db, name)
		if err != nil {
			return nil, err
		}
		if len(owned) > 0 {
			p.fail("schemas", "Schemas", fmt.Sprintf("user %s owns the schema(s) %s in %s: transfer them before dropping it", name, strings.Join(owned, ", "), db))
			return p, nil
		}
		p.ok("schemas", "Schemas", "user "+name+" owns no schema")
		p.Plan.Subject = login
		s.addAccess(p, AccessStep{ID: AccDropUser, Database: db, User: name},
			fmt.Sprintf("Drop user %s from %s", name, db), core.Destructive)
		if err := s.revokeBackup(ctx, p, db, login); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// revokeBackup adds the msdb cleanup of login: its backup permissions go
// when no other database lets it back up, and its msdb user when nothing
// else needs it. db is the database being changed (it does not count).
func (s *Session) revokeBackup(ctx context.Context, p *Prepared, db, login string) error {
	const step, label = "msdb", "Backup permissions"
	m, err := s.SQL.MsdbUser(ctx, login)
	if err != nil {
		return err
	}
	if !m.Exists || m.Grants == 0 {
		p.ok(step, label, login+" has no backup permission in msdb")
		return nil
	}
	used, unreadable, err := s.SQL.BackupOperators(ctx, login)
	if err != nil {
		return err
	}
	used = slices.DeleteFunc(used, func(d string) bool { return strings.EqualFold(d, db) })
	switch {
	case len(used) > 0:
		p.warn(step, label, login+" still backs up "+strings.Join(used, ", ")+": its msdb permissions stay")
		return nil
	case len(unreadable) > 0:
		p.warn(step, label, "cannot read "+strings.Join(unreadable, ", ")+" to see whether "+login+" backs them up: its msdb permissions stay")
		return nil
	}
	p.ok(step, label, "no other database lets "+login+" back up")
	s.addAccess(p, AccessStep{ID: AccBackupRevokes, User: m.Name}, "Revoke the backup permissions of "+m.Name+" in msdb", core.Change)
	if m.Others > 0 {
		p.warn("msdb-user", "User in msdb", "user "+m.Name+" has other permissions in msdb: it stays")
		return nil
	}
	s.addAccess(p, AccessStep{ID: AccMsdbDropUser, User: m.Name}, "Drop user "+m.Name+" from msdb", core.Change)
	return nil
}

// FixOrphan prepares binding the orphan user of db (a SQL user whose login
// does not exist here, typical after a restore from another instance) to
// login, which defaults to the name of the user.
func (s *Session) FixOrphan(ctx context.Context, in Params) (*Prepared, error) {
	p, users, err := s.accessBase(ctx, OpFixOrphan, in)
	if err != nil || !p.Ready() {
		return p, err
	}
	db, user, login := in.Database, in.User, in.Login
	if login == "" {
		login = user
	}
	p.Params.Login = login
	const ustep, ulabel = "user", "Database user"
	u := findUser(users, user)
	switch {
	case u == nil:
		p.fail(ustep, ulabel, "no user "+user+" in "+db)
	case !u.Orphan && u.Login != "":
		p.fail(ustep, ulabel, "user "+u.Name+" is not an orphan: it is bound to "+u.Login)
	case !u.Orphan:
		p.fail(ustep, ulabel, "user "+u.Name+" is not a SQL user with a missing login")
	default:
		p.ok(ustep, ulabel, "user "+u.Name+" has no login (orphan)")
	}
	const lstep, llabel = "login", "Login"
	logins, err := s.SQL.Logins(ctx, s.Target.SQL.User)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(logins, func(l mssql.Login) bool { return strings.EqualFold(l.Name, login) }) {
		p.fail(lstep, llabel, login+" does not exist on the instance: create it first")
	} else if other := userOfLogin(users, login); other != nil {
		p.fail(lstep, llabel, login+" is already bound to user "+other.Name+" in "+db)
	} else {
		p.ok(lstep, llabel, login+" exists and is not bound in "+db)
	}
	if !p.Ready() {
		return p, nil
	}
	s.addAccess(p, AccessStep{ID: AccMapUser, Database: db, User: u.Name, Login: login},
		fmt.Sprintf("Bind user %s of %s to login %s", u.Name, db, login), core.Change)
	return p, nil
}

// AccessView is the answer of the database access view: the users of a
// database and how they can use it.
type AccessView struct {
	Target   string               `json:"target"`
	Instance string               `json:"instance"`
	Database string               `json:"database"`
	Users    []mssql.DatabaseUser `json:"users"`
	// WindowsPrincipals counts Windows users and groups, unusable on RDS
	// without Active Directory.
	WindowsPrincipals int `json:"windowsPrincipals"`
}

// Logins lists the SQL logins of the instance (read-only), the master user
// of the target flagged.
func (s *Session) Logins(ctx context.Context) ([]mssql.Login, error) {
	return s.SQL.Logins(ctx, s.Target.SQL.User)
}

// Access reads who can use db (read-only): mssql.ErrNoDatabase when it does
// not exist, an error when it is not online.
func (s *Session) Access(ctx context.Context, db string) (AccessView, error) {
	state, err := s.SQL.DatabaseState(ctx, db)
	switch {
	case err != nil:
		return AccessView{}, err
	case state == "":
		return AccessView{}, fmt.Errorf("%s: %w", db, mssql.ErrNoDatabase)
	case state != "ONLINE":
		return AccessView{}, fmt.Errorf("%s is %s, not ONLINE: its users cannot be read", db, state)
	}
	a, err := s.SQL.DatabaseAccess(ctx, db, s.Target.SQL.User)
	if err != nil {
		return AccessView{}, err
	}
	return AccessView{Target: s.Name, Instance: s.Target.Instance, Database: db, Users: a.Users, WindowsPrincipals: a.WindowsPrincipals}, nil
}
