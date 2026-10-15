package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show and validate the configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the configuration file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				path, err := a.configFile()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
				return err
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Check the configuration file and list its targets",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, path, err := a.loadConfig()
				if err != nil {
					return err
				}
				if a.output == "json" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
						Path    string   `json:"path"`
						Valid   bool     `json:"valid"`
						Targets []string `json:"targets"`
					}{path, true, cfg.Names()})
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: valid, %d target(s): %s\n",
					path, len(cfg.Targets), strings.Join(cfg.Names(), ", "))
				return err
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the configuration with the defaults applied (YAML)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, _, err := a.loadConfig()
				if err != nil {
					return err
				}
				out, err := cfg.Marshal()
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(out)
				return err
			},
		},
	)
	return cmd
}
