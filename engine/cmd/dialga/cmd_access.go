package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/validate"
)

func init() { commands = append(commands, (*app).accessCmd) }

const accessExitStatus = opExitStatus + `

The login itself is never dropped, and the master user of the target, sa,
rdsa, the ##...## logins and dbo, guest, sys and INFORMATION_SCHEMA are never
changed.`

func (a *app) accessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Who can use a database: list, grant, revoke, fix orphaned users",
		Long: `Give a SQL login access to a database, take it back, bind an orphaned user to
its login. "list" and "logins" only read; the others are a dry run unless
--apply is given, like restore and drop, and run as jobs (dialga jobs).`,
	}
	cmd.AddCommand(a.accessListCmd(), a.accessLoginsCmd(), a.accessGrantCmd(), a.accessRevokeCmd(), a.accessFixOrphanCmd())
	return cmd
}

func (a *app) accessListCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "list <db>",
		Short: "The users of a database: login, roles, level, backup (read-only)",
		Long: `List the SQL users of a database with the login each is bound to, its roles,
the level of access derived from them (none, read, readwrite, owner, custom)
and whether it can back the database up. Orphaned users (no login) are marked.

Exit status: 0 OK, 2 the database does not exist, 1 error.`,
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
			v, err := s.Access(cmd.Context(), args[0])
			if errors.Is(err, mssql.ErrNoDatabase) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: %v\n", err)
				return exitError{exitCheckFailed}
			}
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
			}
			return printAccess(cmd.OutOrStdout(), v)
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

