package main

import "testing"

// userDirEnv lists the variables that decide where the engine looks for the
// user's config, data, cache and job journal directories, on every OS: HOME
// (macOS: ~/Library/Application Support, ~/Library/Caches), the XDG_* ones
// (Linux), APPDATA, LOCALAPPDATA and USERPROFILE (Windows).
func userDirEnv(root string) map[string]string {
	return map[string]string{
		"HOME":            root,
		"USERPROFILE":     root,
		"XDG_CONFIG_HOME": root + "/config",
		"XDG_DATA_HOME":   root + "/data",
		"XDG_CACHE_HOME":  root + "/cache",
		"XDG_STATE_HOME":  root + "/state",
		"APPDATA":         root + "/appdata",
		"LOCALAPPDATA":    root + "/localappdata",
	}
}

// isolateUserDirs points every user directory of every OS at a new temporary
// folder for the duration of the test and returns that folder.
func isolateUserDirs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for k, v := range userDirEnv(root) {
		t.Setenv(k, v)
	}
	return root
}
