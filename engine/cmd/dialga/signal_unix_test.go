//go:build unix

package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// A command stopped by SIGPIPE (its reader went away) or SIGTERM gets its
// context cancelled like with Ctrl-C, so its deferred cleanup runs.
func TestStopSignalsCancelTheCommand(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGPIPE, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			a, _, _ := testApp(t, testConfig)
			saved := commands
			t.Cleanup(func() { commands = saved })
			started := make(chan struct{})
			commands = append(slices.Clone(saved), func(*app) *cobra.Command {
				return &cobra.Command{Use: "block", RunE: func(cmd *cobra.Command, _ []string) error {
					close(started)
					select {
					case <-cmd.Context().Done():
						return cmd.Context().Err()
					case <-time.After(5 * time.Second):
						return errors.New("not cancelled")
					}
				}}
			})
			type result struct {
				code   int
				stderr string
			}
			done := make(chan result, 1)
			go func() {
				code, _, errOut := run(a, "block")
				done <- result{code, errOut}
			}()
			<-started
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			res := <-done
			if res.code != exitFailure || !strings.Contains(res.stderr, "context canceled") {
				t.Fatalf("code %d, stderr %q", res.code, res.stderr)
			}
		})
	}
}
