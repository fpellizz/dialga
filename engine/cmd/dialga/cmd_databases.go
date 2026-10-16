package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

func init() { commands = append(commands, (*app).databasesCmd) }

func (a *app) databasesCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:     "databases [target]",
		Aliases: []string{"dbs"},
		Short:   "List the user databases of a target",
		Long: `List the user databases of a target (sys.databases without the system
databases and rdsadmin), by name: state, size of the data and log files,
compatibility level, recovery model and owner. Read-only.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				target = args[0]
			}
			name, t, dbs, closeDBs, err := a.targetDatabases(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer closeDBs()
			list, err := dbs.Databases(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Target    string           `json:"target"`
					Instance  string           `json:"instance"`
					Databases []mssql.Database `json:"databases"`
				}{name, t.Instance, list})
			}
			return printDatabases(cmd.OutOrStdout(), name, t, list)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

// targetDatabases opens a session on the target to read its databases; it
// asks for the SQL password on the terminal when the configuration says so.
func (a *app) targetDatabases(ctx context.Context, target string) (string, config.Target, mssql.DatabaseReader, func(), error) {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	name, t, err := cfg.Target(target)
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	password, err := a.sqlPassword(name, t)
	if err != nil {
		return "", config.Target{}, nil, nil, fmt.Errorf("SQL password: %w", err)
	}
	dbs, closeDBs, err := a.openDatabases(ctx, t, password)
	if err != nil {
		return "", config.Target{}, nil, nil, err
	}
	return name, t, dbs, closeDBs, nil
}

func printDatabases(w io.Writer, name string, t config.Target, list []mssql.Database) error {
	if len(list) == 0 {
		_, err := fmt.Fprintf(w, "No user databases on %s (instance %s).\n", name, t.Instance)
		return err
	}
	_, _ = fmt.Fprintf(w, "User databases on %s (instance %s):\n", name, t.Instance)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  NAME\tSTATE\tDATA\tLOG\tCOMPAT\tRECOVERY\tOWNER")
	var total float64
	for _, d := range list {
		owner := d.Owner
		if owner == "" {
			owner = "-"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\t%s\n", d.Name, databaseState(d),
			sizeMiB(d.DataMB), sizeMiB(d.LogMB), d.Compatibility, d.Recovery, owner)
		total += d.DataMB + d.LogMB
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	count := fmt.Sprintf("%d databases", len(list))
	if len(list) == 1 {
		count = "1 database"
	}
	_, err := fmt.Fprintf(w, "%s, %s in total.\n", count, sizeMiB(total))
	return err
}

// databaseState is the state with what limits access: ONLINE (READ_ONLY, SINGLE_USER).
func databaseState(d mssql.Database) string {
	var flags []string
	if d.ReadOnly {
		flags = append(flags, "READ_ONLY")
	}
	if d.UserAccess != "" && d.UserAccess != "MULTI_USER" {
		flags = append(flags, d.UserAccess)
	}
	if len(flags) == 0 {
		return d.State
	}
	return d.State + " (" + strings.Join(flags, ", ") + ")"
}

// sizeMiB prints a size in MiB with binary units: 8.0 MiB, 1.5 GiB.
func sizeMiB(mb float64) string {
	switch {
	case mb >= 1<<20:
		return fmt.Sprintf("%.1f TiB", mb/(1<<20))
	case mb >= 1<<10:
		return fmt.Sprintf("%.1f GiB", mb/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", mb)
	}
}
