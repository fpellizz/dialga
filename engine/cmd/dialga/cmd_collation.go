package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() { commands = append(commands, (*app).collationCmd) }

// collationCommands are the subcommands of "dialga collation" defined in
// their own files: each adds its constructor from init(), like commands.
//
//	func init() { collationCommands = append(collationCommands, (*app).collationSetCmd) }
var collationCommands []func(*app) *cobra.Command

func (a *app) collationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collation",
		Short: "Collations: of the server, of the databases, of their columns",
		Long: `Show the collation of the server (fixed when the RDS instance is created),
of tempdb and of each database; change the default collation of a database;
convert the columns of its tables to a collation. The views only read; the
changes are a dry run unless --apply is given, like restore and drop, and run
as jobs (dialga jobs).`,
	}
	cmd.AddCommand(a.collationShowCmd(), a.collationDBCmd(), a.collationListCmd())
	for _, c := range collationCommands {
		cmd.AddCommand(c(a))
	}
	return cmd
}

func (a *app) collationShowCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "show [target]",
		Short: "Collation of the server, of tempdb and of each database (read-only)",
		Long: `Show the collation of the server (fixed when the RDS instance is created: it
cannot be changed afterwards), of tempdb, and the default collation of each
user database, with what differs from the server and which databases risk
"Cannot resolve the collation conflict" on temp tables.

Exit status: 0 OK, 1 error.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				target = args[0]
			}
			name, t, err := a.target(target)
			if err != nil {
				return err
			}
			s, closeOps, err := a.openTarget(cmd.Context(), name, t)
			if err != nil {
				return err
			}
			defer closeOps()
			o, err := s.CollationOverview(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(o)
			}
			return printCollationOverview(cmd.OutOrStdout(), o)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

func printCollationOverview(w io.Writer, o ops.CollationOverview) error {
	_, _ = fmt.Fprintf(w, "Collations on %s (instance %s):\n", o.Target, o.Instance)
	fixed := ""
	if o.ServerFixed {
		fixed = " (fixed at instance creation on RDS)"
	}
	_, _ = fmt.Fprintf(w, "  server  %s%s\n  tempdb  %s\n", o.Server, fixed, o.Tempdb)
	if len(o.Databases) == 0 {
		_, err := fmt.Fprintln(w, "No user databases.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  NAME\tSTATE\tCOLLATION\tNOTE")
	for _, d := range o.Databases {
		var notes []string
		if d.MismatchServer {
			notes = append(notes, "≠ server")
		}
		if d.TempTableRisk {
			notes = append(notes, "temp-table risk")
		}
		if d.Containment == "PARTIAL" {
			notes = append(notes, "contained")
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", d.Name, d.State, orDash(d.Collation), strings.Join(notes, ", "))
	}
	return tw.Flush()
}

func (a *app) collationDBCmd() *cobra.Command {
	var target string
	var columns bool
	cmd := &cobra.Command{
		Use:   "db <db>",
		Short: "Collation of a database and of its character columns (read-only)",
		Long: `Show the default collation of a database against the server's and tempdb's,
and its character columns grouped by collation. With --columns, also list the
columns whose collation differs from the default of the database, with what
stops "convert" from touching them.

Exit status: 0 OK, 2 the database does not exist or is not ONLINE, 1 error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, t, err := a.target(target)
			if err != nil {
				return err
			}
			if err := validate.DBName(args[0]); err != nil {
				return err
			}
			s, closeOps, err := a.openTarget(cmd.Context(), name, t)
			if err != nil {
				return err
			}
			defer closeOps()
			v, err := s.DatabaseCollation(cmd.Context(), args[0])
			if errors.Is(err, mssql.ErrNoDatabase) || errors.Is(err, ops.ErrNotOnline) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: %v\n", err)
				return exitError{exitCheckFailed}
			}
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
			}
			return printDatabaseCollation(cmd.OutOrStdout(), v, columns)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	cmd.Flags().BoolVar(&columns, "columns", false, "list the columns whose collation differs from the default of the database")
	return cmd
}

