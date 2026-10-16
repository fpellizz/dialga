package inspect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Container runtimes, in order of preference.
const (
	Podman = "podman"
	Docker = "docker"
)

// Runtime is the container engine that runs the throwaway SQL Server.
type Runtime struct {
	Name    string // podman or docker
	Path    string
	Version string
	// Rootless: the container's root is the user, so it reads the user's
	// files without changing their permissions.
	Rootless bool
	// SELinux: the bind mounts need label=disable, or the files are denied
	// (and relabelling them is not ours to do).
	SELinux bool
}

func (rt Runtime) String() string {
	s := rt.Name + " " + rt.Version
	if rt.Rootless {
		s += " · rootless"
	}
	if rt.SELinux {
		s += " · SELinux"
	}
	return s
}

// runner runs a runtime command with extra environment variables and returns
// its standard output; the error carries the end of the standard error.
type runner func(ctx context.Context, env []string, name string, args ...string) (string, error)

func execRunner(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if msg := lastLines(stderr.String(), 3); msg != "" {
			return "", fmt.Errorf("%s %s: %s", filepath.Base(name), args[0], msg)
		}
		return "", fmt.Errorf("%s %s: %w", filepath.Base(name), args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// lastLines keeps the last n non-empty lines of s, joined by " / ".
func lastLines(s string, n int) string {
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}

const installHint = "install podman (Fedora: sudo dnf install podman; Debian/Ubuntu: sudo apt install podman) or docker"

// DetectRuntime finds the container runtime inspect would use: podman, else
// docker, or the one DIALGA_CONTAINER_RUNTIME forces. It runs "info", so a
// docker daemon that does not answer is an error; the error carries the
// install hint when there is none.
func DetectRuntime(ctx context.Context) (Runtime, error) {
	return newInspector().detectRuntime(ctx)
}

// detectRuntime picks podman, else docker; DIALGA_CONTAINER_RUNTIME forces one.
func (in *inspector) detectRuntime(ctx context.Context) (Runtime, error) {
	names := []string{Podman, Docker}
	if v := in.getenv(EnvRuntime); v != "" {
		if v != Podman && v != Docker {
			return Runtime{}, fmt.Errorf("%s must be podman or docker (got %q)", EnvRuntime, v)
		}
		names = []string{v}
	}
	for _, name := range names {
		path, err := in.lookPath(name)
		if err != nil {
			continue
		}
		return in.probe(ctx, Runtime{Name: name, Path: path})
	}
	if len(names) == 1 {
		return Runtime{}, fmt.Errorf("%s is %s but %s is not installed", EnvRuntime, names[0], names[0])
	}
	return Runtime{}, errors.New("no container runtime: " + installHint)
}

// probe reads the version and the security settings of the runtime; for
// docker it also proves the daemon answers.
func (in *inspector) probe(ctx context.Context, rt Runtime) (Runtime, error) {
	format := "{{.Version.Version}}|{{.Host.Security.Rootless}}|{{.Host.Security.SELinuxEnabled}}"
	if rt.Name == Docker {
		format = "{{.ServerVersion}}|{{json .SecurityOptions}}"
	}
	out, err := in.run(ctx, nil, rt.Path, "info", "--format", format)
	if err != nil {
		if rt.Name == Docker {
			return rt, fmt.Errorf("%w (is the daemon running and your user in the docker group? Or install podman)", err)
		}
		return rt, err
	}
	fields := strings.Split(lastLines(out, 1), "|")
	if len(fields) < 2 || fields[0] == "" {
		return rt, fmt.Errorf("%s info: unexpected answer %q", rt.Name, out)
	}
	rt.Version = fields[0]
	switch rt.Name {
	case Podman:
		rt.Rootless = fields[1] == "true"
		rt.SELinux = len(fields) > 2 && fields[2] == "true"
	case Docker:
		rt.Rootless = strings.Contains(fields[1], "name=rootless")
		rt.SELinux = strings.Contains(fields[1], "name=selinux")
	}
	return rt, nil
}

var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,254}$`)

// ValidateImage rejects what could not be an image reference (or would be
// read as a flag).
func ValidateImage(image string) error {
	if !imagePattern.MatchString(image) {
		return fmt.Errorf("invalid image %q", image)
	}
	return nil
}

// imageExists fails with the pull command when the image is not local: it is
// never pulled implicitly (about 1.5 GB).
func (in *inspector) imageExists(ctx context.Context, rt Runtime, image string) error {
	args := []string{"image", "exists", image}
	if rt.Name == Docker {
		args = []string{"image", "inspect", "--format", "{{.Id}}", image}
	}
	if _, err := in.run(ctx, nil, rt.Path, args...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s is not in the local images: pull it once (about 1.5 GB) with: %s pull %s",
			image, rt.Name, image)
	}
	return nil
}

// mount is a bind mount of a host file or directory.
type mount struct {
	host, container string
	readOnly        bool
}

// sqlPort is where SQL Server listens inside the container.
const sqlPort = "1433/tcp"

// runArgs are the arguments that start the throwaway SQL Server: as root in
// the container (rootless podman maps it to the user, so the files are read
// without changing their permissions), SELinux labelling disabled for it when
// the host enforces it (the user's directories are not relabelled), SQL
// Server published on a random port of 127.0.0.1 only. The SA password comes
// from the environment of the runtime, never from the command line.
func runArgs(rt Runtime, name, image string, mounts []mount) []string {
	args := []string{"run", "-d", "--name", name, "--pull=never", "-u", "0",
		"--label", "io.dialga.inspect=true",
		"-e", "ACCEPT_EULA=Y", "-e", "MSSQL_PID=Developer", "-e", "MSSQL_SA_PASSWORD",
		"-p", "127.0.0.1::1433"}
	if rt.SELinux {
		args = append(args, "--security-opt", "label=disable")
	}
	for _, m := range mounts {
		v := m.host + ":" + m.container
		if m.readOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	return append(args, image)
}

// removeArgs force-remove the container at once. podman ignores a container
// that is already gone; docker says "No such container".
func removeArgs(rt Runtime, name string) []string {
	if rt.Name == Podman {
		return []string{"rm", "-f", "-i", "-t", "0", name}
	}
	return []string{"rm", "-f", name}
}

// container is one throwaway SQL Server.
type container struct {
	in   *inspector
	rt   Runtime
	name string
}

// start runs the container and returns the address SQL Server is published on.
func (c *container) start(ctx context.Context, image string, mounts []mount, password string) (string, error) {
	env := []string{"MSSQL_SA_PASSWORD=" + password}
	if _, err := c.in.run(ctx, env, c.rt.Path, runArgs(c.rt, c.name, image, mounts)...); err != nil {
		return "", err
	}
	out, err := c.in.run(ctx, nil, c.rt.Path, "port", c.name, sqlPort)
	if err != nil {
		return "", err
	}
	return publishedAddr(out)
}

// publishedAddr turns "127.0.0.1:41234" (the first line of "port") into the
// address to dial.
func publishedAddr(out string) (string, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	_, port, err := net.SplitHostPort(strings.TrimSpace(line))
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("no published port for SQL Server (%q)", out)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

// running reports whether the container is still up.
func (c *container) running(ctx context.Context) (bool, error) {
	out, err := c.in.run(ctx, nil, c.rt.Path, "inspect", "-f", "{{.State.Running}}", c.name)
	return strings.TrimSpace(out) == "true", err
}

// logs returns the last lines SQL Server wrote.
func (c *container) logs(ctx context.Context) string {
	out, err := c.in.run(ctx, nil, c.rt.Path, "logs", "--tail", "20", c.name)
	if err != nil {
		return ""
	}
	return lastLines(out, 3)
}

// waitReady logs in as sa until SQL Server answers, the container stops or
// the deadline passes, and returns the session and the server version.
func (c *container) waitReady(ctx context.Context, addr, password string) (querier, string, error) {
	deadline := time.Now().Add(c.in.ready)
	for {
		q, err := c.in.connect(ctx, addr, password)
		if err == nil {
			var rows []row
			if rows, err = q.query(ctx, "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(30)) AS version"); err == nil {
				if len(rows) == 1 {
					return q, rows[0]["version"], nil
				}
				err = errors.New("no version in the answer")
			}
			_ = q.Close()
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if up, rerr := c.running(ctx); rerr == nil && !up {
			return nil, "", fmt.Errorf("SQL Server stopped: %s", c.logs(ctx))
		}
		if time.Now().After(deadline) {
			return nil, "", fmt.Errorf("SQL Server not ready after %s: %v", c.in.ready, err)
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(c.in.poll):
		}
	}
}

// remove deletes the container, with its own deadline: it runs on cancel too.
func (c *container) remove(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.in.cleanup)
	defer cancel()
	_, err := c.in.run(ctx, nil, c.rt.Path, removeArgs(c.rt, c.name)...)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}
