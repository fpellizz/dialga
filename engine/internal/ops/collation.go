package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// The collation operations change the default collation of a database
// (set-collation) and convert the columns of its tables to a collation
// (convert-columns). The collation of the server is fixed when the RDS
// instance is created: it is only shown. The read types and the dispatch
// are here (collation design, step 0); the views are in collation_view.go,
// the operations in collation_db.go and collation_columns.go.

// The collation operations, as the CLI commands and the API name them.
const (
	OpSetCollation   = "set-collation"
	OpConvertColumns = "convert-columns"
)

// The ids of the collation actions and job steps.
const (
	// CollSetDatabase is the one action of set-collation.
	CollSetDatabase = "set-collation"
	// CollConvert is an action of convert-columns, one per unit: the ids of a
	// job get a "#n" when it repeats, like add-role.
	CollConvert = "convert"
)

// IsCollation reports whether op changes collations.
func IsCollation(op string) bool { return op == OpSetCollation || op == OpConvertColumns }

// ErrNotOnline means the database is not ONLINE: its collation and its
// columns cannot be read.
var ErrNotOnline = errors.New("the database is not online")

// errNotYet is what the parts of the collation work that are not written yet
// answer (the stubs of the work packages).
var errNotYet = errors.New("collation: not implemented yet")

// CollationSQL is the SQL side of the collation views and operations.
// mssql.Session implements it; SQL embeds it.
type CollationSQL interface {
	ServerCollation(ctx context.Context) (mssql.ServerCollation, error)
	Collation(ctx context.Context, name string) (mssql.CollationInfo, bool, error)
	CollationState(ctx context.Context, db string) (mssql.CollationState, error)
	DatabaseCollations(ctx context.Context) ([]mssql.DatabaseCollation, error)
	Collations(ctx context.Context) ([]mssql.CollationInfo, error)
	CharColumns(ctx context.Context, db string) ([]mssql.CharColumn, error)
	CollationBlockers(ctx context.Context, db string) ([]mssql.CollationBlocker, error)
	NameClashes(ctx context.Context, db, collation string) ([]string, error)
	ConversionCatalog(ctx context.Context, db string) (mssql.ConversionCatalog, error)
	// Exists runs a "SELECT TOP (1) 1 ..." data check: true when it returns a
	// row.
	Exists(ctx context.Context, query string) (bool, error)
}

// CollationStep is one step of a collation plan, as the plan and the journal
// of a job record it: parameters, not the statement, so a resume builds it
// again (and refuses a journal that was edited).
type CollationStep struct {
	// ID is CollSetDatabase or CollConvert.
	ID       string `json:"id"`
	Database string `json:"database"`
	// Collation is the target collation.
	Collation string `json:"collation"`
	// From, for set-collation, is the default collation read at plan time.
	From string `json:"from,omitempty"`
	// Unit, for convert, is what the step converts.
	Unit *mssql.ConversionUnit `json:"unit,omitempty"`
}

// Statement is the T-SQL of the step.
func (st CollationStep) Statement() (string, error) {
	switch st.ID {
	case CollSetDatabase:
		return mssql.SetDatabaseCollation(st.Database, st.Collation), nil
	case CollConvert:
		if st.Unit == nil {
			return "", errors.New("the convert step has no unit")
		}
		return mssql.ConvertUnit(*st.Unit), nil
	}
	return "", fmt.Errorf("unknown collation step %q", st.ID)
}

// sqlTypePattern is the types a converted column may have, as
// CharColumn.SQLType writes them.
var sqlTypePattern = regexp.MustCompile(`^(?:n?char\([1-9][0-9]{0,3}\)|n?varchar\((?:max|[1-9][0-9]{0,3})\)|sysname)$`)

// Validate checks the names of the step, as for a new request: a journal is
// a file on disk. Expressions (CHECK, DEFAULT, filters) are not validated:
// they are what the catalog had (collation design §11, R6).
func (st CollationStep) Validate(_ config.Target) error {
	if err := validate.DBName(st.Database); err != nil {
		return err
	}
	if err := validate.CollationName(st.Collation); err != nil {
		return err
	}
	if st.From != "" {
		if err := validate.CollationName(st.From); err != nil {
			return err
		}
	}
	switch st.ID {
	case CollSetDatabase:
		if st.Unit != nil {
			return errors.New("the set-collation step has a unit to convert")
		}
		return nil
	case CollConvert:
		if st.Unit == nil {
			return errors.New("the convert step has no unit")
		}
		return validateUnit(*st.Unit, st.Database, st.Collation)
	}
	return fmt.Errorf("unknown collation step %q", st.ID)
}

