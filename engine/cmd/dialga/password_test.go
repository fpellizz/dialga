package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fake secret, only used by these tests
const cliFakePW = "fake-cli-pw"

var promptDoc = strings.Replace(testConfig, `"env:TEST_SQL_PASSWORD"`, "prompt", 1)

func TestPasswordSetStatusForget(t *testing.T) {
	keyring.MockInit()
	a, _, _ := testApp(t, promptDoc)
	a.stdin = strings.NewReader(cliFakePW + "\n")

	code, out, errOut := run(a, "password", "set", "dev-sql")
	if code != 0 || strings.Contains(out+errOut, cliFakePW) || !strings.Contains(out, "stored in the system keyring") {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	if v, err := keyring.Get("dialga", "dev-sql"); err != nil || v != cliFakePW {
		t.Fatalf("keyring: %q %v", v, err)
	}
	code, out, _ = run(a, "password", "status")
	if code != 0 || !strings.Contains(out, "dev-sql") || !strings.Contains(out, "prompt") || !strings.Contains(out, "stored") ||
		strings.Contains(out, cliFakePW) {
		t.Fatalf("status: %d %q", code, out)
	}
	code, out, _ = run(a, "-o", "json", "password", "status", "dev-sql")
	var rows []passwordState
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Keyring != "stored" {
		t.Fatalf("status json: %d %q", code, out)
	}
	if code, out, _ := run(a, "password", "forget", "dev-sql"); code != 0 || !strings.Contains(out, "any more") {
		t.Fatalf("forget: %d %q", code, out)
	}
	if _, err := keyring.Get("dialga", "dev-sql"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("forget left the entry: %v", err)
	}
	if code, out, _ := run(a, "password", "status", "dev-sql"); code != 0 || !strings.Contains(out, "empty") {
		t.Fatalf("status after forget: %d %q", code, out)
	}
	if code, _, _ := run(a, "password", "forget", "dev-sql"); code != 0 {
		t.Fatalf("forget of nothing: %d", code)
	}
}

func TestPasswordSetErrors(t *testing.T) {
	keyring.MockInit()
	a, _, _ := testApp(t, testConfig) // env:TEST_SQL_PASSWORD
	a.stdin = strings.NewReader(cliFakePW + "\n")
	if code, _, errOut := run(a, "password", "set", "dev-sql"); code != exitFailure || !strings.Contains(errOut, "TEST_SQL_PASSWORD") {
		t.Fatalf("env target: %d %q", code, errOut)
	}
	a, _, _ = testApp(t, promptDoc)
	a.stdin = strings.NewReader("\n")
	if code, _, errOut := run(a, "password", "set", "dev-sql"); code != exitFailure || !strings.Contains(errOut, "1 to") {
		t.Fatalf("empty: %d %q", code, errOut)
	}
	a.stdin = strings.NewReader(cliFakePW + "\n")
	if code, _, errOut := run(a, "password", "set", "nope"); code != exitFailure || strings.Contains(errOut, cliFakePW) {
		t.Fatalf("unknown target: %d %q", code, errOut)
	}
	keyring.MockInitWithError(errors.New("no service"))
	t.Cleanup(keyring.MockInit)
	a, _, _ = testApp(t, promptDoc)
	a.stdin = strings.NewReader(cliFakePW + "\n")
	if code, _, errOut := run(a, "password", "set", "dev-sql"); code != exitFailure || !strings.Contains(errOut, "no system keyring") {
		t.Fatalf("no keyring: %d %q", code, errOut)
	}
	if code, out, _ := run(a, "password", "status"); code != 0 || !strings.Contains(out, "unavailable") {
		t.Fatalf("status without keyring: %d %q", code, out)
	}
}

func TestPromptFlowTriesTheKeyringFirst(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set("dialga", "dev-sql", cliFakePW); err != nil {
		t.Fatal(err)
	}
	a, _, gotPassword := testApp(t, promptDoc) // readPassword fails the test if asked
	if code, _, errOut := run(a, "check", "dev-sql"); code != 0 {
		t.Fatalf("check: %d %q", code, errOut)
	}
	if *gotPassword != cliFakePW {
		t.Fatalf("the check ran with %q", *gotPassword)
	}
}

func TestKeyringSourceWithoutEntryPointsToPasswordSet(t *testing.T) {
	keyring.MockInit()
	a, _, _ := testApp(t, strings.Replace(testConfig, `"env:TEST_SQL_PASSWORD"`, "keyring", 1))
	cfg, _, err := a.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.sqlPassword("dev-sql", cfg.Targets["dev-sql"])
	if err == nil || !strings.Contains(err.Error(), "dialga password set dev-sql") {
		t.Fatalf("got %v", err)
	}
}
