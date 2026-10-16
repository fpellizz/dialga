package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/session"
)

func (a *app) checkCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "check [target]",
		Short: "Run the read-only pre-flight checks of a target",
		Long: `Run the read-only pre-flight checks of a target: AWS credentials and
account, RDS instance, connection (tunnel or direct), SQL Server login with
the certificate verified, databases and native backup tasks.

Exit status: 0 all checks passed, 2 some check failed, 1 error.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				target = args[0]
			}
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			name, t, err := cfg.Target(target)
			if err != nil {
				return err
			}
			req, err := check.RequestFor(t)
			if err != nil {
				return err
			}
			// Without a password the SQL steps are skipped, the others still run.
			password, err := a.sqlPassword(t)
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: SQL login will be skipped: %v\n", err)
			}
			report := newCheckReport(name, t, a.output == "json", cmd.OutOrStdout(), a.stdoutIsTerminal)
			passed := a.runCheck(cmd.Context(), req, password, report.event)
			if err := report.finish(passed); err != nil {
				return err
			}
			if !passed {
				return exitError{exitCheckFailed}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	return cmd
}

// sqlPassword resolves sql.password; with "prompt" and no DIALGA_SQL_PASSWORD
// it asks on the terminal.
func (a *app) sqlPassword(t config.Target) (string, error) {
	password, err := session.PasswordFromEnv(t, a.getenv)
	if errors.Is(err, session.ErrPromptNeeded) {
		return a.readPassword(fmt.Sprintf("SQL password for %s on %s: ", t.SQL.User, t.Instance))
	}
	return password, err
}

// checkReport prints the steps as they end (text) or one document at the end (json).
type checkReport struct {
	target string
	tgt    config.Target
	json   bool
	w      io.Writer
	color  bool
	steps  []check.Event
}

func newCheckReport(target string, t config.Target, asJSON bool, w io.Writer, color bool) *checkReport {
	r := &checkReport{target: target, tgt: t, json: asJSON, w: w, color: color}
	if !asJSON {
		_, _ = fmt.Fprintf(w, "Checking %s: instance %s, profile %s, %s\n", target, t.Instance, t.AWSProfile, t.Region)
	}
	return r
}

func (r *checkReport) event(e check.Event) {
	if e.Status == check.Running {
		return
	}
	r.steps = append(r.steps, e)
	if r.json {
		return
	}
	_, _ = fmt.Fprintf(r.w, "  %s  %-34s %s\n", r.tag(e.Status), e.Label, e.Detail)
}

func (r *checkReport) finish(passed bool) error {
	if r.json {
		return json.NewEncoder(r.w).Encode(struct {
			Target   string        `json:"target"`
			Instance string        `json:"instance"`
			OK       bool          `json:"ok"`
			Steps    []check.Event `json:"steps"`
		}{r.target, r.tgt.Instance, passed, r.steps})
	}
	msg := "All checks passed."
	switch {
	case !passed:
		msg = "Some checks failed."
	case r.warnings() > 0:
		msg = "All checks passed, with warnings."
	}
	_, err := fmt.Fprintln(r.w, msg)
	return err
}

func (r *checkReport) warnings() int {
	n := 0
	for _, e := range r.steps {
		if e.Status == check.Warning {
			n++
		}
	}
	return n
}

func (r *checkReport) tag(s check.Status) string {
	text, color := "SKIP", "90" // grey
	switch s {
	case check.OK:
		text, color = "OK  ", "32" // green
	case check.Warning:
		text, color = "WARN", "33" // yellow
	case check.Failed:
		text, color = "FAIL", "31" // red
	}
	if !r.color {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}