// validateUnit checks the names, types and options of a conversion unit.
func validateUnit(u mssql.ConversionUnit, db, collation string) error {
	if u.Database != db || u.Collation != collation {
		return fmt.Errorf("the unit converts %s to %s, the step %s to %s", u.Database, u.Collation, db, collation)
	}
	if len(u.Tables) == 0 {
		return errors.New("the unit has no table")
	}
	names := func(list ...string) error {
		for _, n := range list {
			if err := validate.ObjectName(n); err != nil {
				return err
			}
		}
		return nil
	}
	for _, t := range u.Tables {
		if err := names(t.Schema, t.Name); err != nil {
			return err
		}
		for _, c := range t.Columns {
			if err := names(c.Name); err != nil {
				return err
			}
			if !sqlTypePattern.MatchString(c.Type) {
				return fmt.Errorf("column %s of %s.%s: type %q is not one a conversion alters", c.Name, t.Schema, t.Name, c.Type)
			}
			if err := validate.CollationName(c.From); err != nil {
				return err
			}
			if c.To != collation {
				return fmt.Errorf("column %s of %s.%s goes to %s, the unit to %s", c.Name, t.Schema, t.Name, c.To, collation)
			}
		}
		for _, ix := range t.Indexes {
			if err := names(ix.Name, ix.FileGroup); err != nil {
				return err
			}
			if !slices.Contains([]string{mssql.IndexPrimaryKey, mssql.IndexUniqueConstraint, mssql.IndexPlain}, ix.Kind) {
				return fmt.Errorf("index %s: invalid kind %q", ix.Name, ix.Kind)
			}
			if !slices.Contains([]string{"NONE", "ROW", "PAGE"}, ix.Compression) {
				return fmt.Errorf("index %s: invalid compression %q", ix.Name, ix.Compression)
			}
			for _, k := range ix.Keys {
				if err := names(k.Name); err != nil {
					return err
				}
			}
			if err := names(ix.Included...); err != nil {
				return err
			}
		}
		for _, c := range t.Checks {
			if err := names(c.Name); err != nil {
				return err
			}
		}
		for _, d := range t.Defaults {
			if err := names(d.Name, d.Column); err != nil {
				return err
			}
		}
		for _, s := range t.Stats {
			if err := names(s.Name); err != nil {
				return err
			}
			if err := names(s.Columns...); err != nil {
				return err
			}
		}
		for _, e := range t.ExtendedProperties {
			if err := names(e.Target, e.Name); err != nil {
				return err
			}
			if e.Level2 != mssql.Level2Constraint && e.Level2 != mssql.Level2Index {
				return fmt.Errorf("extended property %s: invalid level2 %q", e.Name, e.Level2)
			}
		}
	}
	for _, fk := range u.ForeignKeys {
		if err := names(fk.Name, fk.Schema, fk.Table, fk.RefSchema, fk.RefTable); err != nil {
			return err
		}
		if err := names(append(slices.Clone(fk.Columns), fk.RefColumns...)...); err != nil {
			return err
		}
		if !slices.Contains(mssql.ReferentialActions, fk.OnDelete) || !slices.Contains(mssql.ReferentialActions, fk.OnUpdate) {
			return fmt.Errorf("foreign key %s: invalid actions %q, %q", fk.Name, fk.OnDelete, fk.OnUpdate)
		}
	}
	return nil
}

// validateCollation is Validate for the collation operations: what they
// share, then set-collation or convert-columns.
func validateCollation(op string, p Params) error {
	if p.Key != "" || p.TaskID != 0 || p.Restore != (mssql.RestoreOptions{}) || p.Replace != (ReplaceOptions{}) {
		return fmt.Errorf("%s takes a database and a collation, not a backup key, task or restore options", op)
	}
	if p.Login != "" || p.User != "" || p.Level != "" || p.Backup || p.CreateLogin || p.Remove != "" {
		return fmt.Errorf("login, user, level, backup, createLogin and remove are for grant, revoke and fix-orphan, not %s", op)
	}
	if op == OpSetCollation {
		return validateSetCollation(p)
	}
	return validateConvertColumns(p)
}

// collationFields reports whether p has a parameter of the collation
// operations, which the other operations refuse.
func collationFields(op string, p Params) error {
	if p.Collation != "" || len(p.Tables) > 0 || p.SkipDataChecks {
		return fmt.Errorf("collation, tables and skipDataChecks are for set-collation and convert-columns, not %s", op)
	}
	return nil
}

