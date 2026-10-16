package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/session"
)

func init() { commands = append(commands, (*app).passwordCmd) }

// maxPasswordBytes is the longest password dialga stores (as the API accepts).
const maxPasswordBytes = 1024

func (a *app) passwordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Store, forget or show where the SQL password of a target is kept",
		Long: `Manage the SQL password of the targets whose sql.password is prompt or
keyring, in the system keyring (service "dialga", account = target name).
The password is never printed and never written to a file.`,
	}
	cmd.AddCommand(a.passwordSetCmd(), a.passwordForgetCmd(), a.passwordStatusCmd())
	return cmd
}

func (a *app) passwordSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <target>",
		Short: "Store the SQL password of a target in the system keyring",
		Long: `Ask for the password without echo on a terminal (twice), or read one line
from stdin when it is piped, and store it in the system keyring.

  printf '%s\n' "$PW" | dialga password set dev`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, t, err := a.target(args[0])
			if err != nil {
				return err
			}
			if err := a.passwordStore().CheckSettable(t); err != nil {
				return err
			}
			password, err := a.readNewPassword(name, t)
			if err != nil {
				return err
			}
			switch err := a.passwordStore().Set(name, t, password, true); {
			case errors.Is(err, session.ErrNoKeyring):
				return errors.New("no system keyring is usable on this machine (on Linux it needs a Secret Service such as GNOME Keyring or KWallet)")
			case err != nil:
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Password for %s stored in the system keyring.\n", name)
			return err
		},
	}
}

// readNewPassword gets the password to store: asked twice on a terminal,
// one line from stdin otherwise.
func (a *app) readNewPassword(name string, t config.Target) (string, error) {
	var password string
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		first, err := a.readPassword(fmt.Sprintf("SQL password for %s on %s: ", t.SQL.User, t.Instance))
		if err != nil {
			return "", err
		}
		again, err := a.readPassword("Again: ")
		if err != nil {
			return "", err
		}
		if first != again {
			return "", errors.New("the two passwords differ: nothing was stored")
		}
		password = first
	} else {
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("no password on stdin: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if password == "" || len(password) > maxPasswordBytes {
		return "", fmt.Errorf("the password for %s must be 1 to %d bytes", name, maxPasswordBytes)
	}
	return password, nil
}

func (a *app) passwordForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <target>",
		Short: "Remove the SQL password of a target from the system keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _, err := a.target(args[0])
			if err != nil {
				return err
			}
			if err := a.passwordStore().Forget(name); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "No password of %s is stored in the system keyring any more.\n", name)
			return err
		},
	}
}

// passwordState is one row of "dialga password status".
type passwordState struct {
	Target  string `json:"target"`
	Source  string `json:"source"`
	Keyring string `json:"keyring"` // stored, empty or unavailable
}

func (a *app) passwordStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [target]",
		Short: "Show where the SQL password of a target comes from, never the password",
		Long: `For each target (or the one given): the password source of the configuration
and whether the system keyring has an entry (stored, empty) or cannot be used
(unavailable).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			names := cfg.Names()
			if len(args) == 1 {
				name, _, err := cfg.Target(args[0])
				if err != nil {
					return err
				}
				names = []string{name}
			}
			rows := make([]passwordState, 0, len(names))
			for _, name := range names {
				st := a.passwordStore().Status(name, cfg.Targets[name])
				row := passwordState{Target: name, Source: st.Source, Keyring: "unavailable"}
				if st.CanRemember {
					row.Keyring = "empty"
					if a.passwordStore().HasRemembered(name) {
						row.Keyring = "stored"
					}
				}
				rows = append(rows, row)
			}
			return printPasswordStates(cmd.OutOrStdout(), rows, a.output == "json")
		},
	}
}

func printPasswordStates(w io.Writer, rows []passwordState, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TARGET\tSOURCE\tKEYRING")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Target, r.Source, r.Keyring)
	}
	return tw.Flush()
}
