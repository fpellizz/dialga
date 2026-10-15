package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
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
			req, err := checkRequest(t)
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

// checkRequest maps a configured target onto the engine check.
func checkRequest(t config.Target) (check.Request, error) {
	if t.Connect.Mode == config.ModeTunnel {
		return check.Request{}, errors.New("connect.mode tunnel arrives in v0.4: open the tunnel yourself and use mode external")
	}
	req := check.Request{
		Profile:         t.AWSProfile,
		Region:          t.Region,
		Instance:        t.Instance,
		LocalPort:       t.Connect.LocalPort,
		SQLUser:         t.SQL.User,
		ExpectedAccount: t.AccountID,
		Direct:          t.Connect.Mode == config.ModeDirect,
	}
	return req, req.Validate()
}

// sqlPassword resolves sql.password. DIALGA_SQL_PASSWORD answers "prompt"
// without a terminal (scripts, CI).
func (a *app) sqlPassword(t config.Target) (string, error) {
	src, err := t.SQL.PasswordSource()
	if err != nil {
		return "", err
	}
	switch src.Kind {
	case config.PasswordEnv:
		if v := a.getenv(src.Ref); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", src.Ref)
	case config.PasswordPrompt:
		if v := a.getenv("DIALGA_SQL_PASSWORD"); v != "" {
			return v, nil
		}
		return a.readPassword(fmt.Sprintf("SQL password for %s on %s: ", t.SQL.User, t.Instance))
	default:
		return "", fmt.Errorf("password source %q is not supported yet", src.Kind)
	}
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
	if !passed {
		msg = "Some checks failed."
	}
	_, err := fmt.Fprintln(r.w, msg)
	return err
}

func (r *checkReport) tag(s check.Status) string {
	text, color := "SKIP", "33" // yellow
	switch s {
	case check.OK:
		text, color = "OK  ", "32" // green
	case check.Failed:
		text, color = "FAIL", "31" // red
	}
	if !r.color {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}
