// Command dialga imports and exports SQL Server databases on Amazon RDS
// through S3. "serve" runs the local API for the desktop shell; the other
// commands are the CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/ops"
	"github.com/decisyon/dialga/engine/internal/session"
)

// version is overridden at build time by ci/build-engine.sh
// (-ldflags "-X main.version=<version>").
var version = "dev"

// Exit codes: stable, scripts rely on them.
const (
	exitFailure     = 1 // error: bad configuration, AWS or API failure...
	exitCheckFailed = 2 // the command ran, a verification did not pass
	exitTaskFailed  = 3 // a native RDS task ended in ERROR or CANCELLED
)

// exitError ends a command with a given exit code without printing more.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// app holds the global flags and the dependencies the tests replace.
type app struct {
	configPath string
	output     string

	stdin            io.Reader
	stdoutIsTerminal bool
	getenv           func(string) string
	readPassword     func(prompt string) (string, error)
	runCheck         func(context.Context, check.Request, string, check.Emitter) bool
	openTasks        func(context.Context, config.Target, string) (mssql.TaskReader, func(), error)
	// openOps opens a target for the native operations (cmd_ops.go);
	// readConfirm asks to retype a confirmation on the terminal.
	openOps     func(ctx context.Context, name string, t config.Target, password string) (*ops.Session, func(), error)
	readConfirm func(prompt string) (string, error)
}

func main() {
	a := &app{
		stdin:            os.Stdin,
		stdoutIsTerminal: term.IsTerminal(int(os.Stdout.Fd())),
		getenv:           os.Getenv,
		readPassword:     readPasswordFromTerminal,
		runCheck:         check.Run,
		openTasks:        session.OpenTasks,
		openOps:          ops.Open,
		readConfirm:      readLineFromTerminal,
	}
	os.Exit(a.execute(os.Args[1:], os.Stdout, os.Stderr))
}

func (a *app) execute(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	var exit exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	default:
		_, _ = fmt.Fprintf(stderr, "dialga: %v\n", err)
		return exitFailure
	}
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "dialga",
		Short:         "Import and export SQL Server databases on Amazon RDS through S3",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if a.output != "text" && a.output != "json" {
				return fmt.Errorf("--output must be text or json (got %q)", a.output)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&a.configPath, "config", "",
		"configuration file (default: <user config dir>/dialga/config.yaml)")
	root.PersistentFlags().StringVarP(&a.output, "output", "o", "text", "output format: text or json")
	root.AddCommand(a.versionCmd(), a.serveCmd(), a.checkCmd(), a.statusCmd(), a.waitCmd(), a.configCmd())
	for _, c := range commands {
		root.AddCommand(c(a))
	}
	return root
}

// commands are the subcommands defined in their own files: each cmd_*.go
// adds its constructor from init(), so a new command never edits this file.
//
//	func init() { commands = append(commands, (*app).uploadCmd) }
var commands []func(*app) *cobra.Command

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "dialga %s\n", version)
			return err
		},
	}
}

func (a *app) serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the local API for the desktop shell (session token on stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := a.configFile()
			if err != nil {
				return err
			}
			if code := serve(a.stdin, cmd.OutOrStdout(), cmd.ErrOrStderr(), path); code != 0 {
				return exitError{code}
			}
			return nil
		},
	}
}

// configFile is the path given with --config, or the default one.
func (a *app) configFile() (string, error) {
	if a.configPath != "" {
		return a.configPath, nil
	}
	return config.DefaultPath()
}

func (a *app) loadConfig() (*config.Config, string, error) {
	path, err := a.configFile()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, path, fmt.Errorf("no configuration at %s (start from docs/config.example.yaml)", path)
	}
	return cfg, path, err
}

func readPasswordFromTerminal(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no terminal to ask for it: set sql.password to env:VAR")
	}
	_, _ = fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	return string(password), err
}
