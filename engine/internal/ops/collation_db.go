package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// maxListed is how many blockers or clashes a check names before "and N more".
const maxListed = 10

// validateSetCollation is Validate for set-collation, after what the
// collation operations share (validateCollation).
func validateSetCollation(p Params) error {
	if err := validate.DBName(p.Database); err != nil {
		return err
	}
	if p.Collation == "" {
		return errors.New("set-collation needs the collation to change to")
	}
	if err := validate.CollationName(p.Collation); err != nil {
		return err
	}
	if len(p.Tables) > 0 || p.SkipDataChecks {
		return errors.New("tables and skipDataChecks are for convert-columns, not set-collation")
	}
	return nil
}

// SetCollation prepares the change of the default collation of a database
// (collation design §6.1). Existing columns keep their collation: only
// convert-columns changes them. The confirmation is the database name.
func (s *Session) SetCollation(ctx context.Context, in Params) (*Prepared, error) {
	if err := validateSetCollation(in); err != nil {
		return nil, err
	}
	db := in.Database
	p := s.prepare(OpSetCollation, in)
	p.Plan.Subject = db
	if err := s.checkDatabase(ctx, p, db, "ONLINE", "only an online database can change its collation"); err != nil {
		return nil, err
	}
	if !p.Ready() {
		return p, nil
	}
	st, err := s.SQL.CollationState(ctx, db)
	if err != nil {
		return nil, err
	}
	if !st.Exists {
		p.fail("database", "Database", db+" not found on the instance")
		return p, nil
	}

	const astep, alabel = "access", "Database access"
	switch {
	case st.ReadOnly:
		p.fail(astep, alabel, db+" is read-only")
	case st.UserAccess != "MULTI_USER":
		p.fail(astep, alabel, fmt.Sprintf("%s is %s: someone is maintaining it", db, st.UserAccess))
	default:
		p.ok(astep, alabel, db+" is open to every session")
	}

	const cstep, clabel = "collation", "Collation"
	info, found, err := s.SQL.Collation(ctx, in.Collation)
	if err != nil {
		return nil, err
	}
	if !found {
		p.fail(cstep, clabel, in.Collation+" is not a collation of this instance")
		return p, nil
	}
	if info.Name == "" {
		info.Name = in.Collation
	}
	to := info.Name
	if strings.EqualFold(st.Collation, to) {
		p.warn(cstep, clabel, fmt.Sprintf("%s already uses %s: nothing to do", db, st.Collation))
		return p, nil
	}
	p.Params.Collation = to
	p.ok(cstep, clabel, st.Collation+" → "+to)

	const pstep, plabel = "permission", "Permission"
	if st.CanAlter {
		p.ok(pstep, plabel, "the master user can ALTER "+db)
	} else {
		p.fail(pstep, plabel, "the master user cannot ALTER "+db+": it is not its owner or a db_owner")
	}

	const mstep, mlabel = "availability", "Multi-AZ"
	if st.AvailabilityReplica || st.Mirrored {
		p.fail(mstep, mlabel, "SINGLE_USER is not allowed on a Multi-AZ database: "+db+" is in a mirroring or availability group")
	} else {
		p.ok(mstep, mlabel, db+" is not mirrored nor in an availability group (if it were, SQL Server would refuse SINGLE_USER and nothing would change)")
	}

	blockers, err := s.SQL.CollationBlockers(ctx, db)
	if err != nil {
		return nil, err
	}
	const dstep, dlabel = "dependents", "Objects that depend on the collation"
	if len(blockers) > 0 {
		names := make([]string, len(blockers))
		for i, b := range blockers {
			names[i] = b.Kind + " " + strings.TrimPrefix(b.Schema+"."+b.Object, ".")
		}
		p.fail(dstep, dlabel, "SQL Server refuses the change while they exist: "+listed(names)+"; drop or change them by hand and recreate them afterwards")
	} else {
		p.ok(dstep, dlabel, "no object of "+db+" depends on its collation")
	}

	clashes, err := s.SQL.NameClashes(ctx, db, to)
	if err != nil {
		return nil, err
	}
	const nstep, nlabel = "names", "Names under the new collation"
	if len(clashes) > 0 {
		p.fail(nstep, nlabel, "these names would collide under "+to+": "+listed(clashes))
	} else {
		p.ok(nstep, nlabel, "no name collides under "+to)
	}

	tasks, err := s.SQL.ActiveTasks(ctx, db)
	if err != nil {
		return nil, err
	}
	if tasks > 0 {
		p.fail("tasks", "Native tasks", fmt.Sprintf("%d native task(s) of %s running or queued: wait for them", tasks, db))
	} else {
		p.ok("tasks", "Native tasks", "no native task of "+db+" running")
	}

	n, err := s.SQL.ActiveSessions(ctx, db)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		p.warn("sessions", "Active sessions", fmt.Sprintf("%d session(s) will be disconnected and their transactions rolled back", n))
	} else {
		p.ok("sessions", "Active sessions", "no other session in "+db)
	}

	srv, err := s.SQL.ServerCollation(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.EqualFold(to, srv.Server):
		p.ok("server", "Server collation", "matches the server: temp tables are fine")
	case st.Containment == "NONE":
		p.warn("server", "Server collation", fmt.Sprintf("%s differs from the server (%s): #temp tables take the tempdb collation and comparisons with them can fail (Msg 468)", to, srv.Server))
	default:
		p.ok("server", "Server collation", db+" is contained: temp tables use its collation")
	}

	cols, err := s.SQL.CharColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	keep := 0
	for _, c := range cols {
		if strings.EqualFold(c.Collation, st.Collation) && c.Unsupported() == "" {
			keep++
		}
	}
	if keep > 0 {
		p.warn("columns", "Existing columns", fmt.Sprintf("%d column(s) keep %s: convert them with convert-columns", keep, st.Collation))
	} else {
		p.ok("columns", "Existing columns", "no column keeps "+st.Collation)
	}

	if info.UTF8 {
		p.warn("utf8", "UTF-8", to+" is a UTF-8 collation: char and varchar hold UTF-8 from now on, byte lengths and code pages change")
	} else {
		p.ok("utf8", "UTF-8", to+" is not a UTF-8 collation")
	}

	if !p.Ready() {
		return p, nil
	}
	p.Collation = &CollationPlan{Database: db, From: st.Collation, To: to}
	step := CollationStep{ID: CollSetDatabase, Database: db, Collation: to, From: st.Collation}
	err = s.addCollation(p, step,
		fmt.Sprintf("Change the default collation of %s from %s to %s (disconnects its sessions)", db, st.Collation, to))
	return p, err
}