// RunCollationStep runs one step: it reads the database first, so a step the
// instance already did (an interrupted job, a second run) is a no-op.
func (s *Session) RunCollationStep(ctx context.Context, st CollationStep) (string, error) {
	switch st.ID {
	case CollSetDatabase:
		return s.runSetCollation(ctx, st)
	case CollConvert:
		return s.runConvert(ctx, st)
	}
	return "", fmt.Errorf("unknown collation step %q", st.ID)
}

// CollationSteps are the steps of a collation plan, as the job runs them.
func (p *Prepared) CollationSteps() []CollationStep { return p.collation }

// addCollation appends the action of a collation step (destructive: it
// rewrites the database or its tables) with its statement.
func (s *Session) addCollation(p *Prepared, st CollationStep, title string) error {
	stmt, err := st.Statement()
	if err != nil {
		return err
	}
	p.collation = append(p.collation, st)
	p.Plan.Actions = append(p.Plan.Actions, core.Action{
		ID: st.ID, Title: title, Kind: core.KindSQL, Risk: core.Destructive, Statement: stmt,
		Run: func(ctx context.Context) (string, error) { return s.RunCollationStep(ctx, st) },
	})
	return nil
}

// addCollation is used by set-collation and convert-columns (work packages
// B and C); until they land nothing calls it.
var _ = (*Session).addCollation

// CollationPlan is the summary of a collation plan (also PlanPreview.collation).
type CollationPlan struct {
	Database string `json:"database"`
	// From is the default collation of the database (set-collation).
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	// Units, Tables, Columns, Indexes and ForeignKeys count what a
	// convert-columns rebuilds; DataMB is the size of its tables.
	Units       int     `json:"units"`
	Tables      int     `json:"tables"`
	Columns     int     `json:"columns"`
	Indexes     int     `json:"indexes"`
	ForeignKeys int     `json:"foreignKeys"`
	DataMB      float64 `json:"dataMB"`
	// Refused are the tables convert-columns leaves as they are, and why.
	Refused []RefusedTable `json:"refused,omitempty"`
}

// RefusedTable is a table a conversion does not touch, and why.
type RefusedTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// CollationOverview is the collation view of a target: the server, tempdb
// and the default collation of each user database.
type CollationOverview struct {
	Target   string `json:"target"`
	Instance string `json:"instance"`
	Server   string `json:"server"`
	Tempdb   string `json:"tempdb"`
	// ServerFixed: the server collation cannot be changed (always on RDS).
	ServerFixed bool                       `json:"serverFixed"`
	Databases   []DatabaseCollationSummary `json:"databases"`
}

// DatabaseCollationSummary is a database in CollationOverview.
type DatabaseCollationSummary struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Collation   string `json:"collation,omitempty"`
	Containment string `json:"containment"`
	// MismatchServer: the default collation is not the server's.
	MismatchServer bool `json:"mismatchServer"`
	// TempTableRisk: the default collation is not tempdb's and the database
	// is not contained, so #temp tables compare with another collation
	// (Msg 468).
	TempTableRisk bool `json:"tempTableRisk"`
}

// DatabaseCollationView is the collation view of one database: its default,
// the collations its columns use and the columns that differ.
type DatabaseCollationView struct {
	Target         string `json:"target"`
	Instance       string `json:"instance"`
	Database       string `json:"database"`
	Collation      string `json:"collation"`
	Server         string `json:"server"`
	Tempdb         string `json:"tempdb"`
	Containment    string `json:"containment"`
	MismatchServer bool   `json:"mismatchServer"`
	TempTableRisk  bool   `json:"tempTableRisk"`
	// CharColumns counts the columns with a collation.
	CharColumns int              `json:"charColumns"`
	Groups      []CollationGroup `json:"groups"`
	// Columns are the columns whose collation is not the default of the
	// database, at most 2000 (Truncated says there were more).
	Columns   []ColumnView `json:"columns"`
	Truncated bool         `json:"truncated"`
}

// CollationGroup counts the columns of a database with one collation.
type CollationGroup struct {
	Collation string `json:"collation"`
	// Default: the default collation of the database.
	Default  bool `json:"default"`
	Columns  int  `json:"columns"`
	Tables   int  `json:"tables"`
	CodePage int  `json:"codePage"`
}

// ColumnView is a column in DatabaseCollationView.
type ColumnView struct {
	mssql.CharColumn
	// Unsupported is why convert-columns never touches it
	// (CharColumn.Unsupported).
	Unsupported string `json:"unsupported,omitempty"`
}
