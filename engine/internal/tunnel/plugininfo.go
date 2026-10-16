package tunnel

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LastPluginWithoutEnv is the newest session-manager-plugin that FindPlugin
// refuses: it cannot read the session from AWS_SSM_START_SESSION_RESPONSE.
const LastPluginWithoutEnv = lastPluginWithoutEnv

// PluginInfo finds session-manager-plugin where FindPlugin looks (PATH, then
// the install locations) and reads its version, without judging it: the
// prerequisites check reports what FindPlugin will find. path is empty when
// the plugin is missing (err wraps ErrPluginMissing); version is empty when
// "--version" fails (err says why).
func PluginInfo(ctx context.Context) (path, version string, err error) {
	return pluginInfo(ctx, exec.LookPath, knownPluginPaths(runtime.GOOS),
		func(ctx context.Context, path string) ([]byte, error) {
			return exec.CommandContext(ctx, path, "--version").Output()
		})
}

func pluginInfo(ctx context.Context, look func(string) (string, error), known []string,
	versionOf func(context.Context, string) ([]byte, error)) (string, string, error) {
	path, err := lookPlugin(look, known)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := versionOf(ctx, path)
	if err != nil {
		return path, "", err
	}
	return path, strings.TrimSpace(string(out)), nil
}

// PluginTooOld reports whether FindPlugin refuses this version of the plugin
// (an unparsable version counts as too old).
func PluginTooOld(version string) bool { return !readsEnvResponse(version) }
