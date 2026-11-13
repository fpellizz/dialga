package ops

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// planUnits is the plan of a conversion: the units to convert, the tables
// refused and the notes for the checks (collation design §6.2, step 4). It
// is pure: everything it needs is in cat.
//
// The columns to convert are those of tables (all tables when empty) whose
// collation is not target; the foreign keys pull in the column on their
// other side until nothing changes. What depends on a converted column is
// dropped and created again: indexes (all of them when the clustered one
// is), foreign keys (also those that use a dropped unique index), CHECK and
// DEFAULT constraints, statistics, with the extended properties of the
// dropped indexes and constraints. Tables linked by a dropped foreign key
// form a unit: one transaction. A unit with a table that cannot be rebuilt
// exactly is refused whole. notes are the tables a unit converts that were
// not asked for (or, for the whole database, that only have foreign keys
// rebuilt).
func planUnits(cat mssql.ConversionCatalog, target string, tables []mssql.TableRef) (units []mssql.ConversionUnit, refused []RefusedTable, notes []string) {
	cp := planConversion(cat, target, tables)
	return cp.units, cp.refused, cp.notes
}

// conversionPlan is planUnits with lost: the options of the objects the
// units create again that they do not keep (a warning of the plan).
type conversionPlan struct {
	units   []mssql.ConversionUnit
	refused []RefusedTable
	notes   []string
	lost    []string
}

func planConversion(cat mssql.ConversionCatalog, target string, tables []mssql.TableRef) conversionPlan {
	pl := newPlanner(cat, target)
	selected := map[int64]bool{}
	for _, ref := range tables {
		for id, t := range cat.Tables {
			if t.Schema == ref.Schema && t.Name == ref.Name {
				selected[id] = true
			}
		}
	}
	pl.seed(func(id int64) bool { return len(tables) == 0 || selected[id] })
	pl.closeOverForeignKeys()
	pl.affectIndexes()
	pl.dropForeignKeys()
	pl.dependents()
	pl.analyse()
	return pl.units(func(id int64) bool {
		if len(tables) > 0 {
			return selected[id]
		}
		return pl.hasColumns(id)
	})
}

// analyse is what follows the choice of the columns: the foreign keys to
// the other side, the indexes, constraints and statistics to rebuild, the
// tables to refuse.
func (pl *planner) analyse() {
	pl.closeOverForeignKeys()
	pl.affectIndexes()
	pl.dropForeignKeys()
	pl.dependents()
	for id := range pl.scope {
		pl.refuseTable(id)
	}
}

// replanUnit plans u again on cat, seeded with exactly its columns: what
// the unit must be now. reason says why it no longer is one unit.
func replanUnit(cat mssql.ConversionCatalog, u mssql.ConversionUnit) (mssql.ConversionUnit, string) {
	pl := newPlanner(cat, u.Collation)
	for _, t := range u.Tables {
		id, ok := tableID(cat, t.Schema, t.Name)
		if !ok {
			return u, "table " + t.Schema + "." + t.Name + " is gone"
		}
		for _, c := range t.Columns {
			i := slices.IndexFunc(cat.Columns, func(cc mssql.CharColumn) bool { return cc.ObjectID == id && cc.Column == c.Name })
			if i < 0 {
				return u, "column " + c.Name + " of " + t.Schema + "." + t.Name + " is gone"
			}
			if !pl.add(mssql.ColumnKey{ObjectID: id, ColumnID: cat.Columns[i].ColumnID}) {
				return u, "column " + c.Name + " of " + t.Schema + "." + t.Name + " can no longer be converted"
			}
		}
	}
	pl.analyse()
	cp := pl.units(func(int64) bool { return true })
	switch {
	case len(cp.refused) > 0:
		r := cp.refused[0]
		return u, r.Schema + "." + r.Name + ": " + r.Reason
	case len(cp.units) != 1:
		return u, fmt.Sprintf("the tables now form %d units", len(cp.units))
	}
	now := cp.units[0]
	now.Database = u.Database
	return now, ""
}

// sameUnit reports whether a and b convert the same columns and drop and
// create the same objects, with the same definitions: their statements are
// the same, but for the auto-created statistics they only drop (SQL Server
// creates and drops them at any time).
func sameUnit(a, b mssql.ConversionUnit) bool {
	return mssql.ConvertUnit(withoutAutoStats(a)) == mssql.ConvertUnit(withoutAutoStats(b))
}

