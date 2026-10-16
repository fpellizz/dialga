package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/check"
)

const testConfig = `apiVersion: v1
targets:
  dev-sql:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: default
    instance: dev-sqlserver
    bucket: dev-sqlserver-backups
    sql: { user: admin, password: "env:TEST_SQL_PASSWORD" }
    connect: { mode: external, local_port: 11433 }
`

type envMap map[string]string

func (m envMap) get(k string) string { return m[k] }

// testApp returns an app whose check run is faked: it records the request
// and password and emits the given final statuses.
func testApp(t *testing.T, doc string, statuses ...check.Status) (*app, *check.Request, *string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotReq check.Request
	var gotPassword string
	a := &app{
		configPath: path,
		stdin:      strings.NewReader(""),
		getenv:     envMap{"TEST_SQL_PASSWORD": "s3cret"}.get,
		readPassword: func(string) (string, error) {
			t.Fatal("unexpected password prompt")
			return "", nil
		},
		runCheck: func(_ context.Context, req check.Request, password string, emit check.Emitter) bool {
			gotReq, gotPassword = req, password
			ok := true
			for i, s := range statuses {
				emit(check.Event{Step: "s", Label: "Step", Status: check.Running})
				emit(check.Event{Step: "s", Label: "Step", Status: s, Detail: string(rune('a' + i))})
				ok = ok && s == check.OK
			}
			return ok
		},
	}
	return a, &gotReq, &gotPassword
}

func run(a *app, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	args = append([]string{"--config", a.configPath}, args...)
	code = a.execute(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionAndUnknownCommand(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	if code, out, _ := run(a, "version"); code != 0 || !strings.HasPrefix(out, "dialga ") {
		t.Fatalf("version: code %d, out %q", code, out)
	}
	if code, _, errOut := run(a, "nope"); code != exitFailure || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown command: code %d, stderr %q", code, errOut)
	}
}

func TestConfigValidate(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	if code, out, _ := run(a, "config", "validate"); code != 0 || !strings.Contains(out, "valid, 1 target(s): dev-sql") {
		t.Fatalf("validate: code %d, out %q", code, out)
	}
	code, out, _ := run(a, "config", "validate", "-o", "json")
	var doc struct {
		Valid   bool     `json:"valid"`
		Targets []string `json:"targets"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil || !doc.Valid || len(doc.Targets) != 1 {
		t.Fatalf("validate json: code %d, out %q", code, out)
	}
	bad, _, _ := testApp(t, strings.Replace(testConfig, "mode: external", "mode: vpn", 1))
	if code, _, errOut := run(bad, "config", "validate"); code != exitFailure || !strings.Contains(errOut, "connect.mode") {
		t.Fatalf("invalid config: code %d, stderr %q", code, errOut)
	}
}

func TestMissingConfig(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	a.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	if code, _, errOut := run(a, "check"); code != exitFailure || !strings.Contains(errOut, "no configuration") {
		t.Fatalf("missing config: code %d, stderr %q", code, errOut)
	}
}

func TestCheckPassedText(t *testing.T) {
	a, req, password := testApp(t, testConfig, check.OK, check.OK)
	code, out, _ := run(a, "check")
	if code != 0 || !strings.Contains(out, "All checks passed.") || strings.Count(out, "OK  ") != 2 {
		t.Fatalf("code %d, out %q", code, out)
	}
	want := check.Request{Profile: "default", Region: "eu-west-1", Instance: "dev-sqlserver", LocalPort: 11433,
		SQLUser: "admin", Bucket: "dev-sqlserver-backups", ExpectedAccount: "123456789012"}
	if *req != want || *password != "s3cret" {
		t.Fatalf("request %+v, password %q", *req, *password)
	}
}

func TestCheckWarningsDoNotFail(t *testing.T) {
	a, _, _ := testApp(t, testConfig, check.OK, check.Warning)
	a.runCheck = func(_ context.Context, _ check.Request, _ string, emit check.Emitter) bool {
		emit(check.Event{Step: "s", Label: "Storage", Status: check.Warning, Detail: "no CloudWatch"})
		return true
	}
	code, out, _ := run(a, "check")
	if code != 0 || !strings.Contains(out, "WARN  Storage") || !strings.Contains(out, "All checks passed, with warnings.") {
		t.Fatalf("code %d, out %q", code, out)
	}
}

func TestCheckFailedJSON(t *testing.T) {
	a, _, _ := testApp(t, testConfig, check.OK, check.Failed, check.Skipped)
	code, out, _ := run(a, "check", "dev-sql", "-o", "json")
	var doc struct {
		Target string        `json:"target"`
		OK     bool          `json:"ok"`
		Steps  []check.Event `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != exitCheckFailed || doc.OK || doc.Target != "dev-sql" || len(doc.Steps) != 3 {
		t.Fatalf("code %d, doc %+v", code, doc)
	}
}

func TestCheckModes(t *testing.T) {
	direct, req, _ := testApp(t, strings.Replace(testConfig, "mode: external, local_port: 11433", "mode: direct", 1), check.OK)
	if code, _, _ := run(direct, "check"); code != 0 || !req.Direct {
		t.Fatalf("direct: code %d, request %+v", code, *req)
	}
	tunnel, _, _ := testApp(t, strings.Replace(testConfig, "mode: external", "mode: tunnel", 1))
	if code, _, errOut := run(tunnel, "check"); code != exitFailure || !strings.Contains(errOut, "v0.4") {
		t.Fatalf("tunnel: code %d, stderr %q", code, errOut)
	}
}

func TestCheckWithoutPasswordStillRuns(t *testing.T) {
	a, _, password := testApp(t, testConfig, check.OK)
	a.getenv = envMap{}.get
	code, _, errOut := run(a, "check")
	if code != 0 || *password != "" || !strings.Contains(errOut, "TEST_SQL_PASSWORD is empty") {
		t.Fatalf("code %d, password %q, stderr %q", code, *password, errOut)
	}
}

func TestBadOutputFlag(t *testing.T) {
	a, _, _ := testApp(t, testConfig)
	if code, _, errOut := run(a, "check", "-o", "yaml"); code != exitFailure || !strings.Contains(errOut, "--output") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}
