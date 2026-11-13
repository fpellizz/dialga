package main

import (
	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/ops"
)

func init() { collationCommands = append(collationCommands, (*app).collationSetCmd) }

func (a *app) collationSetCmd() *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use:   "set <db> <collation>",
		Short: "Change the default collation of a database (ALTER DATABASE ... COLLATE)",
		Long: `Change the default collation of a database. The database goes to SINGLE_USER
for the change (its sessions are disconnected and their transactions rolled
back) and is opened again afterwards, also when the change fails.

Existing columns keep their collation: only the default for new columns,
literals, variables and the names of objects changes. Convert the columns with
"dialga collation convert". The collation of the server is fixed when the RDS
instance is created and cannot be changed here.

SQL Server refuses the change while schema-bound objects, computed columns or
CHECK constraints depend on the collation, or when names would collide under
the new one: the pre-flight checks list them. A Multi-AZ database is refused
(SINGLE_USER is not allowed). Run it in a maintenance window.

--apply asks to retype the database name (the target name on a protected
target).
` + opExitStatus,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOp(cmd, ops.OpSetCollation, ops.Params{Database: args[0], Collation: args[1]}, f)
		},
	}
	f.register(cmd, false)
	return cmd
}
