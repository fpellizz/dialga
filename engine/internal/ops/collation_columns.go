// Work package C: see the collation design.

package ops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// convert-columns converts the char columns of the tables of a database to
// a collation. ALTER COLUMN ... COLLATE fails while an index, a constraint
// or a statistic uses the column, and a foreign key needs the same
// collation on both sides: so the tables linked by the foreign keys it has
// to drop form a unit, converted in one transaction that drops what depends
// on the columns, alters them and creates everything again (mssql.ConvertUnit).
// What it cannot rebuild exactly, it refuses (planUnits).

// MaxConvertTables is the most tables a convert-columns plan converts.
const MaxConvertTables = 200

// Thresholds of the size warning: a unit is rebuilt in one transaction, so
// the log grows by about its size.
const (
	convertWarnMB   = 1024
	convertWarnRows = 10_000_000
)

// dataCheckTimeout bounds each data check.
const dataCheckTimeout = 60 * time.Second

// validateConvertColumns is Validate for convert-columns, after what the
// collation operations share (validateCollation).
func validateConvertColumns(p Params) error {
	if err := validate.DBName(p.Database); err != nil {
		return err
	}
	if p.Collation != "" {
		if err := validate.CollationName(p.Collation); err != nil {
			return err
		}
	}
	if len(p.Tables) > MaxConvertTables {
		return fmt.Errorf("%d tables: a conversion takes at most %d", len(p.Tables), MaxConvertTables)
	}
	seen := map[string]bool{}
	for _, t := range p.Tables {
		if err := validate.ObjectName(t.Schema); err != nil {
			return err
		}
		if err := validate.ObjectName(t.Name); err != nil {
			return err
		}
		k := strings.ToLower(t.Schema) + "\x00" + strings.ToLower(t.Name)
		if seen[k] {
			return fmt.Errorf("table %s.%s is listed twice", t.Schema, t.Name)
		}
		seen[k] = true
	}
	return nil
}

