package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// Report is the read-only post-restore report of a database, the port of
// mssql-import.sh post: properties, files, whether the login can enter it,
// orphaned SQL users, Windows principals and objects by type.
type Report struct {
	Target   string `json:"target"`
	Instance string `json:"instance"`
	// OK is false when a finding failed (the database is not online, or
	// the login cannot enter it); warnings keep it true.
	OK bool `json:"ok"`
	// Findings are the verdicts, ok, warning or failed: database, access,
	// orphans, windows.
	Findings []check.Event        `json:"findings"`
	Database mssql.DatabaseInfo   `json:"database"`
	Files    []mssql.DatabaseFile `json:"files"`
	// Access reports whether the login can enter the database: without it
	// the users and the objects are not read.
	Access            bool                `json:"access"`
	OrphanUsers       []string            `json:"orphanUsers"`
	WindowsPrincipals int                 `json:"windowsPrincipals"`
	Objects           []mssql.ObjectCount `json:"objects"`
}

// Post reads the report of db; mssql.ErrNoDatabase when it does not exist.
func (s *Session) Post(ctx context.Context, db string) (Report, error) {
	if err := validate.DBName(db); err != nil {
		return Report{}, err
	}
	d, err := s.SQL.DatabaseReport(ctx, db)
	if err != nil {
		return Report{}, err
	}
	r := Report{
		Target: s.Name, Instance: s.Target.Instance, OK: true, Findings: []check.Event{},
		Database: d.Database, Files: d.Files, Access: d.Access,
		OrphanUsers: d.OrphanUsers, WindowsPrincipals: d.WindowsPrincipals, Objects: d.Objects,
	}
	add := func(step, label string, status check.Status, detail string) {
		r.Findings = append(r.Findings, check.Event{Step: step, Label: label, Status: status, Detail: detail})
		r.OK = r.OK && status != check.Failed
	}
	switch state := d.Database.State; state {
	case "ONLINE":
		add("database", "Database", check.OK, fmt.Sprintf("ONLINE, compatibility level %d", d.Database.CompatibilityLevel))
	case "RESTORING":
		add("database", "Database", check.Failed, "RESTORING: dialga finish "+db+" brings it online")
		return r, nil
	default:
		add("database", "Database", check.Failed, state+": not online, nothing else to read")
		return r, nil
	}
	user := s.Target.SQL.User
	if !d.Access {
		add("access", "Login access", check.Failed, fmt.Sprintf(
			"%s cannot enter %s: restored databases are owned by rdsa (runbook, troubleshooting: master user access to the restored database)",
			user, db))
		return r, nil
	}
	add("access", "Login access", check.OK, user+" can enter "+db)
	if n := len(d.OrphanUsers); n > 0 {
		add("orphans", "Orphaned users", check.Warning, fmt.Sprintf(
			"%d SQL user(s) without a login on the instance: %s. For each one: CREATE LOGIN [u] WITH PASSWORD = '...'; then, in the database, ALTER USER [u] WITH LOGIN = [u];",
			n, strings.Join(d.OrphanUsers, ", ")))
	} else {
		add("orphans", "Orphaned users", check.OK, "no SQL user without a login")
	}
	if n := d.WindowsPrincipals; n > 0 {
		add("windows", "Windows users", check.Warning, fmt.Sprintf(
			"%d Windows user(s) or group(s): unusable on RDS without Active Directory", n))
	} else {
		add("windows", "Windows users", check.OK, "no Windows user or group")
	}
	return r, nil
}
