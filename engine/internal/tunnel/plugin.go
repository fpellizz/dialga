package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// pluginName is the executable of the AWS Session Manager plugin.
const pluginName = "session-manager-plugin"

// responseEnv carries the StartSession answer to the plugin, as the AWS CLI
// does with recent plugins: the token never shows in the process list.
const responseEnv = "AWS_SSM_START_SESSION_RESPONSE"

// lastPluginWithoutEnv is the last plugin version that cannot read
// responseEnv (the threshold of the AWS CLI).
const lastPluginWithoutEnv = "1.2.497.0"

// stopGrace is how long the plugin has to end the session after SIGTERM.
const stopGrace = 5 * time.Second

// ErrPluginMissing means session-manager-plugin is not installed.
var ErrPluginMissing = errors.New(pluginName + " not found")

// Launcher starts session-manager-plugin.
type Launcher interface {
	// Start runs the plugin with args, and env on top of the engine's environment.
	Start(args, env []string) (Process, error)
}

// Process is a running session-manager-plugin.
type Process interface {
	// Wait blocks until the process exits.
	Wait() error
	// Stop asks the process to end, kills it after a grace period and
	// returns once it is gone. It does nothing on an exited process.
	Stop()
	// Output is the last meaningful line the process printed, never a secret.
	Output() string
}

// FindPlugin looks for session-manager-plugin on PATH, then where its
// installers put it, and checks that it reads the session from the
// environment.
func FindPlugin(ctx context.Context) (Launcher, error) {
	path, err := lookPlugin(exec.LookPath, knownPluginPaths(runtime.GOOS))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("%s --version: %w", path, err)
	}
	v := strings.TrimSpace(string(out))
	if !readsEnvResponse(v) {
		return nil, fmt.Errorf("%s %s is too old, Dialga needs a version after %s: update it (%s)",
			pluginName, v, lastPluginWithoutEnv, installHint(runtime.GOOS, runtime.GOARCH))
	}
	return pluginLauncher{path: path}, nil
}

