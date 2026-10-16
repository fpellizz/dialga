package discover

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/decisyon/dialga/engine/internal/inspect"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

// Prerequisite ids.
const (
	// PrereqSSMPlugin is the Session Manager plugin, which every SSM tunnel
	// needs (the integrated one and open-dloop-tunnels.sh).
	PrereqSSMPlugin = "session-manager-plugin"
	// PrereqContainerRuntime is podman or docker, which inspect uses to read
	// a .bak in a throwaway SQL Server.
	PrereqContainerRuntime = "container-runtime"
)

// LastPluginWithoutEnv is the newest Session Manager plugin the integrated
// tunnel refuses (tunnel.FindPlugin).
const LastPluginWithoutEnv = tunnel.LastPluginWithoutEnv

// InstallHint is the command that installs a missing program on a distro.
type InstallHint struct {
	Distro  string `json:"distro"` // fedora or debian (also Ubuntu)
	Command string `json:"command"`
}

// Prerequisite is a program Dialga runs, found or not.
type Prerequisite struct {
	ID string `json:"id"`
	// Program is the program found, or the one to install when none is.
	Program string `json:"program"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	// Outdated: found, but too old for what needs it (a Session Manager
	// plugin not newer than LastPluginWithoutEnv).
	Outdated bool `json:"outdated,omitempty"`
	// Error says why the program cannot be used although it may be there
	// (a docker daemon that does not answer), in English.
	Error string `json:"error,omitempty"`
	// Purpose says what needs it, in English (the GUI translates known ids).
	Purpose string        `json:"purpose"`
	Install []InstallHint `json:"install"`
}

// Programs finds the programs the way the code that runs them does; tests
// replace the lookups.
type Programs struct {
	// Plugin is tunnel.PluginInfo: path and version of session-manager-plugin.
	Plugin func(context.Context) (path, version string, err error)
	// Runtime is inspect.DetectRuntime: the runtime inspect would use.
	Runtime func(context.Context) (inspect.Runtime, error)
	Getenv  func(string) string
}

// LocalPrograms asks the lookups of the integrated tunnel and of inspect, so
// the prerequisites say what those will find.
func LocalPrograms() Programs {
	return Programs{Plugin: tunnel.PluginInfo, Runtime: inspect.DetectRuntime, Getenv: os.Getenv}
}

// lookupTimeout bounds the lookups (they run "--version" and "info").
const lookupTimeout = 15 * time.Second

// Prerequisites reports the Session Manager plugin and the container runtime
// (podman, otherwise docker, or the one DIALGA_CONTAINER_RUNTIME names). A
// plugin that is there but does not tell its version is still found.
func (p Programs) Prerequisites(ctx context.Context) []Prerequisite {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	plugin := Prerequisite{
		ID: PrereqSSMPlugin, Program: "session-manager-plugin",
		Purpose: "SSM tunnels to the instances",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_64bit/session-manager-plugin.rpm"},
			{"debian", "curl -fsSLo /tmp/session-manager-plugin.deb https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_64bit/session-manager-plugin.deb && sudo dpkg -i /tmp/session-manager-plugin.deb"},
		},
	}
	path, version, err := p.Plugin(ctx)
	switch {
	case path != "":
		plugin.Found, plugin.Path, plugin.Version = true, path, version
		plugin.Outdated = version != "" && tunnel.PluginTooOld(version)
	case err != nil && !errors.Is(err, tunnel.ErrPluginMissing):
		plugin.Error = err.Error()
	}

	containers := Prerequisite{
		ID: PrereqContainerRuntime, Program: inspect.Podman,
		Purpose: "inspecting .bak files in a throwaway SQL Server",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y podman"},
			{"debian", "sudo apt-get install -y podman"},
		},
	}
	forced := ""
	if p.Getenv != nil {
		forced = p.Getenv(inspect.EnvRuntime)
	}
	rt, err := p.Runtime(ctx)
	switch {
	case err == nil:
		containers.Program, containers.Path, containers.Version, containers.Found = rt.Name, rt.Path, rt.Version, true
	case rt.Path != "":
		// There, but not usable: a docker daemon that does not answer.
		containers.Program, containers.Path, containers.Error = rt.Name, rt.Path, err.Error()
	case forced == inspect.Podman || forced == inspect.Docker:
		containers.Program = forced // forced, and not installed
	case forced != "" || ctx.Err() != nil:
		containers.Error = err.Error() // a wrong DIALGA_CONTAINER_RUNTIME, or no answer in time
	}
	return []Prerequisite{plugin, containers}
}
