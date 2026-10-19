package mssql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The read-only side of the database access operations: who can log in to
// the instance and who can use a database. The changes are the statements of
// access_tsql.go.

// Login is a SQL login of the instance.
type Login struct {
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
	// Master is the master user of the target: its access is never changed.
	Master bool `json:"master"`
	// CreatedAt is the server wall clock without a zone.
	CreatedAt string `json:"createdAt,omitempty"`
}

// DatabaseUser is a SQL user of a database.
type DatabaseUser struct {
	Name string `json:"name"`
	// Login is the login the user is bound to (by SID); empty for an orphan.
	Login string `json:"login,omitempty"`
	// Orphan: a SQL user whose SID matches no login.
	Orphan bool     `json:"orphan"`
	Roles  []string `json:"roles"`
	// Level is none, read, readwrite, owner or custom (AccessLevel).
	Level string `json:"level"`
	// Backup: member of db_backupoperator and its login has the msdb grants
	// of rds_backup_database. BackupPartial: only one of the two.
	Backup        bool `json:"backup"`
	BackupPartial bool `json:"backupPartial,omitempty"`
	// Managed: Dialga may change it.
	Managed bool `json:"managed"`
}

// DatabaseAccess is who can use a database.
type DatabaseAccess struct {
	Users []DatabaseUser `json:"users"`
	// WindowsPrincipals counts Windows users and groups, unusable on RDS
	// without Active Directory.
	WindowsPrincipals int `json:"windowsPrincipals"`
}

// MsdbUser is the user of a login in msdb and what it can do there.
type MsdbUser struct {
	Exists bool
	Name   string
	// Grants is how many of the BackupObjects permissions it has (0 to 3).
	Grants int
	// Others counts its other permissions (CONNECT excepted) and role
	// memberships: what keeps the user needed after the backup grants go.
	Others int
}

// ReservedLogin reports whether name is a login Dialga never changes: sa and
// rdsa, and the internal ##…## logins.
func ReservedLogin(name string) bool {
	l := strings.ToLower(name)
	return l == "sa" || l == "rdsa" || strings.HasPrefix(l, "##")
}

// reservedUsers are the users every database has.
var reservedUsers = []string{"dbo", "guest", "sys", "information_schema"}

// ReservedUser reports whether name is a user of every database (dbo,
// guest, sys, INFORMATION_SCHEMA).
func ReservedUser(name string) bool {
	return slices.Contains(reservedUsers, strings.ToLower(name))
}

// The roles that make up a level of access: the ones a grant sets and a
// revoke of the level takes away. The other roles of a user stay.
var LevelRoles = []string{"db_datareader", "db_datawriter", "db_owner"}

// AccessLevel derives the level from the roles: owner (db_owner), readwrite
// (db_datareader and db_datawriter), read (db_datareader), custom (other
// roles only, db_backupoperator aside) or none.
func AccessLevel(roles []string) string {
	has := func(r string) bool { return slices.Contains(roles, r) }
	switch {
	case has("db_owner"):
		return "owner"
	case has("db_datareader") && has("db_datawriter"):
		return "readwrite"
	case has("db_datareader"):
		return "read"
	}
	for _, r := range roles {
		if r != RoleBackup {
			return "custom"
		}
	}
	return "none"
}

// LevelRolesOf returns the roles of a level (read, readwrite, owner).
func LevelRolesOf(level string) []string {
	switch level {
	case "read":
		return RolesRead
	case "readwrite":
		return RolesReadWrite
	case "owner":
		return RolesOwner
	}
	return nil
}