// listed names up to maxListed of items.
func listed(items []string) string {
	if len(items) <= maxListed {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:maxListed], ", "), len(items)-maxListed)
}

// runSetCollation runs the step of a set-collation (collation design §6.1).
// It reads the database first, so a second run is a no-op; a failure after
// the batch began reopens the database to every session.
func (s *Session) runSetCollation(ctx context.Context, st CollationStep) (string, error) {
	cur, err := s.SQL.CollationState(ctx, st.Database)
	if err != nil {
		return "", err
	}
	if !cur.Exists {
		return "", fmt.Errorf("%s not found on the instance", st.Database)
	}
	if strings.EqualFold(cur.Collation, st.Collation) {
		// A SINGLE_USER database is reopened only when this step plausibly
		// left it so (an interrupted run that changed the collation): the
		// plan read another collation. Otherwise someone else is maintaining
		// it: leave it as it is.
		if cur.UserAccess == "SINGLE_USER" {
			if st.From == "" || strings.EqualFold(st.From, st.Collation) {
				return st.Database + " already uses " + st.Collation + " (left SINGLE_USER: not by this step)", nil
			}
			if err := s.SQL.Exec(ctx, mssql.MultiUserIfSingle(st.Database)); err != nil {
				return "", fmt.Errorf("%s already uses %s but could not be reopened to every session: %w", st.Database, st.Collation, err)
			}
		}
		return st.Database + " already uses " + st.Collation, nil
	}
	if st.From != "" && !strings.EqualFold(cur.Collation, st.From) {
		return "", fmt.Errorf("the collation of %s changed since the plan (now %s, the plan expected %s): preview again", st.Database, cur.Collation, st.From)
	}
	stmt, err := st.Statement()
	if err != nil {
		return "", err
	}
	if err := s.SQL.Exec(ctx, stmt); err != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rerr := s.SQL.Exec(rctx, mssql.MultiUserIfSingle(st.Database)); rerr != nil {
			return "", fmt.Errorf("%w; %s could not be reopened: run ALTER DATABASE [%s] SET MULTI_USER", err, st.Database, strings.ReplaceAll(st.Database, "]", "]]"))
		}
		return "", fmt.Errorf("%w; %s is open to all sessions again", err, st.Database)
	}
	return st.Database + " is now " + st.Collation, nil
}