func printAccess(w io.Writer, v ops.AccessView) error {
	_, _ = fmt.Fprintf(w, "Users of %s on %s (instance %s):\n", v.Database, v.Target, v.Instance)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  USER\tLOGIN\tLEVEL\tBACKUP\tROLES\tNOTE")
	for _, u := range v.Users {
		backup, note := "-", ""
		switch {
		case u.Backup:
			backup = "yes"
		case u.BackupPartial:
			backup = "partial"
		}
		switch {
		case u.Orphan:
			note = "orphan: no login (dialga access fix-orphan)"
		case !u.Managed:
			note = "not managed by Dialga"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", u.Name, orDash(u.Login), u.Level, backup, orDash(strings.Join(u.Roles, ", ")), note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if v.WindowsPrincipals > 0 {
		_, _ = fmt.Fprintf(w, "%d Windows user(s) or group(s), unusable on RDS without Active Directory.\n", v.WindowsPrincipals)
	}
	return nil
}

func (a *app) accessLoginsCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "logins",
		Short: "The SQL logins of the instance (read-only)",
		Long: `List the SQL logins of the instance, without sa, rdsa and the ##...## ones. The
master user of the target is marked: its access is never changed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, t, err := a.target(target)
			if err != nil {
				return err
			}
			s, closeOps, err := a.openTarget(cmd.Context(), name, t)
			if err != nil {
				return err
			}
			defer closeOps()
			logins, err := s.Logins(cmd.Context())
			if err != nil {
				return err
			}
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Logins []mssql.Login `json:"logins"`
				}{logins})
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "SQL logins on %s (instance %s):\n", name, t.Instance)
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "  NAME\tDISABLED\tCREATED\tNOTE")
			for _, l := range logins {
				note, disabled := "", "-"
				if l.Master {
					note = "master user"
				}
				if l.Disabled {
					disabled = "yes"
				}
				_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", l.Name, disabled, orDash(l.CreatedAt), note)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

func (a *app) accessGrantCmd() *cobra.Command {
	var f opFlags
	var level string
	var backup, createLogin, fromStdin, generate bool
	cmd := &cobra.Command{
		Use:   "grant <db> <login> --level read|readwrite|owner",
		Short: "Give a SQL login access to a database (and optionally to back it up)",
		Long: `Give <login> access to <db>: the user of the login (created, named after the
login, when the database has none) joins the roles of --level and leaves those
of the other levels (db_datareader, db_datawriter, db_owner); its other roles
stay.
  read       db_datareader
  readwrite  db_datareader + db_datawriter
  owner      db_owner
--backup also lets it run dialga export: db_backupoperator here and, in msdb, a
user with the permissions of rds_backup_database and rds_task_status.

--create-login creates the login too (CHECK_POLICY ON, no expiry); --apply then
needs its password: --password-stdin reads one line from stdin, or
--generate-password makes one of 20 characters and prints it once, on stderr
(the "generatedPassword" field with -o json), after the login exists. The
password is never shown in the plan or kept in the journal.
` + accessExitStatus,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := ops.Params{Database: args[0], Login: args[1], Level: level, Backup: backup, CreateLogin: createLogin}
			var pw loginPassword
			switch {
			case !createLogin && (fromStdin || generate):
				return errors.New("--password-stdin and --generate-password are for --create-login")
			case fromStdin && generate:
				return errors.New("use one of --password-stdin and --generate-password")
			case createLogin && f.apply && !fromStdin && !generate:
				return errors.New("--create-login needs the password of the new login: --password-stdin or --generate-password")
			case createLogin && f.apply && fromStdin:
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("--password-stdin: no password on stdin: %w", err)
				}
				pw.value = strings.TrimRight(line, "\r\n")
			case createLogin && f.apply:
				var err error
				if pw.value, err = generatePassword(); err != nil {
					return err
				}
				pw.generated = true
			}
			return a.runOpWith(cmd, ops.OpGrant, p, f, pw)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&level, "level", "", "read, readwrite or owner (required)")
	cmd.Flags().BoolVar(&backup, "backup", false, "also let the login back the database up (db_backupoperator + msdb permissions)")
	cmd.Flags().BoolVar(&createLogin, "create-login", false, "create the login too; it needs a password with --apply")
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password of the new login from one line of stdin")
	cmd.Flags().BoolVar(&generate, "generate-password", false, "generate the password of the new login and print it once, after the apply")
	_ = cmd.MarkFlagRequired("level")
	return cmd
}

func (a *app) accessRevokeCmd() *cobra.Command {
	var f opFlags
	var remove string
	cmd := &cobra.Command{
		Use:   "revoke <db> <login> --remove level|backup|user",
		Short: "Take access to a database back from a SQL login",
		Long: `Take access to <db> back from <login>:
  level   the user leaves db_datareader, db_datawriter and db_owner
  backup  the user leaves db_backupoperator; its permissions in msdb are
          revoked too, unless another database still lets the login back up
          (or one cannot be read), and its msdb user dropped if nothing else
          needs it
  user    the user is dropped from the database (destructive: --apply asks
          to retype the login), with the msdb cleanup of backup; it fails if
          the user owns schemas
` + accessExitStatus,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOp(cmd, ops.OpRevoke, ops.Params{Database: args[0], Login: args[1], Remove: remove}, f)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&remove, "remove", "", "what to take back: level, backup or user (required)")
	_ = cmd.MarkFlagRequired("remove")
	return cmd
}

func (a *app) accessFixOrphanCmd() *cobra.Command {
	var f opFlags
	var login string
	cmd := &cobra.Command{
		Use:   "fix-orphan <db> <user> [--login L]",
		Short: "Bind a database user without a login (after a restore) to a login",
		Long: `Bind <user>, a SQL user of <db> whose login does not exist on this instance
(typical after a restore from another one), to the login <login> (default: the
name of the user) with ALTER USER ... WITH LOGIN. The login must exist and not
be bound to another user of the database.
` + accessExitStatus,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOp(cmd, ops.OpFixOrphan, ops.Params{Database: args[0], User: args[1], Login: login}, f)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&login, "login", "", "the login to bind it to (default: the name of the user)")
	return cmd
}

// generatePassword makes a password of 20 characters from crypto/rand that
// meets ops.CheckLoginPassword: at least three of each class. It avoids the
// characters that trip shells and connection strings.
func generatePassword() (string, error) {
	const (
		upper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower  = "abcdefghijkmnopqrstuvwxyz"
		digits = "23456789"
		symbol = "-_.+=#%^*"
		length = 20
	)
	pick := func(set string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[n.Int64()], nil
	}
	// One of each class, the rest from all of them, then a shuffle.
	pw := make([]byte, 0, length)
	for _, set := range []string{upper, lower, digits, symbol} {
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		pw = append(pw, c)
	}
	for len(pw) < length {
		c, err := pick(upper + lower + digits + symbol)
		if err != nil {
			return "", err
		}
		pw = append(pw, c)
	}
	for i := len(pw) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		j := n.Int64()
		pw[i], pw[j] = pw[j], pw[i]
	}
	out := string(pw)
	if err := ops.CheckLoginPassword(out); err != nil {
		return "", err
	}
	return out, nil
}