func printDatabaseCollation(w io.Writer, v ops.DatabaseCollationView, columns bool) error {
	_, _ = fmt.Fprintf(w, "Collation of %s on %s (instance %s):\n", v.Database, v.Target, v.Instance)
	_, _ = fmt.Fprintf(w, "  database  %s\n  server    %s\n  tempdb    %s\n", v.Collation, v.Server, v.Tempdb)
	if v.MismatchServer {
		_, _ = fmt.Fprintln(w, "  The database collation differs from the server's.")
	}
	if v.TempTableRisk {
		_, _ = fmt.Fprintln(w, "  Temp-table risk: #temp tables take the tempdb collation unless COLLATE DATABASE_DEFAULT is used.")
	}
	if v.Containment == "PARTIAL" {
		_, _ = fmt.Fprintln(w, "  Contained database: its temp tables use the database collation.")
	}
	_, _ = fmt.Fprintf(w, "%d character columns by collation:\n", v.CharColumns)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  COLLATION\tCOLUMNS\tTABLES\tCODE PAGE\tNOTE")
	for _, g := range v.Groups {
		note := ""
		if g.Default {
			note = "database default"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%s\n", g.Collation, g.Columns, g.Tables, g.CodePage, note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !columns {
		if n := v.CharColumns - groupColumns(v, true); n > 0 {
			_, _ = fmt.Fprintf(w, "%d columns differ from the database default (--columns lists them).\n", n)
		}
		return nil
	}
	if len(v.Columns) == 0 {
		_, err := fmt.Fprintln(w, "No column differs from the database default.")
		return err
	}
	_, _ = fmt.Fprintln(w, "Columns that differ from the database default:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TABLE\tCOLUMN\tTYPE\tCOLLATION\tNOTE")
	for _, c := range v.Columns {
		_, _ = fmt.Fprintf(tw, "  %s.%s\t%s\t%s\t%s\t%s\n", c.Schema, c.Table, c.Column, c.SQLType(), c.Collation, c.Unsupported)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if v.Truncated {
		_, _ = fmt.Fprintf(w, "Only the first %d columns are listed.\n", len(v.Columns))
	}
	return nil
}

// groupColumns counts the columns of the default group (or of the others).
func groupColumns(v ops.DatabaseCollationView, def bool) int {
	n := 0
	for _, g := range v.Groups {
		if g.Default == def {
			n += g.Columns
		}
	}
	return n
}

func (a *app) collationListCmd() *cobra.Command {
	var target, filter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "The collations the instance knows (read-only)",
		Long: `List the collations of sys.fn_helpcollations with their traits: code page and
case, accent, kana, width sensitivity, binary and UTF-8. --filter keeps those
whose name has the text (case-insensitive).

Exit status: 0 OK, 1 error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(filter) > 128 {
				return errors.New("--filter is longer than 128 characters")
			}
			name, t, err := a.target(target)
			if err != nil {
				return err
			}
			s, closeOps, err := a.openTarget(cmd.Context(), name, t)
			if err != nil {
				return err
			}
			defer closeOps()
			all, err := s.Collations(cmd.Context())
			if err != nil {
				return err
			}
			list := []mssql.CollationInfo{}
			needle := strings.ToLower(filter)
			for _, c := range all {
				if needle == "" || strings.Contains(strings.ToLower(c.Name), needle) {
					list = append(list, c)
				}
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Collations []mssql.CollationInfo `json:"collations"`
				}{list})
			}
			return printCollations(cmd.OutOrStdout(), list)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	cmd.Flags().StringVar(&filter, "filter", "", "keep the collations whose name contains TEXT")
	return cmd
}

func printCollations(w io.Writer, list []mssql.CollationInfo) error {
	if len(list) == 0 {
		_, err := fmt.Fprintln(w, "No collation matches.")
		return err
	}
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "-"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tCODE PAGE\tCASE\tACCENT\tUTF8\tDESCRIPTION")
	for _, c := range list {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", c.Name, c.CodePage,
			sensitivity(c.CaseSensitive), sensitivity(c.AccentSensitive), yes(c.UTF8), c.Description)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%d collations.\n", len(list))
	return err
}

func sensitivity(b bool) string {
	if b {
		return "sensitive"
	}
	return "insensitive"
}
