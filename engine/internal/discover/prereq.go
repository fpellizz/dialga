package discover

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

// EnvContainerRuntime forces the container runtime (podman or docker), as
// for inspect.
const EnvContainerRuntime = "DIALGA_CONTAINER_RUNTIME"

// LastPluginWithoutEnv is the newest Session Manager plugin that cannot read
// the session from AWS_SSM_START_SESSION_RESPONSE (the AWS CLI threshold):
// the integrated tunnel needs a newer one. Same rule as internal/tunnel.
const LastPluginWithoutEnv = "1.2.497.0"

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
	// Purpose says what needs it, in English (the GUI translates known ids).
	Purpose string        `json:"purpose"`
	Install []InstallHint `json:"install"`
}

// Programs finds programs on this computer; tests replace the functions.
type Programs struct {
	// LookPath finds a program name in $PATH, or checks an absolute path.
	LookPath func(file string) (string, error)
	// Output runs a program and returns its standard output.
	Output func(ctx context.Context, path string, args ...string) ([]byte, error)
	Getenv func(string) string
	GOOS   string
}

// LocalPrograms looks in $PATH (and where the installers put the plugin) and
// runs the programs for their version.
func LocalPrograms(getenv func(string) string) Programs {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Programs{
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).Output()
		},
		Getenv: getenv,
		GOOS:   runtime.GOOS,
	}
}

// versionTimeout bounds each "--version" run.
const versionTimeout = 3 * time.Second

var versionPattern = regexp.MustCompile(`[0-9]+(\.[0-9]+)+`)

// pluginPaths are where the installers put the plugin, for an app started
// with a short $PATH: the list internal/tunnel looks in.
func pluginPaths(goos string) []string {
	switch goos {
	case "windows":
		return []string{`C:\Program Files\Amazon\SessionManagerPlugin\bin\session-manager-plugin.exe`}
	case "darwin":
		return []string{"/usr/local/bin/session-manager-plugin",
			"/usr/local/sessionmanagerplugin/bin/session-manager-plugin", "/opt/homebrew/bin/session-manager-plugin"}
	}
	return []string{"/usr/local/bin/session-manager-plugin", "/usr/local/sessionmanagerplugin/bin/session-manager-plugin"}
}

// Prerequisites reports the Session Manager plugin and the container runtime
// (podman, otherwise docker, or the one $DIALGA_CONTAINER_RUNTIME names). A
// program that is there but does not tell its version is still found.
func (p Programs) Prerequisites(ctx context.Context) []Prerequisite {
	plugin := Prerequisite{
		ID: PrereqSSMPlugin, Program: "session-manager-plugin",
		Purpose: "SSM tunnels to the instances",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_64bit/session-manager-plugin.rpm"},
			{"debian", "curl -fsSLo /tmp/session-manager-plugin.deb https://s3.amazonaws.com/session-manager-downloads/plugin/latest/ubuntu_64bit/session-manager-plugin.deb && sudo dpkg -i /tmp/session-manager-plugin.deb"},
		},
	}
	p.find(ctx, &plugin, append([]string{"session-manager-plugin"}, pluginPaths(p.GOOS)...)...)
	if plugin.Version != "" && compareVersions(plugin.Version, LastPluginWithoutEnv) <= 0 {
		plugin.Outdated = true
	}

	runtimes := []string{"podman", "docker"}
	if forced := p.getenv(EnvContainerRuntime); forced == "podman" || forced == "docker" {
		runtimes = []string{forced}
	}
	containers := Prerequisite{
		ID: PrereqContainerRuntime, Program: runtimes[0],
		Purpose: "inspecting .bak files in a throwaway SQL Server",
		Install: []InstallHint{
			{"fedora", "sudo dnf install -y podman"},
			{"debian", "sudo apt-get install -y podman"},
		},
	}
	p.find(ctx, &containers, runtimes...)
	return []Prerequisite{plugin, containers}
}

func (p Programs) getenv(key string) string {
	if p.Getenv == nil {
		return ""
	}
	return p.Getenv(key)
}

// find fills pr with the first of files found: program names in $PATH, or
// absolute paths.
func (p Programs) find(ctx context.Context, pr *Prerequisite, files ...string) {
	for _, file := range files {
		path, err := p.LookPath(file)
		if err != nil {
			continue
		}
		pr.Program = strings.TrimSuffix(filepath.Base(file), ".exe")
		pr.Path, pr.Found = path, true
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

// compareVersions compares dotted numbers: -1, 0 or 1 (missing parts are 0).
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
