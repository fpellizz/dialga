package discover

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
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
	// Purpose says what needs it, in English (the GUI translates known ids).
	Purpose string        `json:"purpose"`
	Install []InstallHint `json:"install"`
}

// Programs finds programs on this computer; tests replace both functions.
type Programs struct {
	LookPath func(file string) (string, error)
	// Output runs a program and returns its standard output.
	Output func(ctx context.Context, path string, args ...string) ([]byte, error)
}

// LocalPrograms looks in $PATH and runs the programs for their version.
func LocalPrograms() Programs {
	return Programs{
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).Output()
		},
	}
}

// versionTimeout bounds each "--version" run.
const versionTimeout = 3 * time.Second

var versionPattern = regexp.MustCompile(`[0-9]+(\.[0-9]+)+`)

// Prerequisites reports the Session Manager plugin and the container runtime
// (podman, otherwise docker). A program that is there but does not tell its
// version is still found.
func (p Programs) Prerequisites(ctx context.Context) []Prerequisite {
	plugin := Prerequisite{
		ID: PrereqSSMPlugin, Program: "session-manager-plugin",
		Purpose: "SSM tunnels to the instances",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_64bit/session-manager-plugin.rpm"},
			{"debian", "curl -fsSLo /tmp/session-manager-plugin.deb https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_64bit/session-manager-plugin.deb && sudo dpkg -i /tmp/session-manager-plugin.deb"},
		},
	}
	p.find(ctx, &plugin, "session-manager-plugin")
	runtime := Prerequisite{
		ID: PrereqContainerRuntime, Program: "podman",
		Purpose: "inspecting .bak files in a throwaway SQL Server",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y podman"},
			{"debian", "sudo apt-get install -y podman"},
		},
	}
	p.find(ctx, &runtime, "podman", "docker")
	return []Prerequisite{plugin, runtime}
}

// find fills pr with the first of programs found in $PATH.
func (p Programs) find(ctx context.Context, pr *Prerequisite, programs ...string) {
	for _, name := range programs {
		path, err := p.LookPath(name)
		if err != nil {
			continue
		}
		pr.Program, pr.Path, pr.Found = name, path, true
		ctx, cancel := context.WithTimeout(ctx, versionTimeout)
		out, err := p.Output(ctx, path, "--version")
		cancel()
		if err == nil {
			pr.Version = parseVersion(string(out))
		}
		return
	}
}

// parseVersion takes the first dotted number of the first line:
// "1.2.707.0", "podman version 5.6.2", "Docker version 28.3.1, build 38b7060".
func parseVersion(out string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return versionPattern.FindString(first)
}