func withoutAutoStats(u mssql.ConversionUnit) mssql.ConversionUnit {
	tables := make([]mssql.TableConversion, len(u.Tables))
	for i, t := range u.Tables {
		t.Stats = slices.DeleteFunc(slices.Clone(t.Stats), func(s mssql.StatsDef) bool { return !s.Recreate })
		tables[i] = t
	}
	u.Tables = tables
	return u
}

type planner struct {
	cat    mssql.ConversionCatalog
	target string
	cols   map[mssql.ColumnKey]int // index in cat.Columns
	masked map[mssql.ColumnKey]bool
	conv   map[mssql.ColumnKey]bool
	// reasons are the refused tables and the first reason found.
	reasons map[int64]string
	// scope are the tables a unit touches.
	scope map[int64]bool
	// affected are the indexes to drop and create again, by table and id.
	affected map[[2]int64]bool
	fks      map[int64]bool // dropped foreign keys, by object id
	checks   map[int64]bool // dropped checks, by object id
	defaults map[int64]bool
	stats    map[[2]int64]bool
}

func newPlanner(cat mssql.ConversionCatalog, target string) *planner {
	pl := &planner{cat: cat, target: target, cols: map[mssql.ColumnKey]int{}, masked: map[mssql.ColumnKey]bool{},
		conv: map[mssql.ColumnKey]bool{}, reasons: map[int64]string{}, scope: map[int64]bool{},
		affected: map[[2]int64]bool{}, fks: map[int64]bool{}, checks: map[int64]bool{}, defaults: map[int64]bool{},
		stats: map[[2]int64]bool{}}
	for i, c := range cat.Columns {
		pl.cols[mssql.ColumnKey{ObjectID: c.ObjectID, ColumnID: c.ColumnID}] = i
	}
	for _, k := range cat.Masked {
		pl.masked[k] = true
	}
	return pl
}

func (pl *planner) refuse(id int64, reason string) {
	pl.scope[id] = true
	if _, done := pl.reasons[id]; !done {
		pl.reasons[id] = reason
	}
}

// add converts column k, or refuses its table when the column cannot be.
func (pl *planner) add(k mssql.ColumnKey) bool {
	i, ok := pl.cols[k]
	if !ok || pl.conv[k] {
		return false
	}
	c := pl.cat.Columns[i]
	if _, ok := pl.cat.Tables[c.ObjectID]; !ok {
		return false
	}
	switch {
	case c.Collation == pl.target:
		return false
	case c.Unsupported() != "":
		pl.refuse(c.ObjectID, "column "+c.Column+": "+c.Unsupported())
		return false
	case pl.masked[k]:
		pl.refuse(c.ObjectID, "column "+c.Column+": dynamic data masking is not supported")
		return false
	}
	pl.conv[k] = true
	pl.scope[c.ObjectID] = true
	return true
}

func (pl *planner) seed(wanted func(int64) bool) {
	for _, c := range pl.cat.Columns {
		if c.Computed || !wanted(c.ObjectID) {
			continue
		}
		pl.add(mssql.ColumnKey{ObjectID: c.ObjectID, ColumnID: c.ColumnID})
	}
}

// closeOverForeignKeys converts the other side of every foreign key with a
// converted column: both sides need the same collation.
func (pl *planner) closeOverForeignKeys() {
	for changed := true; changed; {
		changed = false
		for _, fk := range pl.cat.ForeignKeys {
			for i := range fk.ParentIDs {
				p := mssql.ColumnKey{ObjectID: fk.ParentID, ColumnID: fk.ParentIDs[i]}
				r := mssql.ColumnKey{ObjectID: fk.RefID, ColumnID: fk.RefIDs[i]}
				switch {
				case pl.conv[p] && !pl.conv[r]:
					if !pl.add(r) && pl.unconvertible(r) {
						pl.refuse(fk.ParentID, "its foreign key "+fk.Def.Name+" references a column that cannot be converted")
					}
					changed = changed || pl.conv[r]
				case pl.conv[r] && !pl.conv[p]:
					if !pl.add(p) && pl.unconvertible(p) {
						pl.refuse(fk.RefID, "the foreign key "+fk.Def.Name+" of "+fk.Def.Schema+"."+fk.Def.Table+" uses a column that cannot be converted")
					}
					changed = changed || pl.conv[p]
				}
			}
		}
	}
}

