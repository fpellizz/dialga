package ops

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

const (
	fromColl = "SQL_Latin1_General_CP1_CI_AS"
	toColl   = "Latin1_General_100_CS_AS"
)

// catBuilder builds a ConversionCatalog: tables get ids 100, 200...; their
// columns ids 1, 2... in the order given. "int:" columns have no collation
// (they are not in Columns) but take an id.
type catBuilder struct {
	cat  mssql.ConversionCatalog
	next int64
	ids  map[string]int64
	cols map[string]int // "table.column" -> column id
	obj  int64
}

func newCatalog() *catBuilder {
	return &catBuilder{cat: mssql.ConversionCatalog{Major: 16, Tables: map[int64]mssql.CatalogTable{}}, next: 100,
		ids: map[string]int64{}, cols: map[string]int{}, obj: 10_000}
}

func (b *catBuilder) table(name string, cols ...string) *catBuilder {
	return b.tableWith(name, mssql.CatalogTable{}, cols...)
}

func (b *catBuilder) tableWith(name string, t mssql.CatalogTable, cols ...string) *catBuilder {
	id := b.next
	b.next += 100
	t.Schema, t.Name = "dbo", name
	t.ReservedMB, t.Rows = 1, 10
	b.cat.Tables[id] = t
	b.ids[name] = id
	for i, spec := range cols {
		b.cols[name+"."+strings.TrimPrefix(spec, "int:")] = i + 1
		if strings.HasPrefix(spec, "int:") {
			continue
		}
		c := mssql.CharColumn{ObjectID: id, ColumnID: i + 1, Schema: "dbo", Table: name, Column: spec, Type: "nvarchar",
			BaseType: "nvarchar", MaxLength: 100, Collation: fromColl}
		b.cat.Columns = append(b.cat.Columns, c)
	}
	return b
}

// column changes the column table.col.
func (b *catBuilder) column(ref string, change func(*mssql.CharColumn)) *catBuilder {
	t, c, _ := strings.Cut(ref, ".")
	for i := range b.cat.Columns {
		if b.cat.Columns[i].ObjectID == b.ids[t] && b.cat.Columns[i].Column == c {
			change(&b.cat.Columns[i])
		}
	}
	return b
}

func (b *catBuilder) colIDs(table string, cols ...string) []int {
	var ids []int
	for _, c := range cols {
		id, ok := b.cols[table+"."+c]
		if !ok {
			panic("no column " + table + "." + c)
		}
		ids = append(ids, id)
	}
	return ids
}

// index adds an index: type 1 clustered (index id 1), others get ids 2, 3...
func (b *catBuilder) index(table, name string, typ int, keys []string, change ...func(*mssql.CatalogIndex)) *catBuilder {
	id := b.ids[table]
	ixID := 1
	if typ != 1 && typ != 5 {
		ixID = 2
		for _, ix := range b.cat.Indexes {
			if ix.ObjectID == id && ix.IndexID >= ixID {
				ixID = ix.IndexID + 1
			}
		}
	}
	ix := mssql.CatalogIndex{ObjectID: id, IndexID: ixID, Type: typ, DataSpaceType: "FG", Compressions: 1, KeyIDs: b.colIDs(table, keys...),
		Def: mssql.IndexDef{Name: name, Kind: mssql.IndexPlain, Clustered: typ == 1, AllowRowLocks: true, AllowPageLocks: true,
			Compression: "NONE", FileGroup: "PRIMARY"}}
	for _, k := range keys {
		ix.Def.Keys = append(ix.Def.Keys, mssql.IndexColumn{Name: k})
	}
	for _, c := range change {
		c(&ix)
	}
	if ix.Def.Kind != mssql.IndexPlain {
		b.obj++
		ix.ConstraintID = b.obj
	}
	b.cat.Indexes = append(b.cat.Indexes, ix)
	return b
}

func pk(ix *mssql.CatalogIndex) { ix.Def.Kind, ix.Def.Unique = mssql.IndexPrimaryKey, true }

func (b *catBuilder) indexID(table, name string) int {
	for _, ix := range b.cat.Indexes {
		if ix.ObjectID == b.ids[table] && ix.Def.Name == name {
			return ix.IndexID
		}
	}
	panic("no index " + name)
}