// ConvertColumns prepares the conversion of the columns of a database to a
// collation (collation design §6.2): with in.Tables only those tables (and
// the tables their foreign keys pull in), otherwise every table; the target
// is in.Collation or, empty, the default collation of the database.
func (s *Session) ConvertColumns(ctx context.Context, in Params) (*Prepared, error) {
	if err := validateConvertColumns(in); err != nil {
		return nil, err
	}
	db := in.Database
	p := s.prepare(OpConvertColumns, in)
	if err := s.checkDatabase(ctx, p, db, "ONLINE", "only an online database can have its columns converted"); err != nil {
		return nil, err
	}
	if !p.Ready() {
		return p, nil
	}
	state, err := s.SQL.CollationState(ctx, db)
	if err != nil {
		return nil, err
	}
	switch {
	case state.ReadOnly:
		p.fail("access", "Database access", db+" is read-only")
	case state.UserAccess == "SINGLE_USER":
		p.fail("access", "Database access", db+" is SINGLE_USER: someone is maintaining it")
	default:
		p.ok("access", "Database access", db+" is "+cmp.Or(state.UserAccess, "MULTI_USER")+" and writable")
	}
	if state.CanAlter {
		p.ok("permission", "Permission", "the master user can alter the tables of "+db)
	} else {
		p.fail("permission", "Permission", "the master user cannot ALTER "+db+": only its owner (or a db_owner) can convert its columns")
	}
	if !p.Ready() {
		return p, nil
	}

	target := cmp.Or(in.Collation, state.Collation)
	p.Params.Collation = target
	info, found, err := s.SQL.Collation(ctx, target)
	if err != nil {
		return nil, err
	}
	if !found {
		p.fail("collation", "Collation", target+" is not a collation of this instance")
		return p, nil
	}
	if in.Collation == "" {
		p.ok("collation", "Collation", "to "+target+", the default of "+db)
	} else {
		p.ok("collation", "Collation", "to "+target)
	}

	cat, err := s.SQL.ConversionCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	refs, missing := resolveTables(cat, in.Tables)
	if len(missing) > 0 {
		p.fail("tables", "Tables", "not found in "+db+": "+listSome(missing, 10))
		return p, nil
	}
	units, refused, notes := planUnits(cat, target, refs)
	for i := range units {
		units[i].Database = db
	}
	explicit := len(in.Tables) > 0

	sum := &CollationPlan{Database: db, To: target, Units: len(units), Refused: refused}
	for _, u := range units {
		sum.Tables += len(u.Tables)
		sum.ForeignKeys += len(u.ForeignKeys)
		for _, t := range u.Tables {
			sum.Columns += len(t.Columns)
			sum.Indexes += len(t.Indexes)
		}
		sum.DataMB += unitSize(cat, u).mb
	}
	p.Collation = sum
	p.Plan.Subject = db

	p.ok("scope", "Scope", fmt.Sprintf("%d table(s), %d column(s), %d unit(s) → %s", sum.Tables, sum.Columns, sum.Units, target))
	if len(notes) > 0 {
		p.warn("fk-closure", "Foreign keys", "the foreign keys also bring in "+listSome(notes, 10))
	}
	if len(refused) > 0 {
		var why []string
		for _, r := range refused {
			why = append(why, r.Schema+"."+r.Name+" ("+r.Reason+")")
		}
		if explicit {
			p.fail("refused", "Refused tables", "cannot convert "+listSome(why, 10))
		} else {
			p.warn("refused", "Refused tables", "left as they are: "+listSome(why, 10))
		}
	}
	if sum.Tables > MaxConvertTables {
		p.fail("limit", "Limit", fmt.Sprintf("%d tables to convert, at most %d per plan: narrow it with tables", sum.Tables, MaxConvertTables))
	}
	if len(units) == 0 {
		p.warn("columns", "Columns", "nothing to convert: every convertible column is already "+target)
		return p, nil
	}
	if !p.Ready() {
		return p, nil
	}

	if in.SkipDataChecks {
		p.warn("data-checks", "Data checks", "skipped: uniqueness, references and code page are not verified (a failure rolls the unit back)")
	} else if err := s.convertDataChecks(ctx, p, db, info, units, cat); err != nil {
		return nil, err
	}
	s.convertSizeChecks(ctx, p, cat, units)
	if n, err := s.SQL.ActiveSessions(ctx, db); err != nil {
		return nil, err
	} else if n > 0 {
		p.warn("sessions", "Active sessions", fmt.Sprintf("%d other session(s) in %s: a unit waits at most %d s for its tables, then fails without changes", n, db, mssql.ConvertLockTimeoutMS/1000))
	} else {
		p.ok("sessions", "Active sessions", "no other session in "+db)
	}
	if n, err := s.SQL.ActiveTasks(ctx, db); err != nil {
		return nil, err
	} else if n > 0 {
		p.warn("tasks", "Native tasks", fmt.Sprintf("%d native task(s) running or queued on %s", n, db))
	} else {
		p.ok("tasks", "Native tasks", "no native task running")
	}
	if !p.Ready() {
		return p, nil
	}
	for _, u := range units {
		st := CollationStep{ID: CollConvert, Database: db, Collation: target, Unit: &u}
		if err := s.addCollation(p, st, convertTitle(u)); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// resolveTables finds the tables the user named: by exact name, else by a
// case-insensitive one when only one table matches.
func resolveTables(cat mssql.ConversionCatalog, in []mssql.TableRef) (found []mssql.TableRef, missing []string) {
	for _, want := range in {
		var exact, folded []mssql.TableRef
		for _, t := range cat.Tables {
			ref := mssql.TableRef{Schema: t.Schema, Name: t.Name}
			switch {
			case t.Schema == want.Schema && t.Name == want.Name:
				exact = append(exact, ref)
			case strings.EqualFold(t.Schema, want.Schema) && strings.EqualFold(t.Name, want.Name):
				folded = append(folded, ref)
			}
		}
		switch {
		case len(exact) == 1:
			found = append(found, exact[0])
		case len(folded) == 1:
			found = append(found, folded[0])
		default:
			missing = append(missing, want.Schema+"."+want.Name)
		}
	}
	return found, missing
}

func listSome(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(" and %d more", len(items)-n)
}

func convertTitle(u mssql.ConversionUnit) string {
	var names []string
	cols := 0
	for _, t := range u.Tables {
		if len(t.Columns) > 0 {
			names = append(names, "["+t.Schema+"].["+t.Name+"]")
			cols += len(t.Columns)
		}
	}
	list := names
	more := ""
	if len(names) > 3 {
		list, more = names[:3], fmt.Sprintf(" and %d more", len(names)-3)
	}
	return fmt.Sprintf("Convert %d column(s) of %s%s to %s", cols, strings.Join(list, ", "), more, u.Collation)
}

type sizeOf struct {
	mb   float64
	rows int64
}

func unitSize(cat mssql.ConversionCatalog, u mssql.ConversionUnit) sizeOf {
	var s sizeOf
	for _, t := range u.Tables {
		for _, ct := range cat.Tables {
			if ct.Schema == t.Schema && ct.Name == t.Name {
				s.mb += ct.ReservedMB
				s.rows += ct.Rows
			}
		}
	}
	return s
}

// convertSizeChecks warns about units that make the log grow: each one is
// rebuilt in one transaction. With the free storage of the instance, it
// also warns when twice the largest unit does not fit.
func (s *Session) convertSizeChecks(ctx context.Context, p *Prepared, cat mssql.ConversionCatalog, units []mssql.ConversionUnit) {
	var big []string
	var largest float64
	for _, u := range units {
		size := unitSize(cat, u)
		largest = max(largest, size.mb)
		if size.mb > convertWarnMB || size.rows > convertWarnRows {
			big = append(big, fmt.Sprintf("%s.%s (%.0f MB, %d rows)", u.Tables[0].Schema, u.Tables[0].Name, size.mb, size.rows))
		}
	}
	if len(big) > 0 {
		p.warn("size", "Size", "rebuilt in one transaction each, the log grows by about as much: "+listSome(big, 10)+"; run it in a maintenance window")
	} else {
		p.ok("size", "Size", fmt.Sprintf("the largest unit is %.0f MB", largest))
	}
	if s.FreeStorage == nil {
		return
	}
	free, err := s.FreeStorage(ctx)
	if err != nil {
		return // the free storage is a courtesy: CloudWatch may be out of reach
	}
	freeMB := float64(free) / (1 << 20)
	if 2*largest > freeMB {
		p.warn("storage", "Free storage", fmt.Sprintf("%.0f MB free, the largest unit may need about %.0f MB of log and data", freeMB, 2*largest))
	} else {
		p.ok("storage", "Free storage", fmt.Sprintf("%.0f MB free", freeMB))
	}
}

// convertDataChecks runs the data checks (collation design §6.2, step 5):
// unique keys that would collide, foreign keys that would lose their parent,
// varchar values the code page of the target cannot hold.
func (s *Session) convertDataChecks(ctx context.Context, p *Prepared, db string, target mssql.CollationInfo, units []mssql.ConversionUnit, cat mssql.ConversionCatalog) error {
	run := func(query string) (bool, error) {
		c, cancel := context.WithTimeout(ctx, dataCheckTimeout)
		defer cancel()
		return s.SQL.Exists(c, query)
	}
	type outcome struct{ bad, unknown []string }
	tally := func(o *outcome, what, query string) error {
		found, err := run(query)
		switch {
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			o.unknown = append(o.unknown, what)
		case found:
			o.bad = append(o.bad, what)
		}
		return nil
	}
	report := func(step, label string, o outcome, n int, problem string) {
		switch {
		case n == 0:
			p.ok(step, label, "nothing to check")
		case len(o.bad) > 0:
			p.fail(step, label, problem+": "+listSome(o.bad, 10))
		case len(o.unknown) > 0:
			p.warn(step, label, "not verified (timeout or error): "+listSome(o.unknown, 10))
		default:
			p.ok(step, label, fmt.Sprintf("%d checked", n))
		}
	}

	var uniq, refs, pages outcome
	nUniq, nRefs, nPages := 0, 0, 0
	codePages := map[string]int{}
	codePage := func(name string) (int, error) {
		if cp, ok := codePages[name]; ok {
			return cp, nil
		}
		info, _, err := s.SQL.Collation(ctx, name)
		if err != nil {
			return 0, err
		}
		codePages[name] = info.CodePage
		return info.CodePage, nil
	}
	for _, u := range units {
		for _, t := range u.Tables {
			ref := mssql.TableRef{Schema: t.Schema, Name: t.Name}
			converted := columnNames(t.Columns)
			for _, ix := range t.Indexes {
				if !ix.Unique {
					continue
				}
				var keys, conv []string
				for _, k := range ix.Keys {
					keys = append(keys, k.Name)
					if slices.Contains(converted, k.Name) {
						conv = append(conv, k.Name)
					}
				}
				if len(conv) == 0 {
					continue
				}
				nUniq++
				if err := tally(&uniq, t.Schema+"."+t.Name+" "+ix.Name, mssql.DuplicateKeyQuery(db, ref, keys, conv, u.Collation, ix.Filter)); err != nil {
					return err
				}
			}
			for _, c := range t.Columns {
				base, _, _ := strings.Cut(c.Type, "(")
				if base != "char" && base != "varchar" {
					continue
				}
				from, err := codePage(c.From)
				if err != nil {
					return err
				}
				if from == target.CodePage && !target.UTF8 {
					continue
				}
				nPages++
				maxBytes := 0
				for _, cc := range cat.Columns {
					if cc.Schema == t.Schema && cc.Table == t.Name && cc.Column == c.Name {
						maxBytes = cc.MaxLength
					}
				}
				if err := tally(&pages, t.Schema+"."+t.Name+"."+c.Name, mssql.CodePageLossQuery(db, ref, c, maxBytes)); err != nil {
					return err
				}
			}
		}
		for _, fk := range u.ForeignKeys {
			if fk.Disabled || fk.NotTrusted {
				continue
			}
			child := columnsOf(u, fk.Schema, fk.Table)
			parent := columnsOf(u, fk.RefSchema, fk.RefTable)
			var conv []string
			for i, c := range fk.Columns {
				if slices.Contains(child, c) || slices.Contains(parent, fk.RefColumns[i]) {
					conv = append(conv, c)
				}
			}
			if len(conv) == 0 {
				continue
			}
			nRefs++
			if err := tally(&refs, fk.Schema+"."+fk.Table+" "+fk.Name, mssql.OrphanQuery(db, fk, conv, u.Collation)); err != nil {
				return err
			}
		}
	}
	report("uniqueness", "Unique keys", uniq, nUniq, "values that differ only by case or accent would collide")
	report("references", "Foreign key values", refs, nRefs, "rows would lose their parent under the new collation")
	report("code-page", "Code page", pages, nPages, "values the code page of "+target.Name+" cannot hold (or that no longer fit)")
	return nil
}

func columnNames(cols []mssql.ColumnChange) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names
}

func columnsOf(u mssql.ConversionUnit, schema, table string) []string {
	for _, t := range u.Tables {
		if t.Schema == schema && t.Name == table {
			return columnNames(t.Columns)
		}
	}
	return nil
}

// errUnitState means a unit is neither converted nor as the plan read it.
var errUnitState = errors.New("unit in an unexpected state (partly converted outside Dialga): nothing done")

// runConvert runs one unit of a convert-columns (collation design §6.2). It
// reads the catalog first: a unit already converted (an interrupted job, a
// second run) is a no-op; one with every column still as planned runs; any
// other state is refused.
func (s *Session) runConvert(ctx context.Context, st CollationStep) (string, error) {
	u := st.Unit
	if u == nil {
		return "", errors.New("the convert step has no unit")
	}
	cat, err := s.SQL.ConversionCatalog(ctx, st.Database)
	if err != nil {
		return "", err
	}
	tables := 0
	allTo, allFrom := true, true
	for _, t := range u.Tables {
		id, ok := tableID(cat, t.Schema, t.Name)
		if !ok {
			return "", fmt.Errorf("table %s.%s is gone: %w", t.Schema, t.Name, errUnitState)
		}
		if len(t.Columns) > 0 {
			tables++
		}
		for _, c := range t.Columns {
			i := slices.IndexFunc(cat.Columns, func(cc mssql.CharColumn) bool { return cc.ObjectID == id && cc.Column == c.Name })
			if i < 0 {
				return "", fmt.Errorf("column %s of %s.%s is gone: %w", c.Name, t.Schema, t.Name, errUnitState)
			}
			now := cat.Columns[i].Collation
			allTo = allTo && now == c.To
			allFrom = allFrom && now == c.From
		}
		allTo = allTo && unitObjectsExist(cat, id, t)
	}
	for _, fk := range u.ForeignKeys {
		allTo = allTo && slices.ContainsFunc(cat.ForeignKeys, func(f mssql.CatalogFK) bool {
			return f.Def.Name == fk.Name && f.Def.Schema == fk.Schema && f.Def.Table == fk.Table
		})
	}
	cols := 0
	for _, t := range u.Tables {
		cols += len(t.Columns)
	}
	switch {
	case allTo:
		return fmt.Sprintf("already converted: %d column(s) of %d table(s) are %s", cols, tables, u.Collation), nil
	case !allFrom:
		return "", errUnitState
	}
	if err := s.SQL.Exec(ctx, mssql.ConvertUnit(*u)); err != nil {
		if mssql.IsLockTimeout(err) {
			return "", fmt.Errorf("blocked by other sessions for %d s: nothing changed, retry or resume (%w)", mssql.ConvertLockTimeoutMS/1000, err)
		}
		return "", fmt.Errorf("%w (the unit was rolled back: nothing changed)", err)
	}
	return fmt.Sprintf("%d column(s) of %d table(s) converted to %s", cols, tables, u.Collation), nil
}

func tableID(cat mssql.ConversionCatalog, schema, name string) (int64, bool) {
	for id, t := range cat.Tables {
		if t.Schema == schema && t.Name == name {
			return id, true
		}
	}
	return 0, false
}

// unitObjectsExist reports whether the indexes, constraints and statistics
// the unit creates again on table id are there.
func unitObjectsExist(cat mssql.ConversionCatalog, id int64, t mssql.TableConversion) bool {
	for _, ix := range t.Indexes {
		if !slices.ContainsFunc(cat.Indexes, func(c mssql.CatalogIndex) bool { return c.ObjectID == id && c.Def.Name == ix.Name }) {
			return false
		}
	}
	for _, c := range t.Checks {
		if !slices.ContainsFunc(cat.Checks, func(cc mssql.CatalogCheck) bool { return cc.ParentID == id && cc.Def.Name == c.Name }) {
			return false
		}
	}
	for _, d := range t.Defaults {
		if !slices.ContainsFunc(cat.Defaults, func(cd mssql.CatalogDefault) bool { return cd.ParentID == id && cd.Def.Name == d.Name }) {
			return false
		}
	}
	for _, st := range t.Stats {
		if st.Recreate && !slices.ContainsFunc(cat.Stats, func(cs mssql.CatalogStats) bool { return cs.ObjectID == id && cs.Def.Name == st.Name }) {
			return false
		}
	}
	return true
}
