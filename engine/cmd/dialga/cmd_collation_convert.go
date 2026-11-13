// Work package C: see the collation design.

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
)

func init() { collationCommands = append(collationCommands, (*app).collationConvertCmd) }

func (a *app) collationConvertCmd() *cobra.Command {
	var f opFlags
	var to string
	var tables []string
	var skip bool
	cmd := &cobra.Command{
		Use:   "convert <db> [--to COLLATION] [--table SCHEMA.TABLE]...",
		Short: "Convert the columns of the tables of a database to a collation",
		Long: `Convert the char, varchar, nchar and nvarchar columns of <db> to a collation
(--to, default: the default collation of the database), every table or only
those given with --table (repeatable; SCHEMA.TABLE, or [a.b].[c] for names
with dots). Foreign keys need the same collation on both sides, so the
tables they link are converted together, as one unit: one transaction that
drops the indexes, constraints and statistics on the columns, alters the
columns and creates everything again with its options and extended
properties. A unit waits at most 30 s for its tables, then fails with
nothing changed; while it runs, the application waits for those tables:
run it in a maintenance window. The log grows by about the size of a unit.

Tables it cannot rebuild exactly are refused, with the reason: text/ntext,
user-defined types, sparse, encrypted or masked columns; memory-optimized,
temporal, replicated, CDC, FileTable, graph and ledger tables; computed
columns, schema-bound views or functions on a column; XML, spatial,
columnstore, partitioned or disabled indexes; full-text indexes. With
--table a refusal fails the plan; for the whole database the refused tables
are left as they are.

The pre-flight checks the data: unique keys that would collide (CI to CS),
foreign key values that would lose their parent, varchar values the code
page of the new collation cannot hold. --skip-data-checks skips them (a
failure then rolls its unit back).

Converting is destructive: --apply asks to retype the database name (the
target name if it is protected).
` + opExitStatus,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ops.Params{Database: args[0], Collation: to, SkipDataChecks: skip}
			for _, t := range tables {
				ref, err := parseTableRef(t)
				if err != nil {
					return err
				}
				p.Tables = append(p.Tables, ref)
			}
			return a.runOp(cmd, ops.OpConvertColumns, p, f)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&to, "to", "", "the collation to convert to (default: the default collation of the database)")
	cmd.Flags().StringArrayVar(&tables, "table", nil, "a table to convert, SCHEMA.TABLE (repeatable; default: every table)")
	cmd.Flags().BoolVar(&skip, "skip-data-checks", false, "skip the uniqueness, reference and code page checks")
	return cmd
}

// parseTableRef reads SCHEMA.TABLE, [schema].[table] (brackets for names
// with dots, "]]" for a "]") or a bare TABLE (schema dbo).
func parseTableRef(s string) (mssql.TableRef, error) {
	var parts []string
	rest := s
	for rest != "" {
		var part string
		if strings.HasPrefix(rest, "[") {
			end := -1
			for i := 1; i < len(rest); i++ {
				if rest[i] != ']' {
					continue
				}
				if i+1 < len(rest) && rest[i+1] == ']' {
					i++
					continue
				}
				end = i
				break
			}
			if end < 0 {
				return mssql.TableRef{}, fmt.Errorf("invalid table %q: unclosed [", s)
			}
			part, rest = strings.ReplaceAll(rest[1:end], "]]", "]"), rest[end+1:]
		} else {
			i := strings.IndexByte(rest, '.')
			if i < 0 {
				i = len(rest)
			}
			part, rest = rest[:i], rest[i:]
		}
		if part == "" {
			return mssql.TableRef{}, fmt.Errorf("invalid table %q: a name is empty", s)
		}
		parts = append(parts, part)
		switch {
		case rest == "":
		case strings.HasPrefix(rest, ".") && len(rest) > 1:
			rest = rest[1:]
		default:
			return mssql.TableRef{}, fmt.Errorf("invalid table %q: use SCHEMA.TABLE or [schema].[table]", s)
		}
	}
	switch len(parts) {
	case 0:
		return mssql.TableRef{}, fmt.Errorf("invalid table %q: it is empty", s)
	case 1:
		return mssql.TableRef{Schema: "dbo", Name: parts[0]}, nil
	case 2:
		return mssql.TableRef{Schema: parts[0], Name: parts[1]}, nil
	}
	return mssql.TableRef{}, fmt.Errorf("invalid table %q: use SCHEMA.TABLE or [schema].[table]", s)
}