// lookPlugin tries PATH first: a desktop app may start with a short PATH,
// hence the known install locations.
func lookPlugin(look func(string) (string, error), known []string) (string, error) {
	if p, err := look(pluginName); err == nil {
		return p, nil
	}
	for _, p := range known {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w on PATH: install it (%s)", ErrPluginMissing, installHint(runtime.GOOS, runtime.GOARCH))
}

func knownPluginPaths(goos string) []string {
	switch goos {
	case "windows":
		return []string{`C:\Program Files\Amazon\SessionManagerPlugin\bin\session-manager-plugin.exe`}
	case "darwin":
		return []string{"/usr/local/bin/" + pluginName, "/opt/homebrew/bin/" + pluginName,
			"/usr/local/sessionmanagerplugin/bin/" + pluginName}
	default:
		return []string{"/usr/local/bin/" + pluginName, "/usr/local/sessionmanagerplugin/bin/" + pluginName}
	}
}

func installHint(goos, goarch string) string {
	const doc = "https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html"
	if goos != "linux" {
		return doc
	}
	arch := "64bit"
	if goarch == "arm64" {
		arch = "arm64"
	}
	return fmt.Sprintf("Fedora/RHEL: sudo dnf install https://s3.amazonaws.com/session-manager-downloads/plugin/latest/linux_%s/%s.rpm; others: %s",
		arch, pluginName, doc)
}

// readsEnvResponse reports whether the plugin version is after
// lastPluginWithoutEnv. Anything unparsable counts as too old.
func readsEnvResponse(version string) bool {
	got, ok := parseVersion(version)
	want, _ := parseVersion(lastPluginWithoutEnv)
	if !ok {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return false
}

func parseVersion(v string) ([4]int, bool) {
	var n [4]int
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) == 0 || len(parts) > 4 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// pluginArgs are the arguments "aws ssm start-session" gives the plugin:
// the session (the name of the variable that holds it), region, operation,
// profile, the request as JSON and the SSM endpoint. The session, with its
// token, travels only in env.
func pluginArgs(out *ssm.StartSessionOutput, region, profile string, in *ssm.StartSessionInput, endpoint string) (args, env []string, err error) {
	resp, err := json.Marshal(struct {
		SessionID  string `json:"SessionId"`
		TokenValue string `json:"TokenValue"`
		StreamURL  string `json:"StreamUrl"`
	}{aws.ToString(out.SessionId), aws.ToString(out.TokenValue), aws.ToString(out.StreamUrl)})
	if err != nil {
		return nil, nil, err
	}
	req, err := json.Marshal(struct {
		Target       string              `json:"Target"`
		DocumentName string              `json:"DocumentName"`
		Parameters   map[string][]string `json:"Parameters"`
	}{aws.ToString(in.Target), aws.ToString(in.DocumentName), in.Parameters})
	if err != nil {
		return nil, nil, err
	}
	args = []string{responseEnv, region, "StartSession", profile, string(req), endpoint}
	return args, []string{responseEnv + "=" + string(resp)}, nil
}

// endpointOf resolves the SSM endpoint of the client options, as the SDK
// does for its own calls (custom endpoint, FIPS, dual stack).
func endpointOf(ctx context.Context, o ssm.Options) (string, error) {
	r := o.EndpointResolverV2
	if r == nil {
		r = ssm.NewDefaultEndpointResolverV2()
	}
	ep, err := r.ResolveEndpoint(ctx, ssm.EndpointParameters{
		Region:       aws.String(o.Region),
		UseDualStack: aws.Bool(o.EndpointOptions.UseDualStackEndpoint == aws.DualStackEndpointStateEnabled),
		UseFIPS:      aws.Bool(o.EndpointOptions.UseFIPSEndpoint == aws.FIPSEndpointStateEnabled),
		Endpoint:     o.BaseEndpoint,
	})
	if err != nil {
		return "", err
	}
	return ep.URI.String(), nil
}

// pluginLauncher runs the real plugin.
type pluginLauncher struct{ path string }

func (l pluginLauncher) Start(args, env []string) (Process, error) {
	cmd := exec.Command(l.path, args...)
	cmd.Env = append(os.Environ(), env...)
	out := &lastLine{}
	cmd.Stdout, cmd.Stderr = out, out
	// Its own process group: Ctrl-C reaches the engine only, which then
	// closes the tunnel in order (see Stop).
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &pluginProcess{cmd: cmd, out: out, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type pluginProcess struct {
	cmd  *exec.Cmd
	out  *lastLine
	done chan struct{}
	err  error // valid once done is closed
}

func (p *pluginProcess) Wait() error {
	<-p.done
	return p.err
}

// Stop sends SIGTERM (the plugin then ends the session on the agent) and
// kills the process if it is still there after stopGrace.
func (p *pluginProcess) Stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = terminate(p.cmd.Process)
	select {
	case <-p.done:
	case <-time.After(stopGrace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *pluginProcess) Output() string { return p.out.String() }

// lastLine keeps the last meaningful line written to it: not the routine
// messages of the plugin, never a line that could carry the session token.
type lastLine struct {
	mu      sync.Mutex
	pending []byte
	last    string
}

const maxLine = 300

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(l.pending, p...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			break
		}
		l.keep(string(l.pending[:i]))
		l.pending = l.pending[i+1:]
	}
	if len(l.pending) > 4*maxLine {
		l.keep(string(l.pending))
		l.pending = l.pending[:0]
	}
	return len(p), nil
}

func (l *lastLine) keep(line string) {
	line = strings.TrimSpace(line)
	if line == "" || routineLine(line) || secretLine(line) {
		return
	}
	if r := []rune(line); len(r) > maxLine {
		line = string(r[:maxLine-1]) + "…"
	}
	l.last = line
}

func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// routineLine: what the plugin prints when all is well.
func routineLine(line string) bool {
	for _, prefix := range []string{"Starting session with", "Waiting for connections", "Connection accepted"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return strings.HasPrefix(line, "Port ") && strings.Contains(line, " opened for session")
}

func secretLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "wss://") || strings.Contains(lower, "token") || strings.Contains(lower, "streamurl")
}