// unconvertible reports whether k has a collation other than the target but
// cannot be converted (its table is refused by add).
func (pl *planner) unconvertible(k mssql.ColumnKey) bool {
	i, ok := pl.cols[k]
	return ok && !pl.conv[k] && pl.cat.Columns[i].Collation != pl.target
}

func (pl *planner) converted(obj int64, ids []int) bool {
	return slices.ContainsFunc(ids, func(id int) bool { return pl.conv[mssql.ColumnKey{ObjectID: obj, ColumnID: id}] })
}

// affectIndexes finds the indexes that use a converted column, every index
// of a table whose clustered index does, and refuses what cannot be rebuilt.
func (pl *planner) affectIndexes() {
	clustered := map[int64]bool{}
	for _, ix := range pl.cat.Indexes {
		// A filter that names a converted column blocks ALTER COLUMN too.
		if pl.converted(ix.ObjectID, ix.KeyIDs) || pl.converted(ix.ObjectID, ix.IncludedIDs) ||
			(ix.Def.Filter != "" && pl.mentions(ix.ObjectID, ix.Def.Filter)) {
			pl.affected[[2]int64{ix.ObjectID, int64(ix.IndexID)}] = true
			if ix.IndexID == 1 {
				clustered[ix.ObjectID] = true
			}
		}
	}
	for _, ix := range pl.cat.Indexes {
		if clustered[ix.ObjectID] {
			pl.affected[[2]int64{ix.ObjectID, int64(ix.IndexID)}] = true
		}
	}
	for _, ix := range pl.cat.Indexes {
		name := "index " + ix.Def.Name
		if ix.FilterUnreadable && pl.hasColumns(ix.ObjectID) {
			// It may name a converted column: it cannot be told nor rebuilt.
			pl.refuse(ix.ObjectID, name+": filter not readable (VIEW DEFINITION)")
		}
		if !pl.affected[[2]int64{ix.ObjectID, int64(ix.IndexID)}] {
			continue
		}
		switch {
		case ix.Type != 1 && ix.Type != 2:
			if clustered[ix.ObjectID] && !pl.converted(ix.ObjectID, ix.KeyIDs) && !pl.converted(ix.ObjectID, ix.IncludedIDs) {
				pl.refuse(ix.ObjectID, name+" (XML, spatial or columnstore) would have to be rebuilt with the clustered index: not supported")
			} else {
				pl.refuse(ix.ObjectID, name+" is an XML, spatial, columnstore or hash index: not supported")
			}
		case ix.Hypothetical:
			pl.refuse(ix.ObjectID, name+" is hypothetical (tuning advisor): drop it first")
		case ix.Disabled:
			pl.refuse(ix.ObjectID, name+" is disabled: rebuild or drop it first")
		case ix.DataSpaceType == "PS":
			pl.refuse(ix.ObjectID, name+" is partitioned: not supported")
		case ix.DataSpaceType != "FG":
			pl.refuse(ix.ObjectID, name+" is not on a filegroup: not supported")
		case ix.Compressions > 1 || !slices.Contains([]string{"NONE", "ROW", "PAGE"}, ix.Def.Compression):
			pl.refuse(ix.ObjectID, name+" has a compression that cannot be kept ("+ix.Def.Compression+")")
		case ix.Def.Kind == mssql.IndexPrimaryKey && pl.cat.Tables[ix.ObjectID].ChangeTracking:
			pl.refuse(ix.ObjectID, "change tracking needs the primary key "+ix.Def.Name+": disable change tracking on the table first")
		}
	}
}

// dropForeignKeys finds the foreign keys to drop: those with a converted
// column, and those whose unique index on the referenced table is rebuilt.
func (pl *planner) dropForeignKeys() {
	for _, fk := range pl.cat.ForeignKeys {
		if pl.converted(fk.ParentID, fk.ParentIDs) || pl.converted(fk.RefID, fk.RefIDs) ||
			pl.affected[[2]int64{fk.RefID, int64(fk.KeyIndexID)}] {
			pl.fks[fk.ObjectID] = true
			pl.scope[fk.ParentID], pl.scope[fk.RefID] = true, true
		}
	}
}

