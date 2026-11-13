package ops

import (
	"context"
	"fmt"
	"sort"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// maxCollationColumns bounds the columns a DatabaseCollationView lists
// (the ones whose collation is not the default of the database).
const maxCollationColumns = 2000

// CollationOverview reads the collation view of the target: the server,
// tempdb and every user database (read-only).
func (s *Session) CollationOverview(ctx context.Context) (CollationOverview, error) {
	server, err := s.SQL.ServerCollation(ctx)
	if err != nil {
		return CollationOverview{}, err
	}
	dbs, err := s.SQL.DatabaseCollations(ctx)
	if err != nil {
		return CollationOverview{}, err
	}
	o := CollationOverview{Target: s.Name, Instance: s.Target.Instance, Server: server.Server,
		Tempdb: server.Tempdb, ServerFixed: true, Databases: []DatabaseCollationSummary{}}
	for _, d := range dbs {
		mismatch, risk := collationFlags(d.Collation, d.Containment, server)
		o.Databases = append(o.Databases, DatabaseCollationSummary{Name: d.Name, State: d.State,
			Collation: d.Collation, Containment: d.Containment, MismatchServer: mismatch, TempTableRisk: risk})
	}
	return o, nil
}

// collationFlags says whether a database collation differs from the
// server's and whether its temp tables clash with it: tempdb has the server
// collation, and only a contained database avoids the clash. A database
// whose collation is unknown (not readable) is flagged for nothing.
func collationFlags(collation, containment string, server mssql.ServerCollation) (mismatch, tempTableRisk bool) {
	if collation == "" {
		return false, false
	}
	return collation != server.Server, collation != server.Tempdb && containment != "PARTIAL"
}

// DatabaseCollation reads the collation view of db (read-only):
// mssql.ErrNoDatabase when it does not exist, ErrNotOnline when it is not
// online.
func (s *Session) DatabaseCollation(ctx context.Context, db string) (DatabaseCollationView, error) {
	st, err := s.SQL.CollationState(ctx, db)
	switch {
	case err != nil:
		return DatabaseCollationView{}, err
	case !st.Exists:
		return DatabaseCollationView{}, fmt.Errorf("%s: %w", db, mssql.ErrNoDatabase)
	case st.State != "ONLINE":
		return DatabaseCollationView{}, fmt.Errorf("%s is %s: %w", db, st.State, ErrNotOnline)
	}
	server, err := s.SQL.ServerCollation(ctx)
	if err != nil {
		return DatabaseCollationView{}, err
	}
	cols, err := s.SQL.CharColumns(ctx, db)
	if err != nil {
		return DatabaseCollationView{}, err
	}
	v := DatabaseCollationView{Target: s.Name, Instance: s.Target.Instance, Database: db,
		Collation: st.Collation, Server: server.Server, Tempdb: server.Tempdb, Containment: st.Containment,
		CharColumns: len(cols), Groups: []CollationGroup{}, Columns: []ColumnView{}}
	v.MismatchServer, v.TempTableRisk = collationFlags(st.Collation, st.Containment, server)

	type tally struct {
		columns int
		tables  map[mssql.TableRef]struct{}
	}
	groups := map[string]*tally{st.Collation: {tables: map[mssql.TableRef]struct{}{}}}
	for _, c := range cols {
		g := groups[c.Collation]
		if g == nil {
			g = &tally{tables: map[mssql.TableRef]struct{}{}}
			groups[c.Collation] = g
		}
		g.columns++
		g.tables[mssql.TableRef{Schema: c.Schema, Name: c.Table}] = struct{}{}
		if c.Collation == st.Collation {
			continue
		}
		if len(v.Columns) == maxCollationColumns {
			v.Truncated = true
			continue
		}
		v.Columns = append(v.Columns, ColumnView{CharColumn: c, Unsupported: c.Unsupported()})
	}
	for name, g := range groups {
		grp := CollationGroup{Collation: name, Default: name == st.Collation, Columns: g.columns, Tables: len(g.tables)}
		if info, found, err := s.SQL.Collation(ctx, name); err != nil {
			return DatabaseCollationView{}, err
		} else if found {
			grp.CodePage = info.CodePage
		}
		v.Groups = append(v.Groups, grp)
	}
	sort.Slice(v.Groups, func(i, j int) bool {
		a, b := v.Groups[i], v.Groups[j]
		switch {
		case a.Default != b.Default:
			return a.Default
		case a.Columns != b.Columns:
			return a.Columns > b.Columns
		}
		return a.Collation < b.Collation
	})
	return v, nil
}

// Collations lists the collations of the instance (read-only).
func (s *Session) Collations(ctx context.Context) ([]mssql.CollationInfo, error) {
	return s.SQL.Collations(ctx)
}
