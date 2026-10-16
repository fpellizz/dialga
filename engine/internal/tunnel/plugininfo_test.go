package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginInfo(t *testing.T) {
	known := filepath.Join(t.TempDir(), pluginName)
	if err := os.WriteFile(known, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notOnPath := func(string) (string, error) { return "", errors.New("not on PATH") }
	version := func(_ context.Context, path string) ([]byte, error) {
		if path != known {
			t.Fatalf("ran %s", path)
		}
		return []byte("1.2.707.0\n"), nil
	}
	path, v, err := pluginInfo(context.Background(), notOnPath, []string{known}, version)
	if err != nil || path != known || v != "1.2.707.0" {
		t.Fatalf("found: %q %q %v", path, v, err)
	}

	failing := func(context.Context, string) ([]byte, error) { return nil, errors.New("exit status 1") }
	if path, v, err = pluginInfo(context.Background(), notOnPath, []string{known}, failing); path != known || v != "" || err == nil {
		t.Fatalf("no version: %q %q %v", path, v, err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if path, _, err = pluginInfo(context.Background(), notOnPath, []string{missing}, version); path != "" || !errors.Is(err, ErrPluginMissing) {
		t.Fatalf("missing: %q %v", path, err)
	}
}

func TestPluginTooOld(t *testing.T) {
	for v, want := range map[string]bool{"1.2.497.0": true, "1.2.498.0": false, "1.2.707.0": false, "": true, "x": true} {
		if got := PluginTooOld(v); got != want {
			t.Errorf("PluginTooOld(%q) = %v, want %v", v, got, want)
		}
	}
}