// mentions reports whether a definition names a converted column of obj
// as [column] (conservative: any mention counts).
func (pl *planner) mentions(obj int64, definition string) bool {
	def := strings.ToLower(definition)
	for k := range pl.conv {
		if k.ObjectID == obj && strings.Contains(def, "["+strings.ToLower(strings.ReplaceAll(pl.cat.Columns[pl.cols[k]].Column, "]", "]]"))+"]") {
			return true
		}
	}
	return false
}

// dependents finds the constraints and statistics to drop and refuses the
// tables with dependents that cannot be dropped and created again.
func (pl *planner) dependents() {
	for _, ft := range pl.cat.FullText {
		if pl.converted(ft.ObjectID, ft.ColumnIDs) || pl.affected[[2]int64{ft.ObjectID, int64(ft.KeyIndexID)}] {
			pl.refuse(ft.ObjectID, "its full-text index uses a converted column or index: not supported")
		}
	}
	for _, cc := range pl.cat.Computed {
		if pl.converted(cc.ObjectID, cc.ColumnIDs) || pl.mentions(cc.ObjectID, cc.Definition) {
			pl.refuse(cc.ObjectID, "computed column "+cc.Name+" reads a converted column: not supported")
		}
	}
	// A schema-bound module lists the columns it reads, plus a row for the
	// whole table (column 0). It blocks ALTER COLUMN only on the columns it
	// reads (TestIntegrationConvertColumnsRefusals); a module known only by
	// its whole-table row is taken as reading every column.
	byColumn := map[string]bool{}
	for _, ref := range pl.cat.SchemaBound {
		if ref.ColumnID != 0 {
			byColumn[fmt.Sprint(ref.ReferencedID, ref.Schema, "\x00", ref.Name)] = true
		}
	}
	for _, ref := range pl.cat.SchemaBound {
		if !pl.hasColumns(ref.ReferencedID) {
			continue
		}
		what := "schema-bound " + strings.ToLower(strings.ReplaceAll(ref.Type, "_", " ")) + " " + ref.Schema + "." + ref.Name
		whole := ref.ColumnID == 0 && !byColumn[fmt.Sprint(ref.ReferencedID, ref.Schema, "\x00", ref.Name)]
		if whole || pl.conv[mssql.ColumnKey{ObjectID: ref.ReferencedID, ColumnID: ref.ColumnID}] {
			pl.refuse(ref.ReferencedID, "referenced by the "+what+": drop it and create it again by hand")
		}
	}
	for _, c := range pl.cat.Checks {
		if !pl.converted(c.ParentID, append(slices.Clone(c.ColumnIDs), c.ParentColumnID)) &&
			(c.ParentColumnID != 0 || !pl.mentions(c.ParentID, c.Def.Definition)) {
			continue
		}
		pl.checks[c.ObjectID] = true
		if c.NoDefinition {
			pl.refuse(c.ParentID, "CHECK "+c.Def.Name+": definition not readable (VIEW DEFINITION)")
		}
	}
	for _, d := range pl.cat.Defaults {
		if !pl.conv[mssql.ColumnKey{ObjectID: d.ParentID, ColumnID: d.ColumnID}] {
			continue
		}
		pl.defaults[d.ObjectID] = true
		if d.NoDefinition {
			pl.refuse(d.ParentID, "DEFAULT "+d.Def.Name+": definition not readable (VIEW DEFINITION)")
		}
	}
	for _, s := range pl.cat.Stats {
		if s.FilterUnreadable && pl.hasColumns(s.ObjectID) {
			pl.refuse(s.ObjectID, "statistic "+s.Def.Name+": filter not readable (VIEW DEFINITION)")
		}
		if !pl.converted(s.ObjectID, s.ColumnIDs) && (s.Def.Filter == "" || !pl.mentions(s.ObjectID, s.Def.Filter)) {
			continue
		}
		pl.stats[[2]int64{s.ObjectID, int64(s.StatsID)}] = true
		if s.Incremental {
			pl.refuse(s.ObjectID, "statistic "+s.Def.Name+" is incremental: not supported")
		}
	}
	for _, e := range pl.cat.ExtendedProperties {
		obj, what, ok := pl.droppedObject(e)
		if !ok {
			continue
		}
		switch strings.ToLower(e.BaseType) {
		case "char", "varchar", "nchar", "nvarchar":
		default:
			pl.refuse(obj, "extended property "+e.Name+" on "+what+" is not text: not supported")
		}
	}
}