func (b *catBuilder) fk(name, child string, cols []string, parent string, refCols []string, keyIndex string) *catBuilder {
	b.obj++
	fk := mssql.CatalogFK{ObjectID: b.obj, ParentID: b.ids[child], RefID: b.ids[parent], KeyIndexID: b.indexID(parent, keyIndex),
		ParentIDs: b.colIDs(child, cols...), RefIDs: b.colIDs(parent, refCols...),
		Def: mssql.ForeignKeyDef{Name: name, Schema: "dbo", Table: child, Columns: cols, RefSchema: "dbo", RefTable: parent,
			RefColumns: refCols, OnDelete: "NO_ACTION", OnUpdate: "NO_ACTION"}}
	b.cat.ForeignKeys = append(b.cat.ForeignKeys, fk)
	return b
}

func (b *catBuilder) check(table, name string, column string, deps []string, def string, change ...func(*mssql.CatalogCheck)) *catBuilder {
	b.obj++
	c := mssql.CatalogCheck{ObjectID: b.obj, ParentID: b.ids[table], ColumnIDs: b.colIDs(table, deps...), Def: mssql.CheckDef{Name: name, Definition: def}}
	if column != "" {
		c.ParentColumnID = b.colIDs(table, column)[0]
	}
	for _, f := range change {
		f(&c)
	}
	b.cat.Checks = append(b.cat.Checks, c)
	return b
}

func (b *catBuilder) dflt(table, name, column string, noDef bool) *catBuilder {
	b.obj++
	b.cat.Defaults = append(b.cat.Defaults, mssql.CatalogDefault{ObjectID: b.obj, ParentID: b.ids[table], ColumnID: b.colIDs(table, column)[0],
		Def: mssql.DefaultDef{Name: name, Column: column, Definition: "('x')"}, NoDefinition: noDef})
	return b
}

func (b *catBuilder) stat(table, name string, auto, incremental bool, cols ...string) *catBuilder {
	b.cat.Stats = append(b.cat.Stats, mssql.CatalogStats{ObjectID: b.ids[table], StatsID: 100 + len(b.cat.Stats), AutoCreated: auto,
		Incremental: incremental, ColumnIDs: b.colIDs(table, cols...), Def: mssql.StatsDef{Name: name, Columns: cols, Recreate: !auto}})
	return b
}

func (b *catBuilder) objectOf(name string) int64 {
	for _, ix := range b.cat.Indexes {
		if ix.Def.Name == name && ix.ConstraintID != 0 {
			return ix.ConstraintID
		}
	}
	for _, fk := range b.cat.ForeignKeys {
		if fk.Def.Name == name {
			return fk.ObjectID
		}
	}
	for _, c := range b.cat.Checks {
		if c.Def.Name == name {
			return c.ObjectID
		}
	}
	panic("no object " + name)
}

func (b *catBuilder) prop(class int, table, object, name, base string) *catBuilder {
	e := mssql.CatalogExtendedProperty{Class: class, Name: name, Value: "v", BaseType: base}
	if class == 7 {
		e.MajorID, e.MinorID = b.ids[table], b.indexID(table, object)
	} else {
		e.MajorID = b.objectOf(object)
	}
	b.cat.ExtendedProperties = append(b.cat.ExtendedProperties, e)
	return b
}

// describe writes units as "a[c1,c2 ix:X ck:Y df:Z st:S/r ep:T.N]+b[...] fk:F | ...".
func describe(units []mssql.ConversionUnit) string {
	var out []string
	for _, u := range units {
		var tables []string
		for _, t := range u.Tables {
			var parts []string
			for _, c := range t.Columns {
				parts = append(parts, c.Name)
			}
			s := strings.Join(parts, ",")
			for _, ix := range t.Indexes {
				s += " ix:" + ix.Name
			}
			for _, c := range t.Checks {
				s += " ck:" + c.Name
			}
			for _, d := range t.Defaults {
				s += " df:" + d.Name
			}
			for _, st := range t.Stats {
				s += " st:" + st.Name
				if st.Recreate {
					s += "/r"
				}
			}
			for _, e := range t.ExtendedProperties {
				s += " ep:" + e.Target + "." + e.Name
			}
			tables = append(tables, t.Name+"["+s+"]")
		}
		s := strings.Join(tables, "+")
		for _, fk := range u.ForeignKeys {
			s += " fk:" + fk.Name
		}
		out = append(out, s)
	}
	return strings.Join(out, " | ")
}

func describeRefused(refused []RefusedTable) string {
	var out []string
	for _, r := range refused {
		out = append(out, r.Name+": "+r.Reason)
	}
	return strings.Join(out, " | ")
}