// Logins lists the SQL logins, without sa, rdsa and the ##…## ones; master
// names the master user of the target.
func (s Session) Logins(ctx context.Context, master string) ([]Login, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, is_disabled, create_date FROM sys.server_principals
		WHERE type = 'S' AND name NOT IN (N'sa', N'rdsa') AND name NOT LIKE N'##%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	list := []Login{}
	for rows.Next() {
		var l Login
		var created sql.NullTime
		if err := rows.Scan(&l.Name, &l.Disabled, &created); err != nil {
			return nil, err
		}
		l.Master = master != "" && strings.EqualFold(l.Name, master)
		if created.Valid {
			l.CreatedAt = created.Time.Format("2006-01-02T15:04:05")
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

// backupGrantFilter is the WHERE clause that picks the BackupObjects
// permissions of o (objects) and p (permissions) in msdb.
func backupGrantFilter() string {
	var parts []string
	for _, b := range BackupObjects {
		parts = append(parts, "(o.name = "+Str(b.Object)+" AND p.permission_name = "+Str(b.Permission)+")")
	}
	return "o.schema_id = SCHEMA_ID(N'dbo') AND (" + strings.Join(parts, " OR ") + ")"
}

// DatabaseAccess reads the users of db (which must be online): SQL users
// with their login, roles and backup grants. master names the master user of
// the target, which is never managed.
func (s Session) DatabaseAccess(ctx context.Context, db, master string) (DatabaseAccess, error) {
	// The database name cannot be a parameter of a three-part name: it is
	// quoted as an identifier (callers validate it first anyway).
	q := quoteIdent(db)
	a := DatabaseAccess{Users: []DatabaseUser{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT dp.name, dp.sid, sp.name,
		CASE WHEN dp.principal_id > 4 AND dp.authentication_type = 1 AND sp.sid IS NULL THEN 1 ELSE 0 END
		FROM `+q+`.sys.database_principals dp LEFT JOIN sys.server_principals sp ON sp.sid = dp.sid
		WHERE dp.type = 'S' AND (dp.principal_id <= 4 OR dp.authentication_type = 1) ORDER BY dp.name`)
	if err != nil {
		return DatabaseAccess{}, err
	}
	sids := map[string]string{} // user -> hex SID
	for rows.Next() {
		var u DatabaseUser
		var sid []byte
		var login sql.NullString
		if err := rows.Scan(&u.Name, &sid, &login, &u.Orphan); err != nil {
			_ = rows.Close()
			return DatabaseAccess{}, err
		}
		u.Login = login.String
		u.Roles = []string{}
		u.Managed = !ReservedUser(u.Name) && !ReservedLogin(u.Login) && !(master != "" &&
			(strings.EqualFold(u.Name, master) || strings.EqualFold(u.Login, master)))
		sids[u.Name] = hex.EncodeToString(sid)
		a.Users = append(a.Users, u)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DatabaseAccess{}, err
	}
	_ = rows.Close()

	roles := map[string][]string{}
	rows, err = s.DB.QueryContext(ctx, `SELECT m.name, r.name
		FROM `+q+`.sys.database_role_members rm
		JOIN `+q+`.sys.database_principals r ON r.principal_id = rm.role_principal_id
		JOIN `+q+`.sys.database_principals m ON m.principal_id = rm.member_principal_id
		WHERE m.type = 'S' ORDER BY m.name, r.name`)
	if err != nil {
		return DatabaseAccess{}, err
	}
	for rows.Next() {
		var member, role string
		if err := rows.Scan(&member, &role); err != nil {
			_ = rows.Close()
			return DatabaseAccess{}, err
		}
		roles[member] = append(roles[member], role)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DatabaseAccess{}, err
	}
	_ = rows.Close()

	// How many of the backup permissions each SID has in msdb.
	grants := map[string]int{}
	rows, err = s.DB.QueryContext(ctx, `SELECT mp.sid, COUNT(DISTINCT o.name)
		FROM msdb.sys.database_principals mp
		JOIN msdb.sys.database_permissions p ON p.grantee_principal_id = mp.principal_id AND p.class = 1 AND p.state IN ('G', 'W')
		JOIN msdb.sys.objects o ON o.object_id = p.major_id
		WHERE mp.type = 'S' AND `+backupGrantFilter()+` GROUP BY mp.sid`)
	if err != nil {
		return DatabaseAccess{}, err
	}
	for rows.Next() {
		var sid []byte
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			_ = rows.Close()
			return DatabaseAccess{}, err
		}
		grants[hex.EncodeToString(sid)] = n
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DatabaseAccess{}, err
	}
	_ = rows.Close()

	for i := range a.Users {
		u := &a.Users[i]
		if r := roles[u.Name]; r != nil {
			u.Roles = r
		}
		u.Level = AccessLevel(u.Roles)
		inRole := slices.Contains(u.Roles, RoleBackup)
		full := !u.Orphan && grants[sids[u.Name]] == len(BackupObjects)
		some := !u.Orphan && grants[sids[u.Name]] > 0
		u.Backup = inRole && full
		u.BackupPartial = !u.Backup && (inRole || some)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+q+`.sys.database_principals
		WHERE type IN ('U', 'G') AND principal_id > 4`).Scan(&a.WindowsPrincipals); err != nil {
		return DatabaseAccess{}, err
	}
	return a, nil
}

// MsdbUser reads the user bound to login in msdb (by SID) and what it can do
// there.
func (s Session) MsdbUser(ctx context.Context, login string) (MsdbUser, error) {
	var u MsdbUser
	var pid int
	err := s.DB.QueryRowContext(ctx, `SELECT mp.principal_id, mp.name FROM msdb.sys.database_principals mp
		JOIN sys.server_principals sp ON sp.sid = mp.sid WHERE sp.name = @login AND mp.type = 'S'`,
		sql.Named("login", login)).Scan(&pid, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return MsdbUser{}, nil
	}
	if err != nil {
		return MsdbUser{}, err
	}
	u.Exists = true
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT o.name) FROM msdb.sys.database_permissions p
		JOIN msdb.sys.objects o ON o.object_id = p.major_id
		WHERE p.grantee_principal_id = @pid AND p.class = 1 AND p.state IN ('G', 'W') AND `+backupGrantFilter(),
		sql.Named("pid", pid)).Scan(&u.Grants); err != nil {
		return MsdbUser{}, err
	}
	// Everything else it has: permissions that are not the backup ones
	// (CONNECT is what a user is made of) and the roles it is a member of.
	err = s.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM msdb.sys.database_permissions p
		   LEFT JOIN msdb.sys.objects o ON p.class = 1 AND o.object_id = p.major_id
		  WHERE p.grantee_principal_id = @pid AND p.state IN ('G', 'W')
		    AND NOT (p.class = 0 AND p.permission_name = 'CONNECT')
		    AND NOT (p.class = 1 AND `+backupGrantFilter()+`))
		+ (SELECT COUNT(*) FROM msdb.sys.database_role_members WHERE member_principal_id = @pid)`,
		sql.Named("pid", pid)).Scan(&u.Others)
	return u, err
}

// BackupOperators lists the online databases where the user bound to login
// is a member of db_backupoperator, and those it could not read (the caller
// has no access to them): the grants of msdb are still needed while one
// database uses them.
func (s Session) BackupOperators(ctx context.Context, login string) (used, unreadable []string, err error) {
	rows, err := s.DB.QueryContext(ctx, `SET NOCOUNT ON;
DECLARE @r TABLE (db sysname, n int NULL);
DECLARE @d sysname, @q nvarchar(max);
DECLARE c CURSOR LOCAL FAST_FORWARD FOR
  SELECT name FROM sys.databases WHERE state_desc = 'ONLINE' AND database_id > 4 AND name <> N'rdsadmin';
OPEN c; FETCH NEXT FROM c INTO @d;
WHILE @@FETCH_STATUS = 0
BEGIN
  BEGIN TRY
    SET @q = N'SELECT @d, COUNT(*) FROM ' + QUOTENAME(@d) + N'.sys.database_role_members rm
      JOIN ' + QUOTENAME(@d) + N'.sys.database_principals r ON r.principal_id = rm.role_principal_id AND r.name = N''db_backupoperator''
      JOIN ' + QUOTENAME(@d) + N'.sys.database_principals m ON m.principal_id = rm.member_principal_id
      JOIN sys.server_principals sp ON sp.sid = m.sid WHERE sp.name = @login';
    INSERT @r EXEC sp_executesql @q, N'@d sysname, @login sysname', @d, @login;
  END TRY
  BEGIN CATCH
    INSERT @r VALUES (@d, NULL);
  END CATCH
  FETCH NEXT FROM c INTO @d;
END
CLOSE c; DEALLOCATE c;
SELECT db, n FROM @r ORDER BY db;`, sql.Named("login", login))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var db string
		var n sql.NullInt64
		if err := rows.Scan(&db, &n); err != nil {
			return nil, nil, err
		}
		switch {
		case !n.Valid:
			unreadable = append(unreadable, db)
		case n.Int64 > 0:
			used = append(used, db)
		}
	}
	return used, unreadable, rows.Err()
}

// UserSchemas lists the schemas of db that user owns (a user that owns one
// cannot be dropped).
func (s Session) UserSchemas(ctx context.Context, db, user string) ([]string, error) {
	q := quoteIdent(db)
	list, err := s.strings(ctx, fmt.Sprintf(`SELECT s.name FROM %s.sys.schemas s
		JOIN %s.sys.database_principals p ON p.principal_id = s.principal_id
		WHERE p.name = %s ORDER BY s.name`, q, q, Str(user)))
	return list, err
}