// droppedObject finds the table and the name of the dropped index or
// constraint e belongs to.
func (pl *planner) droppedObject(e mssql.CatalogExtendedProperty) (table int64, name string, ok bool) {
	switch e.Class {
	case 7:
		for _, ix := range pl.cat.Indexes {
			if ix.ObjectID == e.MajorID && ix.IndexID == e.MinorID && pl.affected[[2]int64{ix.ObjectID, int64(ix.IndexID)}] {
				return ix.ObjectID, ix.Def.Name, true
			}
		}
	case 1:
		for _, ix := range pl.cat.Indexes {
			if ix.ConstraintID != 0 && ix.ConstraintID == e.MajorID && pl.affected[[2]int64{ix.ObjectID, int64(ix.IndexID)}] {
				return ix.ObjectID, ix.Def.Name, true
			}
		}
		for _, fk := range pl.cat.ForeignKeys {
			if fk.ObjectID == e.MajorID && pl.fks[fk.ObjectID] {
				return fk.ParentID, fk.Def.Name, true
			}
		}
		for _, c := range pl.cat.Checks {
			if c.ObjectID == e.MajorID && pl.checks[c.ObjectID] {
				return c.ParentID, c.Def.Name, true
			}
		}
		for _, d := range pl.cat.Defaults {
			if d.ObjectID == e.MajorID && pl.defaults[d.ObjectID] {
				return d.ParentID, d.Def.Name, true
			}
		}
	}
	return 0, "", false
}

func (pl *planner) hasColumns(id int64) bool {
	for k := range pl.conv {
		if k.ObjectID == id {
			return true
		}
	}
	return false
}

// refuseTable refuses a table of the scope for what it is, whatever its
// columns.
func (pl *planner) refuseTable(id int64) {
	t, ok := pl.cat.Tables[id]
	if !ok {
		return
	}
	switch {
	case t.MemoryOptimized:
		pl.refuse(id, "memory-optimized table: not supported")
	case t.Temporal != 0:
		pl.refuse(id, "system-versioned (temporal) table or its history: not supported")
	case t.Replicated:
		pl.refuse(id, "replicated table: not supported")
	case t.CDC:
		pl.refuse(id, "tracked by change data capture: not supported")
	case t.FileTable:
		pl.refuse(id, "FileTable: not supported")
	case t.Graph:
		pl.refuse(id, "graph table: not supported")
	case t.Ledger:
		pl.refuse(id, "ledger table: not supported")
	}
}

func tableKey(t mssql.CatalogTable) string { return t.Schema + "\x00" + t.Name }

// units groups the scope by the dropped foreign keys (union-find) and builds
// the units; asked reports whether a table was asked for (notes).
func (pl *planner) units(asked func(int64) bool) (cp conversionPlan) {
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if parent[x] == x {
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	ids := make([]int64, 0, len(pl.scope))
	for id := range pl.scope {
		if _, ok := pl.cat.Tables[id]; ok {
			parent[id] = id
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b int64) int { return strings.Compare(tableKey(pl.cat.Tables[a]), tableKey(pl.cat.Tables[b])) })
	for _, fk := range pl.cat.ForeignKeys {
		if pl.fks[fk.ObjectID] {
			if _, ok := parent[fk.ParentID]; !ok {
				continue
			}
			if _, ok := parent[fk.RefID]; !ok {
				continue
			}
			a, b := find(fk.ParentID), find(fk.RefID)
			if a != b {
				parent[a] = b
			}
		}
	}
	groups := map[int64][]int64{}
	var roots []int64
	for _, id := range ids { // sorted: each group in name order, groups by first table
		r := find(id)
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], id)
	}
	for _, r := range roots {
		group := groups[r]
		var bad int64 = -1
		for _, id := range group {
			if _, ok := pl.reasons[id]; ok {
				bad = id
				break
			}
		}
		if bad >= 0 {
			bt := pl.cat.Tables[bad]
			for _, id := range group {
				t := pl.cat.Tables[id]
				reason, ok := pl.reasons[id]
				if !ok {
					reason = "linked by foreign key to " + bt.Schema + "." + bt.Name + ": " + pl.reasons[bad]
				}
				cp.refused = append(cp.refused, RefusedTable{Schema: t.Schema, Name: t.Name, Reason: reason})
			}
			continue
		}
		u := pl.unit(group, &cp.lost)
		if len(u.Tables) == 0 {
			continue
		}
		cp.units = append(cp.units, u)
		for _, id := range group {
			if !asked(id) {
				t := pl.cat.Tables[id]
				cp.notes = append(cp.notes, t.Schema+"."+t.Name)
			}
		}
	}
	return cp
}

