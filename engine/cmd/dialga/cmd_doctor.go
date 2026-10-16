package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/discover"
)

func init() { commands = append(commands, (*app).doctorCmd) }

// doctorItem is one verdict of dialga doctor.
type doctorItem struct {
	ID      string                 `json:"id"`
	Label   string                 `json:"label"`
	Status  check.Status           `json:"status"`
	Detail  string                 `json:"detail,omitempty"`
	Install []discover.InstallHint `json:"install,omitempty"`
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this computer: prerequisites, AWS profiles, configuration",
		Long: `Check this computer, one verdict per item: session-manager-plugin, the
container runtime (podman or docker), the AWS profiles, the configuration
file and, for each target, its AWS profile and SQL password source.

No AWS call: "dialga check" verifies a target against AWS.

Exit status: 0 ready (warnings included), 2 something to fix, 1 error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			items := a.doctor(cmd)
			ok := true
			for _, it := range items {
				ok = ok && it.Status != check.Failed
			}
			if a.output == "json" {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					OK    bool         `json:"ok"`
					Items []doctorItem `json:"items"`
				}{ok, items}); err != nil {
					return err
				}
			} else if err := a.printDoctor(cmd.OutOrStdout(), items, ok); err != nil {
				return err
			}
			if !ok {
				return exitError{exitCheckFailed}
			}
			return nil
		},
	}
}

func (a *app) doctor(cmd *cobra.Command) []doctorItem {
	d := a.discovery()
	var items []doctorItem

	// The configuration first: what the targets use decides how much a
	// missing program matters.
	path, pathErr := a.configFile()
	conf := doctorItem{ID: "config", Label: "Configuration", Status: check.OK}
	var cfg *config.Config
	switch {
	case pathErr != nil:
		conf.Status, conf.Detail = check.Failed, pathErr.Error()
	default:
		var err error
		cfg, err = config.Load(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			conf.Status, conf.Detail = check.Failed, path+" does not exist: add a target with dialga config init"
		case err != nil:
			conf.Status, conf.Detail = check.Failed, err.Error()
		default:
			conf.Detail = fmt.Sprintf("%s: %d target(s)", path, len(cfg.Targets))
		}
	}
	allDirect, anyTunnel := cfg != nil, false
	if cfg != nil {
		for _, t := range cfg.Targets {
			allDirect = allDirect && t.Connect.Mode == config.ModeDirect
			anyTunnel = anyTunnel || t.Connect.Mode == config.ModeTunnel
		}
	}

	for _, pr := range d.Prerequisites(cmd.Context()) {
		it := doctorItem{ID: pr.ID, Label: pr.Program, Status: check.OK}
		if pr.ID == discover.PrereqContainerRuntime {
			it.Label = "Container runtime"
		}
		switch {
		case pr.Found && pr.Outdated:
			// Only the integrated tunnel needs the newer plugin.
			it.Status, it.Install = check.Warning, pr.Install
			if anyTunnel {
				it.Status = check.Failed
			}
			it.Detail = fmt.Sprintf("%s (%s): too old for the integrated tunnel, which needs a version newer than %s",
				pr.Version, pr.Path, discover.LastPluginWithoutEnv)
		case pr.Found:
			it.Detail = strings.TrimSpace(pr.Program + " " + pr.Version + " (" + pr.Path + ")")
			if pr.ID != discover.PrereqContainerRuntime {
				it.Detail = strings.TrimSpace(pr.Version + " (" + pr.Path + ")")
			}
		default:
			it.Status, it.Detail, it.Install = check.Failed, "not found: needed for "+pr.Purpose, pr.Install
			if pr.Error != "" {
				it.Detail = "not usable: " + pr.Error
			}
			// Only inspect needs a container runtime; direct targets need no tunnel.
			if pr.ID == discover.PrereqContainerRuntime || (pr.ID == discover.PrereqSSMPlugin && allDirect) {
				it.Status = check.Warning
			}
		}
		items = append(items, it)
	}

	profiles, err := d.Profiles()
	aws := doctorItem{ID: "aws-profiles", Label: "AWS profiles", Status: check.OK}
	switch {
	case err != nil:
		aws.Status, aws.Detail = check.Failed, err.Error()
	case len(profiles) == 0:
		configFile, credentialsFile := discover.ProfileFiles(a.getenv)
		aws.Status = check.Failed
		aws.Detail = fmt.Sprintf("none in %s or %s: create one with aws configure (or aws configure sso)", configFile, credentialsFile)
	default:
		aws.Detail = fmt.Sprintf("%d: %s", len(profiles), profileNames(profiles))
	}
	items = append(items, aws, conf)

	if cfg != nil {
		for _, name := range cfg.Names() {
			t := cfg.Targets[name]
			it := doctorItem{ID: "target:" + name, Label: "Target " + name, Status: check.OK,
				Detail: fmt.Sprintf("profile %s, %s, instance %s, %s", t.AWSProfile, t.Region, t.Instance, t.Connect.Mode)}
			if !hasProfile(profiles, t.AWSProfile) && t.AWSProfile != "default" {
				it.Status, it.Detail = check.Failed, fmt.Sprintf("AWS profile %s is not on this computer", t.AWSProfile)
			} else if src, _ := t.SQL.PasswordSource(); src.Kind == config.PasswordEnv && a.getenv(src.Ref) == "" {
				it.Status, it.Detail = check.Warning, fmt.Sprintf("%s; SQL password: environment variable %s is empty", it.Detail, src.Ref)
			}
			items = append(items, it)
		}
	}
	return items
}

func (a *app) printDoctor(w io.Writer, items []doctorItem, ok bool) error {
	tags := &checkReport{color: a.stdoutIsTerminal}
	warnings := 0
	for _, it := range items {
		if it.Status == check.Warning {
			warnings++
		}
		_, _ = fmt.Fprintf(w, "  %s  %-26s %s\n", tags.tag(it.Status), it.Label, it.Detail)
		for _, h := range it.Install {
			_, _ = fmt.Fprintf(w, "        %-26s %s\n", "install on "+distroName(h.Distro)+":", h.Command)
		}
	}
	msg := "Ready."
	switch {
	case !ok:
		msg = "Not ready: fix the items marked FAIL."
	case warnings > 0:
		msg = "Ready, with warnings."
	}
	_, err := fmt.Fprintln(w, msg)
	return err
}

func distroName(id string) string {
	switch id {
	case "fedora":
		return "Fedora"
	case "debian":
		return "Debian/Ubuntu"
	}
	return id
}
