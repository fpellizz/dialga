package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/update"
)

func updateApp(t *testing.T, in update.Info, env envMap) (*app, *[]bool) {
	t.Helper()
	a, _, _ := testApp(t, testConfig)
	var calls []bool
	a.checkUpdate = func(_ context.Context, force bool) update.Info {
		calls = append(calls, force)
		return in
	}
	a.getenv = env.get
	return a, &calls
}

var newerInfo = update.Info{Current: "dev", Latest: "9.9.9", Newer: true, URL: "https://github.com/fpellizz/dialga/releases/tag/v9.9.9"}

func TestVersionPrintsTheNoticeWhenNewer(t *testing.T) {
	a, calls := updateApp(t, newerInfo, nil)
	code, out, errOut := run(a, "version")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 || lines[1] != "Dialga 9.9.9 is available: "+newerInfo.URL || errOut != "" {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	if len(*calls) != 1 || (*calls)[0] {
		t.Fatalf("calls %v: the cache must be used", *calls)
	}
}

func TestVersionSilentWhenNotNewerOrOnError(t *testing.T) {
	for _, in := range []update.Info{{Latest: "1.0.0"}, {Error: "offline"}, {}} {
		a, _ := updateApp(t, in, nil)
		code, out, errOut := run(a, "version")
		if code != 0 || strings.Count(out, "\n") != 1 || errOut != "" {
			t.Fatalf("%+v: code %d out %q err %q", in, code, out, errOut)
		}
	}
}

func TestVersionSkipsTheCheckWhenDisabled(t *testing.T) {
	a, calls := updateApp(t, newerInfo, envMap{noUpdateCheckEnv: "1"})
	for _, args := range [][]string{{"version"}, {"version", "-o", "json"}} {
		if code, out, _ := run(a, args...); code != 0 || strings.Contains(out, "9.9.9") {
			t.Fatalf("%v: %d %q", args, code, out)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("calls %v", *calls)
	}
}

func TestVersionJSONHasTheUpdateObject(t *testing.T) {
	a, _ := updateApp(t, newerInfo, nil)
	code, out, _ := run(a, "version", "-o", "json")
	var doc struct {
		Version string      `json:"version"`
		Update  update.Info `json:"update"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil || doc.Version != version || !doc.Update.Newer {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestVersionCheckForcesAndNeverFails(t *testing.T) {
	for _, tc := range []struct {
		in         update.Info
		out, stder string
	}{
		{newerInfo, "Dialga 9.9.9 is available", ""},
		{update.Info{Latest: "1.0.0"}, "Up to date (latest release: 1.0.0)", ""},
		{update.Info{}, "No release published yet", ""},
		{update.Info{Error: "update check: offline"}, "dialga ", "update check: offline"},
	} {
		a, calls := updateApp(t, tc.in, envMap{noUpdateCheckEnv: "1"})
		code, out, errOut := run(a, "version", "--check")
		if code != 0 || !strings.Contains(out, tc.out) || !strings.Contains(errOut, tc.stder) {
			t.Fatalf("%+v: code %d out %q err %q", tc.in, code, out, errOut)
		}
		if len(*calls) != 1 || !(*calls)[0] {
			t.Fatalf("calls %v: --check must force", *calls)
		}
	}
}