// unit builds the unit of a group of tables (in name order); lost gets the
// options of its objects that it does not keep.
func (pl *planner) unit(group []int64, lost *[]string) mssql.ConversionUnit {
	u := mssql.ConversionUnit{Collation: pl.target}
	in := map[int64]bool{}
	for _, id := range group {
		in[id] = true
	}
	for _, id := range group {
		t := pl.cat.Tables[id]
		tc := mssql.TableConversion{Schema: t.Schema, Name: t.Name}
		for _, c := range pl.cat.Columns { // in column order
			if c.ObjectID == id && pl.conv[mssql.ColumnKey{ObjectID: id, ColumnID: c.ColumnID}] {
				tc.Columns = append(tc.Columns, mssql.ColumnChange{Name: c.Column, Type: c.SQLType(), Nullable: c.Nullable, From: c.Collation, To: pl.target})
			}
		}
		for _, ix := range pl.cat.Indexes {
			if ix.ObjectID == id && pl.affected[[2]int64{id, int64(ix.IndexID)}] {
				tc.Indexes = append(tc.Indexes, ix.Def)
				if ix.OptimizeSequentialKey {
					*lost = append(*lost, "OPTIMIZE_FOR_SEQUENTIAL_KEY of index "+t.Schema+"."+t.Name+"."+ix.Def.Name)
				}
			}
		}
		for _, c := range pl.cat.Checks {
			if c.ParentID == id && pl.checks[c.ObjectID] {
				tc.Checks = append(tc.Checks, c.Def)
			}
		}
		for _, d := range pl.cat.Defaults {
			if d.ParentID == id && pl.defaults[d.ObjectID] {
				tc.Defaults = append(tc.Defaults, d.Def)
			}
		}
		for _, s := range pl.cat.Stats {
			if s.ObjectID == id && pl.stats[[2]int64{id, int64(s.StatsID)}] {
				tc.Stats = append(tc.Stats, s.Def)
				if s.Def.Recreate && s.PersistedSample {
					*lost = append(*lost, "the persisted sample of statistic "+t.Schema+"."+t.Name+"."+s.Def.Name)
				}
				if s.Def.Recreate && s.AutoDrop {
					*lost = append(*lost, "AUTO_DROP of statistic "+t.Schema+"."+t.Name+"."+s.Def.Name)
				}
			}
		}
		for _, e := range pl.cat.ExtendedProperties {
			if obj, name, ok := pl.droppedObject(e); ok && obj == id {
				level2 := mssql.Level2Constraint
				if e.Class == 7 {
					level2 = mssql.Level2Index
				}
				tc.ExtendedProperties = append(tc.ExtendedProperties, mssql.ExtendedPropertyDef{Target: name, Level2: level2, Name: e.Name, Value: e.Value})
				if b := strings.ToLower(e.BaseType); b == "char" || b == "varchar" {
					*lost = append(*lost, "the "+b+" type of extended property "+e.Name+" of "+t.Schema+"."+t.Name+"."+name+" (added again as nvarchar)")
				}
			}
		}
		slices.SortFunc(tc.Indexes, func(a, b mssql.IndexDef) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(tc.Checks, func(a, b mssql.CheckDef) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(tc.Defaults, func(a, b mssql.DefaultDef) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(tc.Stats, func(a, b mssql.StatsDef) int { return strings.Compare(a.Name, b.Name) })
		slices.SortFunc(tc.ExtendedProperties, func(a, b mssql.ExtendedPropertyDef) int {
			return cmp.Or(strings.Compare(a.Level2, b.Level2), strings.Compare(a.Target, b.Target), strings.Compare(a.Name, b.Name))
		})
		u.Tables = append(u.Tables, tc)
	}
	for _, fk := range pl.cat.ForeignKeys {
		if pl.fks[fk.ObjectID] && in[fk.ParentID] {
			u.ForeignKeys = append(u.ForeignKeys, fk.Def)
		}
	}
	slices.SortFunc(u.ForeignKeys, func(a, b mssql.ForeignKeyDef) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Schema, b.Schema), strings.Compare(a.Table, b.Table))
	})
	return u
}
