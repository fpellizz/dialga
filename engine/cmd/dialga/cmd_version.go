package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/update"
)

// noUpdateCheckEnv=1 turns the automatic update check off.
const noUpdateCheckEnv = "DIALGA_NO_UPDATE_CHECK"

// passiveUpdateTimeout bounds the check that "dialga version" does on its
// own: the command must stay quick even offline.
const passiveUpdateTimeout = 3 * time.Second

func (a *app) updateInfo(ctx context.Context, force bool, timeout time.Duration) update.Info {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if a.checkUpdate != nil {
		return a.checkUpdate(ctx, force)
	}
	return update.New(version).Check(ctx, force)
}

func (a *app) versionCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version, and say when a newer release is published",
		Long: `Print the version. When a newer release is published on GitHub, one more line
says where to get it. The answer is cached for 12 hours in the user cache
directory and the check never fails or delays the command for more than a few
seconds; set ` + noUpdateCheckEnv + `=1 to turn it off. The only network access is
one GET of the latest release to api.github.com.

With --check the cache is ignored and the result is always printed (exit 0
even when offline, the error goes to stderr).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			disabled := a.getenv(noUpdateCheckEnv) == "1"
			asJSON := a.output == "json"
			switch {
			case force:
				return a.versionCheck(cmd, asJSON)
			case asJSON:
				doc := struct {
					Version string       `json:"version"`
					Update  *update.Info `json:"update,omitempty"`
				}{Version: version}
				if !disabled {
					in := a.updateInfo(cmd.Context(), false, passiveUpdateTimeout)
					doc.Update = &in
				}
				return json.NewEncoder(out).Encode(doc)
			}
			if _, err := fmt.Fprintf(out, "dialga %s\n", version); err != nil {
				return err
			}
			if !disabled {
				if in := a.updateInfo(cmd.Context(), false, passiveUpdateTimeout); in.Newer {
					_, _ = fmt.Fprintf(out, "Dialga %s is available: %s\n", in.Latest, in.URL)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "check", false, "ask GitHub now (ignore the cache) and print the result")
	return cmd
}

// versionCheck is "version --check": it always answers, and being offline is
// not a failure.
func (a *app) versionCheck(cmd *cobra.Command, asJSON bool) error {
	out := cmd.OutOrStdout()
	in := a.updateInfo(cmd.Context(), true, update.Timeout+time.Second)
	if in.Error != "" {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "dialga: %s\n", in.Error)
	}
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Version string      `json:"version"`
			Update  update.Info `json:"update"`
		}{version, in})
	}
	if _, err := fmt.Fprintf(out, "dialga %s\n", version); err != nil {
		return err
	}
	switch {
	case in.Newer:
		_, err := fmt.Fprintf(out, "Dialga %s is available: %s\n", in.Latest, in.URL)
		return err
	case in.Latest != "":
		_, err := fmt.Fprintf(out, "Up to date (latest release: %s).\n", in.Latest)
		return err
	case in.Error == "":
		_, err := fmt.Fprintln(out, "No release published yet.")
		return err
	}
	return nil
}
