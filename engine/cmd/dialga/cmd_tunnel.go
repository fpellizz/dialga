package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/session"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

func init() { commands = append(commands, (*app).tunnelCmd) }

// tunnelHandle is an open tunnel (*tunnel.Tunnel; the tests fake it).
type tunnelHandle interface {
	Done() <-chan struct{}
	Close() error
}

// tunnelOpener opens the integrated tunnel of a target on a local port and
// reports its states (default openTunnel).
type tunnelOpener func(context.Context, config.Target, int, func(tunnel.State)) (tunnelHandle, error)

func openTunnel(ctx context.Context, t config.Target, port int, onState func(tunnel.State)) (tunnelHandle, error) {
	tun, err := session.OpenTunnel(ctx, t, port, onState)
	if err != nil {
		return nil, err
	}
	return tun, nil
}

func (a *app) tunnelCmd() *cobra.Command {
	var target string
	var port int
	cmd := &cobra.Command{
		Use:   "tunnel [target]",
		Short: "Open the SSM tunnel to a target and keep it open until Ctrl-C",
		Long: `Open the SSM tunnel to the SQL Server of a target, whatever its connect.mode,
through the bridge instance: tag Name=connect.bridge_tag (default ssm-bridge),
in the VPC of the RDS instance, running. It needs session-manager-plugin.

The tunnel stays in the foreground and reconnects by itself when the session
drops; Ctrl-C or SIGTERM closes it and terminates the session. The local port
is --port, else connect.local_port, else a free one. SQL clients connect to
127.0.0.1 on that port; the certificate is issued for the RDS endpoint name.
With --output json every change of state is one JSON line.`,
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
			local := t.Connect.LocalPort
			if cmd.Flags().Changed("port") {
				if port < 0 || port > 65535 {
					return fmt.Errorf("--port must be between 0 (a free port) and 65535 (got %d)", port)
				}
				local = port
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
			defer stop()
			out := cmd.OutOrStdout()
			text := a.output != "json"
			if text {
				_, _ = fmt.Fprintf(out, "Opening the tunnel to %s: instance %s, profile %s, %s (Ctrl-C closes it).\n",
					name, t.Instance, t.AWSProfile, t.Region)
			}
			open := a.openTunnel
			if open == nil {
				open = openTunnel
			}
			tun, err := open(ctx, t, local, tunnelReport(out, name, text))
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
			case <-tun.Done():
			}
			if err := tun.Close(); err != nil {
				return fmt.Errorf("closing the tunnel: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "target name from the configuration")
	cmd.Flags().IntVar(&port, "port", 0, "local port on 127.0.0.1 (default connect.local_port, 0: a free port)")
	return cmd
}

// tunnelReport prints every state: one line of text, or one JSON document
// per line. The first time the tunnel is ready it says how to connect.
func tunnelReport(w io.Writer, target string, text bool) func(tunnel.State) {
	enc := json.NewEncoder(w)
	hinted := false
	return func(s tunnel.State) {
		if !text {
			_ = enc.Encode(struct {
				Target string `json:"target"`
				tunnel.State
			}{target, s})
			return
		}
		line := fmt.Sprintf("  [%s] %-12s %s", s.Time.Format("15:04:05"), s.Status, s.Detail)
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
		if s.Status == tunnel.Ready && !hinted {
			hinted = true
			host, _, _ := net.SplitHostPort(s.Endpoint)
			_, _ = fmt.Fprintf(w, "  SQL clients: server 127.0.0.1,%d; the certificate is issued for %s.\n", s.LocalPort, host)
		}
	}
}
