// Work package A: see the collation design (show, db, list).

package main

import "github.com/spf13/cobra"

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
	for _, c := range collationCommands {
		cmd.AddCommand(c(a))
	}
	return cmd
}