func TestPlanUnits(t *testing.T) {
	flag := func(f func(*mssql.CatalogTable)) mssql.CatalogTable {
		var t mssql.CatalogTable
		f(&t)
		return t
	}
	tests := []struct {
		name    string
		cat     *catBuilder
		tables  []string
		units   string
		refused []string // "table: reason substring"
		notes   []string
	}{
		{name: "one column, nothing depends on it", cat: newCatalog().table("a", "int:id", "name"), units: "a[name]"},
		{name: "already the target", cat: newCatalog().table("a", "name").column("a.name", func(c *mssql.CharColumn) { c.Collation = toColl })},
		{name: "computed column is not seeded", cat: newCatalog().table("a", "name", "upper").column("a.upper", func(c *mssql.CharColumn) { c.Computed = true }),
			units: "a[name]"},
		{name: "text column refuses its table", cat: newCatalog().table("a", "name", "notes").column("a.notes", func(c *mssql.CharColumn) { c.Type, c.BaseType = "text", "text" }),
			refused: []string{"a: column notes: text/ntext"}},
		{name: "alias type refuses its table", cat: newCatalog().table("a", "code").column("a.code", func(c *mssql.CharColumn) { c.Type, c.AliasType = "code_t", true }),
			refused: []string{"a: user-defined type code_t"}},
		{name: "masked column refuses its table", cat: func() *catBuilder {
			b := newCatalog().table("a", "email")
			b.cat.Masked = []mssql.ColumnKey{{ObjectID: b.ids["a"], ColumnID: 1}}
			return b
		}(), refused: []string{"a: column email: dynamic data masking"}},
		{name: "foreign key pulls the parent column in", cat: newCatalog().
			table("child", "int:id", "pcode").table("parent", "code", "label").
			index("parent", "PK_parent", 1, []string{"code"}, pk).
			fk("FK_child_parent", "child", []string{"pcode"}, "parent", []string{"code"}, "PK_parent"),
			tables: []string{"child"}, units: "child[pcode]+parent[code ix:PK_parent] fk:FK_child_parent", notes: []string{"dbo.parent"}},
		{name: "foreign keys close transitively", cat: newCatalog().
			table("a", "k").table("b", "k").table("c", "k").
			index("b", "PK_b", 1, []string{"k"}, pk).index("c", "PK_c", 1, []string{"k"}, pk).
			fk("FK_a_b", "a", []string{"k"}, "b", []string{"k"}, "PK_b").
			fk("FK_b_c", "b", []string{"k"}, "c", []string{"k"}, "PK_c"),
			tables: []string{"a"}, units: "a[k]+b[k ix:PK_b]+c[k ix:PK_c] fk:FK_a_b fk:FK_b_c", notes: []string{"dbo.b", "dbo.c"}},
		{name: "self-referencing foreign key", cat: newCatalog().table("node", "code", "parent").
			index("node", "PK_node", 1, []string{"code"}, pk).
			fk("FK_node_parent", "node", []string{"parent"}, "node", []string{"code"}, "PK_node"),
			units: "node[code,parent ix:PK_node] fk:FK_node_parent"},
		{name: "clustered index affected rebuilds every index", cat: newCatalog().table("a", "int:id", "code", "int:n").
			index("a", "CX_a", 1, []string{"code"}).index("a", "IX_a_n", 2, []string{"n"}).index("a", "IX_a_id", 2, []string{"id"}),
			units: "a[code ix:CX_a ix:IX_a_id ix:IX_a_n]"},
		{name: "non-clustered index affected alone", cat: newCatalog().table("a", "int:id", "code").
			index("a", "PK_a", 1, []string{"id"}, pk).index("a", "IX_a_code", 2, []string{"code"}),
			units: "a[code ix:IX_a_code]"},
		{name: "included column affects the index", cat: newCatalog().table("a", "int:id", "code").
			index("a", "IX_a_id", 2, []string{"id"}, func(ix *mssql.CatalogIndex) { ix.IncludedIDs, ix.Def.Included = []int{2}, []string{"code"} }),
			units: "a[code ix:IX_a_id]"},
		{name: "foreign key through a rebuilt unique index", cat: newCatalog().
			table("parent", "int:id", "name").table("child", "int:pid").
			index("parent", "CX_parent", 1, []string{"name"}).
			index("parent", "UX_parent_id", 2, []string{"id"}, func(ix *mssql.CatalogIndex) { ix.Def.Unique = true }).
			fk("FK_child_parent", "child", []string{"pid"}, "parent", []string{"id"}, "UX_parent_id"),
			units: "child[]+parent[name ix:CX_parent ix:UX_parent_id] fk:FK_child_parent", notes: []string{"dbo.child"}},
		{name: "memory-optimized", cat: newCatalog().tableWith("a", flag(func(t *mssql.CatalogTable) { t.MemoryOptimized = true }), "name"),
			refused: []string{"a: memory-optimized"}},
		{name: "temporal", cat: newCatalog().tableWith("a", flag(func(t *mssql.CatalogTable) { t.Temporal = 2 }), "name"),
			refused: []string{"a: temporal"}},
		{name: "replicated", cat: newCatalog().tableWith("a", flag(func(t *mssql.CatalogTable) { t.Replicated = true }), "name"),
			refused: []string{"a: replicated"}},
		{name: "change data capture", cat: newCatalog().tableWith("a", flag(func(t *mssql.CatalogTable) { t.CDC = true }), "name"),
			refused: []string{"a: change data capture"}},
		{name: "filetable, graph, ledger", cat: newCatalog().
			tableWith("f", flag(func(t *mssql.CatalogTable) { t.FileTable = true }), "name").
			tableWith("g", flag(func(t *mssql.CatalogTable) { t.Graph = true }), "name").
			tableWith("l", flag(func(t *mssql.CatalogTable) { t.Ledger = true }), "name"),
			refused: []string{"f: FileTable", "g: graph", "l: ledger"}},
		{name: "XML index rebuilt with the clustered one", cat: newCatalog().table("a", "code", "int:doc").
			index("a", "PK_a", 1, []string{"code"}, pk).index("a", "XML_a", 3, []string{"doc"}),
			refused: []string{"a: index XML_a (XML, spatial or columnstore) would have to be rebuilt"}},
		{name: "columnstore index on the column", cat: newCatalog().table("a", "int:id", "code").
			index("a", "NCCI_a", 6, nil, func(ix *mssql.CatalogIndex) { ix.IncludedIDs = []int{2} }),
			refused: []string{"a: index NCCI_a is an XML, spatial, columnstore or hash index"}},
		{name: "disabled index", cat: newCatalog().table("a", "code").index("a", "IX_a", 2, []string{"code"}, func(ix *mssql.CatalogIndex) { ix.Disabled = true }),
			refused: []string{"a: index IX_a is disabled"}},
		{name: "partitioned index", cat: newCatalog().table("a", "code").index("a", "IX_a", 2, []string{"code"}, func(ix *mssql.CatalogIndex) { ix.DataSpaceType = "PS" }),
			refused: []string{"a: index IX_a is partitioned"}},
		{name: "compression per partition", cat: newCatalog().table("a", "code").index("a", "IX_a", 2, []string{"code"}, func(ix *mssql.CatalogIndex) { ix.Compressions = 2 }),
			refused: []string{"a: index IX_a has a compression"}},
		{name: "hypothetical index", cat: newCatalog().table("a", "code").index("a", "_dta_a", 2, []string{"code"}, func(ix *mssql.CatalogIndex) { ix.Hypothetical = true }),
			refused: []string{"a: index _dta_a is hypothetical"}},
		{name: "full-text on the column", cat: func() *catBuilder {
			b := newCatalog().table("a", "int:id", "body").index("a", "PK_a", 1, []string{"id"}, pk)
			b.cat.FullText = []mssql.CatalogFullText{{ObjectID: b.ids["a"], KeyIndexID: 1, ColumnIDs: []int{2}}}
			return b
		}(), refused: []string{"a: its full-text index"}},
		{name: "full-text untouched", cat: func() *catBuilder {
			b := newCatalog().table("a", "int:id", "body", "code").index("a", "PK_a", 1, []string{"id"}, pk)
			b.column("a.body", func(c *mssql.CharColumn) { c.Collation = toColl })
			b.cat.FullText = []mssql.CatalogFullText{{ObjectID: b.ids["a"], KeyIndexID: 1, ColumnIDs: []int{2}}}
			return b
		}(), units: "a[code]"},
		{name: "computed column reads the column", cat: func() *catBuilder {
			b := newCatalog().table("a", "code", "int:len")
			b.cat.Computed = []mssql.CatalogComputed{{ObjectID: b.ids["a"], ColumnID: 2, Name: "len", Definition: "(len([code]))", ColumnIDs: []int{1}}}
			return b
		}(), refused: []string{"a: computed column len reads a converted column"}},
		{name: "computed column found by its definition", cat: func() *catBuilder {
			b := newCatalog().table("a", "Code", "int:len")
			b.cat.Computed = []mssql.CatalogComputed{{ObjectID: b.ids["a"], ColumnID: 2, Name: "len", Definition: "(len([code]))"}}
			return b
		}(), refused: []string{"a: computed column len"}},
		{name: "schema-bound view on the column", cat: func() *catBuilder {
			b := newCatalog().table("a", "code", "other")
			b.column("a.other", func(c *mssql.CharColumn) { c.Collation = toColl })
			b.cat.SchemaBound = []mssql.CatalogReference{{ReferencedID: b.ids["a"], ColumnID: 1, Schema: "dbo", Name: "v_a", Type: "VIEW"}}
			return b
		}(), refused: []string{"a: referenced by the schema-bound view dbo.v_a"}},
		{name: "schema-bound view on another column", cat: func() *catBuilder {
			b := newCatalog().table("a", "code", "other")
			b.column("a.other", func(c *mssql.CharColumn) { c.Collation = toColl })
			b.cat.SchemaBound = []mssql.CatalogReference{{ReferencedID: b.ids["a"], ColumnID: 2, Schema: "dbo", Name: "v_a", Type: "VIEW"}}
			return b
		}(), units: "a[code]"},
		{name: "schema-bound view lists another column and the whole table", cat: func() *catBuilder {
			b := newCatalog().table("a", "code", "other")
			b.column("a.other", func(c *mssql.CharColumn) { c.Collation = toColl })
			b.cat.SchemaBound = []mssql.CatalogReference{{ReferencedID: b.ids["a"], ColumnID: 2, Schema: "dbo", Name: "v_a", Type: "VIEW"},
				{ReferencedID: b.ids["a"], Schema: "dbo", Name: "v_a", Type: "VIEW"}}
			return b
		}(), units: "a[code]"},
		{name: "schema-bound reference to the whole table", cat: func() *catBuilder {
			b := newCatalog().table("a", "code")
			b.cat.SchemaBound = []mssql.CatalogReference{{ReferencedID: b.ids["a"], Schema: "dbo", Name: "f_a", Type: "SQL_INLINE_TABLE_VALUED_FUNCTION"}}
			return b
		}(), refused: []string{"a: referenced by the schema-bound sql inline table valued function dbo.f_a"}},
		{name: "checks: column, dependency, mention, unrelated", cat: newCatalog().table("a", "code", "kind", "int:n").
			column("a.kind", func(c *mssql.CharColumn) { c.Collation = toColl }).
			check("a", "CK_col", "code", nil, "([code]<>'')").
			check("a", "CK_dep", "", []string{"code"}, "([code]<>[kind])").
			check("a", "CK_text", "", nil, "([CODE] IS NOT NULL OR [n]>(0))").
			check("a", "CK_other", "kind", nil, "([kind]<>'')").
			check("a", "CK_n", "", []string{"n"}, "([n]>(0))"),
			units: "a[code ck:CK_col ck:CK_dep ck:CK_text]"},
		{name: "check without definition", cat: newCatalog().table("a", "code").
			check("a", "CK_a", "code", nil, "", func(c *mssql.CatalogCheck) { c.NoDefinition = true }),
			refused: []string{"a: CHECK CK_a: definition not readable"}},
		{name: "defaults", cat: newCatalog().table("a", "code", "kind").column("a.kind", func(c *mssql.CharColumn) { c.Collation = toColl }).
			dflt("a", "DF_code", "code", false).dflt("a", "DF_kind", "kind", false),
			units: "a[code df:DF_code]"},
		{name: "default without definition", cat: newCatalog().table("a", "code").dflt("a", "DF_code", "code", true),
			refused: []string{"a: DEFAULT DF_code: definition not readable"}},
		{name: "statistics: auto dropped, user recreated", cat: newCatalog().table("a", "code", "int:n").
			stat("a", "_WA_Sys_1", true, false, "code").stat("a", "st_user", false, false, "n", "code").stat("a", "st_n", false, false, "n"),
			units: "a[code st:_WA_Sys_1 st:st_user/r]"},
		{name: "incremental statistic", cat: newCatalog().table("a", "code").stat("a", "st_inc", false, true, "code"),
			refused: []string{"a: statistic st_inc is incremental"}},
		{name: "extended properties follow their objects", cat: newCatalog().
			table("child", "pcode").table("parent", "code").
			index("parent", "PK_parent", 1, []string{"code"}, pk).
			index("child", "IX_child", 2, []string{"pcode"}).
			fk("FK_child_parent", "child", []string{"pcode"}, "parent", []string{"code"}, "PK_parent").
			prop(1, "parent", "PK_parent", "MS_Description", "nvarchar").
			prop(7, "child", "IX_child", "owner", "varchar").
			prop(1, "child", "FK_child_parent", "MS_Description", "nvarchar"),
			units: "child[pcode ix:IX_child ep:FK_child_parent.MS_Description ep:IX_child.owner]+parent[code ix:PK_parent ep:PK_parent.MS_Description] fk:FK_child_parent"},
		{name: "extended property that is not text", cat: newCatalog().table("a", "code").index("a", "IX_a", 2, []string{"code"}).
			prop(7, "a", "IX_a", "since", "int"),
			refused: []string{"a: extended property since on IX_a is not text"}},
		{name: "a refused table refuses its unit", cat: newCatalog().
			table("child", "pcode").tableWith("parent", flag(func(t *mssql.CatalogTable) { t.Temporal = 2 }), "code").table("lonely", "name").
			index("parent", "PK_parent", 1, []string{"code"}, pk).
			fk("FK_child_parent", "child", []string{"pcode"}, "parent", []string{"code"}, "PK_parent"),
			units:   "lonely[name]",
			refused: []string{"child: linked by foreign key to dbo.parent: system-versioned", "parent: system-versioned"}},
		{name: "foreign key to a column that cannot be converted", cat: newCatalog().
			table("child", "pcode").table("parent", "code").
			column("parent.code", func(c *mssql.CharColumn) { c.Type, c.AliasType = "code_t", true }).
			index("parent", "PK_parent", 1, []string{"code"}, pk).
			fk("FK_child_parent", "child", []string{"pcode"}, "parent", []string{"code"}, "PK_parent"),
			tables:  []string{"child"},
			refused: []string{"child: its foreign key FK_child_parent references a column that cannot be converted", "parent: column code: user-defined type code_t"}},
		{name: "explicit tables leave the others alone", cat: newCatalog().table("a", "name").table("b", "name"),
			tables: []string{"b"}, units: "b[name]"},
		{name: "units and tables in name order", cat: newCatalog().
			table("z", "k").table("m", "k").table("b", "k").table("a", "name").
			index("m", "PK_m", 1, []string{"k"}, pk).
			fk("FK_z_m", "z", []string{"k"}, "m", []string{"k"}, "PK_m").
			fk("FK_b_m", "b", []string{"k"}, "m", []string{"k"}, "PK_m"),
			units: "a[name] | b[k]+m[k ix:PK_m]+z[k] fk:FK_b_m fk:FK_z_m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var refs []mssql.TableRef
			for _, n := range tc.tables {
				refs = append(refs, mssql.TableRef{Schema: "dbo", Name: n})
			}
			units, refused, notes := planUnits(tc.cat.cat, toColl, refs)
			if got := describe(units); got != tc.units {
				t.Errorf("units\n got: %s\nwant: %s", got, tc.units)
			}
			if len(refused) != len(tc.refused) {
				t.Fatalf("refused: %s", describeRefused(refused))
			}
			for i, want := range tc.refused {
				name, reason, _ := strings.Cut(want, ": ")
				if refused[i].Name != name || !strings.Contains(refused[i].Reason, reason) {
					t.Errorf("refused[%d] = %s: %s, want %s", i, refused[i].Name, refused[i].Reason, want)
				}
			}
			if !slices.Equal(notes, tc.notes) {
				t.Errorf("notes %q, want %q", notes, tc.notes)
			}
			for _, u := range units {
				for _, tbl := range u.Tables {
					for _, c := range tbl.Columns {
						if c.From != fromColl || c.To != toColl || c.Type != "nvarchar(50)" {
							t.Errorf("column %+v", c)
						}
					}
				}
			}
			// the same catalog plans the same units
			again, _, _ := planUnits(tc.cat.cat, toColl, refs)
			if fmt.Sprint(again) != fmt.Sprint(units) {
				t.Error("not deterministic")
			}
		})
	}
}
