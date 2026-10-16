package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/discover"
)

// fakeDiscover answers config init and doctor without AWS.
type fakeDiscover struct {
	profiles  []discover.Profile
	prereqs   []discover.Prerequisite
	bucketErr error
	asked     []string // profile/region of the discoveries, bucket checks
}

func (f *fakeDiscover) Profiles() ([]discover.Profile, error) { return f.profiles, nil }

func (f *fakeDiscover) Instances(_ context.Context, profile, region string) (discover.Instances, error) {
	f.asked = append(f.asked, "instances "+profile+"/"+region)
	return discover.Instances{Profile: profile, Region: region, AccountID: "123456789012", Instances: []discover.Instance{
		{Identifier: "Dev-SQLServer", Engine: "sqlserver-se", EngineVersion: "14.00", Class: "db.m6i.large",
			Status: "available", MasterUser: "admin", OptionGroup: "native", BackupRestore: true},
		{Identifier: "legacy-sql", Engine: "sqlserver-ex", EngineVersion: "13.00", Class: "db.t3.small",
			Status: "stopped", MasterUser: "sa", OptionGroup: "default:sqlserver-ex-13-00"},
	}}, nil
}

func (f *fakeDiscover) Prerequisites(context.Context) []discover.Prerequisite { return f.prereqs }

func (f *fakeDiscover) CheckBucket(_ context.Context, profile, _, bucket string) error {
	f.asked = append(f.asked, "bucket "+profile+" "+bucket)
	return f.bucketErr
}

// firstRunApp is an app on a config file (doc "" = no file yet) whose
// discovery is faked; answers, when set, are typed on a fake terminal.
func firstRunApp(t *testing.T, doc string, answers ...string) (*app, *fakeDiscover) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dialga", "config.yaml")
	if doc != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	terminal := len(answers) > 0
	stdinIsTerminal = func(io.Reader) bool { return terminal }
	t.Cleanup(func() { stdinIsTerminal = func(io.Reader) bool { return false } })
	fake := &fakeDiscover{
		profiles: []discover.Profile{{Name: "default"}, {Name: "sales", Region: "eu-south-1"}},
		prereqs: []discover.Prerequisite{
			{ID: discover.PrereqSSMPlugin, Program: "session-manager-plugin", Found: true, Path: "/usr/bin/session-manager-plugin", Version: "1.2.707.0"},
			{ID: discover.PrereqContainerRuntime, Program: "podman", Purpose: "inspecting .bak files",
				Install: []discover.InstallHint{{Distro: "fedora", Command: "sudo dnf install -y podman"}}},
		},
	}
	a := &app{
		configPath: path,
		stdin:      strings.NewReader(strings.Join(answers, "\n") + "\n"),
		getenv:     envMap{"TEST_SQL_PASSWORD": "s3cret"}.get,
		discover:   fake,
	}
	return a, fake
}

func loadTarget(t *testing.T, a *app, name string) config.Target {
	t.Helper()
	cfg, err := config.Load(a.configPath)
	if err != nil {
		t.Fatal(err)
	}
	tgt, ok := cfg.Targets[name]
	if !ok {
		t.Fatalf("no target %s in %v", name, cfg.Names())
	}
	return tgt
}

func TestConfigInitNonInteractive(t *testing.T) {
	a, fake := firstRunApp(t, "")
	code, out, errOut := run(a, "config", "init", "--profile", "sales", "--instance", "Dev-SQLServer", "--bucket", "dev-backups")
	if code != 0 || !strings.Contains(out, "Added target dev-sqlserver in ") || !strings.Contains(out, "dialga check dev-sqlserver") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	if strings.Contains(out, ".bak") || !strings.Contains(errOut, "Account 123456789012: 2 SQL Server instance(s)") {
		t.Fatalf("out %q, stderr %q", out, errOut)
	}
	want := config.Target{
		AccountID: "123456789012", Region: "eu-south-1", AWSProfile: "sales", Instance: "Dev-SQLServer", Bucket: "dev-backups",
		Prefixes: config.Prefixes{Import: "import", Export: "export"}, MaxTasks: 2,
		SQL:     config.SQL{User: "admin", Password: "prompt"},
		Connect: config.Connect{Mode: "external", LocalPort: 11433},
	}
	if got := loadTarget(t, a, "dev-sqlserver"); got != want {
		t.Fatalf("target %+v\nwant   %+v", got, want)
	}
	if strings.Join(fake.asked, "; ") != "instances sales/eu-south-1; bucket sales dev-backups" {
		t.Fatalf("AWS calls: %v", fake.asked)
	}
	if info, _ := os.Stat(a.configPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestConfigInitNonInteractiveErrors(t *testing.T) {
	a, _ := firstRunApp(t, "")
	if code, _, errOut := run(a, "config", "init", "--instance", "x"); code != exitFailure || !strings.Contains(errOut, "--bucket") {
		t.Fatalf("no bucket: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "init", "--instance", "nope", "--bucket", "dev-backups"); code != exitFailure ||
		!strings.Contains(errOut, `instance "nope" not found (found: Dev-SQLServer, legacy-sql)`) {
		t.Fatalf("unknown instance: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "init", "--profile", "ghost", "--instance", "x", "--bucket", "dev-backups"); code != exitFailure ||
		!strings.Contains(errOut, `AWS profile "ghost" is not in the AWS shared files`) {
		t.Fatalf("unknown profile: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "init", "--instance", "legacy-sql", "--bucket", "dev-backups", "--password", "hunter2"); code != exitFailure ||
		!strings.Contains(errOut, "prompt or env:VAR") || strings.Contains(errOut, "hunter2") {
		t.Fatalf("plain password: code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(a.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed init wrote the file: %v", err)
	}
}

func TestConfigInitBucketCheck(t *testing.T) {
	a, fake := firstRunApp(t, "")
	fake.bucketErr = errors.New("bucket not found")
	args := []string{"config", "init", "--instance", "legacy-sql", "--bucket", "dev-backups"}
	code, _, errOut := run(a, args...)
	if code != exitFailure || !strings.Contains(errOut, "bucket dev-backups: bucket not found (--no-bucket-check keeps it anyway)") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, "has no SQLSERVER_BACKUP_RESTORE") || !strings.Contains(errOut, "is stopped") {
		t.Fatalf("warnings missing: %q", errOut)
	}
	if code, _, errOut := run(a, append(args, "--no-bucket-check", "--mode", "direct")...); code != 0 {
		t.Fatalf("--no-bucket-check: code %d, stderr %q", code, errOut)
	}
	if got := loadTarget(t, a, "legacy-sql"); got.SQL.User != "sa" || got.Connect != (config.Connect{Mode: "direct"}) {
		t.Fatalf("target %+v", got)
	}
}

func TestConfigInitReplaceKeepsHiddenKeys(t *testing.T) {
	doc := strings.Replace(testConfig, "dev-sql:", "legacy-sql:", 1) + "    max_tasks: 1\n"
	a, _ := firstRunApp(t, doc)
	args := []string{"config", "init", "--instance", "legacy-sql", "--bucket", "dev-backups"}
	if code, _, errOut := run(a, args...); code != exitFailure || !strings.Contains(errOut, "use --replace") {
		t.Fatalf("exists: code %d, stderr %q", code, errOut)
	}
	code, out, errOut := run(a, append(args, "--replace", "-o", "json")...)
	var doc2 savedTarget
	if code != 0 || json.Unmarshal([]byte(out), &doc2) != nil || !doc2.Replaced || doc2.Target != "legacy-sql" {
		t.Fatalf("replace: code %d, out %q, stderr %q", code, out, errOut)
	}
	if got := loadTarget(t, a, "legacy-sql"); got.MaxTasks != 1 || got.Bucket != "dev-backups" || got.AWSProfile != "default" {
		t.Fatalf("target %+v", got)
	}
	if bak, _ := os.ReadFile(a.configPath + ".bak"); string(bak) != doc {
		t.Fatalf("backup:\n%s", bak)
	}
}

func TestConfigInitInteractive(t *testing.T) {
	a, fake := firstRunApp(t, "",
		"2",           // profile: sales
		"",            // region: the profile's (eu-south-1)
		"1",           // instance: Dev-SQLServer
		"Bad_Bucket",  // invalid, asked again
		"dev-backups", // bucket
		"",            // SQL login: the master user
		"env:DEV_PW",  // password source
		"",            // connection: external
		"21433",       // local port
		"",            // name: dev-sqlserver
		"",            // protected: no
		"",            // save: yes
	)
	code, out, errOut := run(a, "config", "init")
	if code != 0 || !strings.Contains(out, "Added target dev-sqlserver") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	for _, want := range []string{"AWS profile [default]: ", "invalid bucket name", "Target dev-sqlserver\n", "Save it to "} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	got := loadTarget(t, a, "dev-sqlserver")
	if got.AWSProfile != "sales" || got.Region != "eu-south-1" || got.SQL.Password != "env:DEV_PW" ||
		got.Connect.LocalPort != 21433 || got.Protected {
		t.Fatalf("target %+v", got)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("AWS calls %v", fake.asked)
	}
}

func TestConfigInitInteractiveDeclined(t *testing.T) {
	a, _ := firstRunApp(t, "", "", "", "1", "dev-backups", "", "", "", "", "", "y", "n")
	if code, _, errOut := run(a, "config", "init"); code != exitFailure || !strings.Contains(errOut, "not saved") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(a.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was written: %v", err)
	}
}

func TestConfigAddTarget(t *testing.T) {
	a, fake := firstRunApp(t, testConfig)
	code, _, errOut := run(a, "config", "add-target", "prod-sql", "--instance", "prod-sqlserver")
	if code != exitFailure || !strings.Contains(errOut, "a new target needs --account-id, --bucket, --sql-user") {
		t.Fatalf("missing flags: code %d, stderr %q", code, errOut)
	}
	full := []string{"config", "add-target", "prod-sql", "--instance", "prod-sqlserver", "--account-id", "210987654321",
		"--bucket", "prod-backups", "--sql-user", "admin", "--profile", "sales", "--password", "env:PROD_PW", "--protected"}
	if code, out, errOut := run(a, full...); code != 0 || !strings.Contains(out, "Added target prod-sql") || !strings.Contains(out, ".bak") {
		t.Fatalf("add: code %d, out %q, stderr %q", code, out, errOut)
	}
	got := loadTarget(t, a, "prod-sql")
	if got.Region != "eu-south-1" || got.Connect.LocalPort != 11433 || !got.Protected || got.SQL.Password != "env:PROD_PW" {
		t.Fatalf("target %+v", got)
	}
	if code, _, errOut := run(a, full...); code != exitFailure || !strings.Contains(errOut, "use --replace") {
		t.Fatalf("twice: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "add-target", "prod-sql", "--replace", "--bucket", "other-backups"); code != 0 {
		t.Fatalf("replace: code %d, stderr %q", code, errOut)
	}
	changed := loadTarget(t, a, "prod-sql")
	got.Bucket = "other-backups"
	if changed != got || len(fake.asked) != 0 {
		t.Fatalf("replace changed more than the bucket: %+v (AWS calls %v)", changed, fake.asked)
	}
	if loadTarget(t, a, "dev-sql").Instance != "dev-sqlserver" {
		t.Fatal("the other target changed")
	}
}

func TestConfigRemoveTarget(t *testing.T) {
	a, _ := firstRunApp(t, testConfig)
	if code, _, errOut := run(a, "config", "remove-target", "dev-sql"); code != exitFailure || !strings.Contains(errOut, "--confirm dev-sql") {
		t.Fatalf("no confirmation: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "remove-target", "dev-sql", "--confirm", "dev"); code != exitFailure || !strings.Contains(errOut, "does not match") {
		t.Fatalf("wrong confirmation: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "config", "remove-target", "nope", "--confirm", "nope"); code != exitFailure || !strings.Contains(errOut, "unknown target") {
		t.Fatalf("unknown: code %d, stderr %q", code, errOut)
	}
	code, out, _ := run(a, "config", "remove-target", "dev-sql", "--confirm", "dev-sql")
	if code != 0 || !strings.Contains(out, "No targets left: the file was removed") {
		t.Fatalf("remove: code %d, out %q", code, out)
	}
	if bak, _ := os.ReadFile(a.configPath + ".bak"); string(bak) != testConfig {
		t.Fatalf("backup:\n%s", bak)
	}
}

func TestConfigRemoveTargetInteractive(t *testing.T) {
	doc := testConfig + strings.ReplaceAll(strings.SplitN(testConfig, "targets:\n", 2)[1], "dev-sql:", "prod-sql:")
	a, _ := firstRunApp(t, doc, "prod-sql")
	code, out, errOut := run(a, "config", "remove-target", "prod-sql")
	if code != 0 || !strings.Contains(errOut, "Type the target name to confirm") || strings.Contains(out, "No targets left") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	if cfg, err := config.Load(a.configPath); err != nil || strings.Join(cfg.Names(), ",") != "dev-sql" {
		t.Fatalf("after remove: %v", err)
	}
}

func TestDoctor(t *testing.T) {
	a, _ := firstRunApp(t, testConfig)
	code, out, _ := run(a, "doctor")
	if code != 0 || !strings.Contains(out, "Ready, with warnings.") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
	for _, want := range []string{
		"OK    session-manager-plugin     1.2.707.0 (/usr/bin/session-manager-plugin)",
		"WARN  Container runtime          not found: needed for inspecting .bak files",
		"install on Fedora:         sudo dnf install -y podman",
		"OK    AWS profiles               2: default, sales",
		"OK    Target dev-sql             profile default, eu-west-1, instance dev-sqlserver, external",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorFailures(t *testing.T) {
	a, fake := firstRunApp(t, strings.Replace(testConfig, "aws_profile: default", "aws_profile: ghost", 1))
	fake.prereqs[0] = discover.Prerequisite{ID: discover.PrereqSSMPlugin, Program: "session-manager-plugin", Purpose: "SSM tunnels"}
	a.getenv = envMap{}.get
	code, out, _ := run(a, "doctor", "-o", "json")
	var doc struct {
		OK    bool         `json:"ok"`
		Items []doctorItem `json:"items"`
	}
	if code != exitCheckFailed || json.Unmarshal([]byte(out), &doc) != nil || doc.OK {
		t.Fatalf("code %d, out %s", code, out)
	}
	status := map[string]string{}
	for _, it := range doc.Items {
		status[it.ID] = string(it.Status)
	}
	want := map[string]string{"session-manager-plugin": "failed", "container-runtime": "warning",
		"aws-profiles": "ok", "config": "ok", "target:dev-sql": "failed"}
	for id, s := range want {
		if status[id] != s {
			t.Errorf("%s: %s, want %s (%s)", id, status[id], s, out)
		}
	}

	none, _ := firstRunApp(t, "")
	if code, out, _ := run(none, "doctor"); code != exitCheckFailed || !strings.Contains(out, "add a target with dialga config init") {
		t.Fatalf("no config: code %d, out %s", code, out)
	}
}

func TestDoctorPasswordWarning(t *testing.T) {
	a, _ := firstRunApp(t, testConfig)
	a.getenv = envMap{}.get
	code, out, _ := run(a, "doctor")
	if code != 0 || !strings.Contains(out, "SQL password: environment variable TEST_SQL_PASSWORD is empty") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
}

func TestSuggestName(t *testing.T) {
	for in, want := range map[string]string{
		"Dev-SQLServer":          "dev-sqlserver",
		"mssql_dev.01":           "mssql-dev-01",
		"--x--":                  "x",
		strings.Repeat("a", 70):  strings.Repeat("a", 63),
		strings.Repeat("a-", 40): strings.TrimRight(strings.Repeat("a-", 32)[:63], "-"),
	} {
		if got := suggestName(in); got != want || !config.ValidTargetName(got) {
			t.Errorf("suggestName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDoctorOutdatedPlugin(t *testing.T) {
	a, fake := firstRunApp(t, testConfig)
	fake.prereqs[0].Version, fake.prereqs[0].Outdated = "1.2.400.0", true
	code, out, _ := run(a, "doctor")
	if code != 0 || !strings.Contains(out, "WARN  session-manager-plugin     1.2.400.0 (/usr/bin/session-manager-plugin): too old") {
		t.Fatalf("external target: code %d, out:\n%s", code, out)
	}
	tunnel, fake := firstRunApp(t, strings.Replace(testConfig, "mode: external", "mode: tunnel", 1))
	fake.prereqs[0].Version, fake.prereqs[0].Outdated = "1.2.400.0", true
	if code, out, _ := run(tunnel, "doctor"); code != exitCheckFailed || !strings.Contains(out, "FAIL  session-manager-plugin") {
		t.Fatalf("tunnel target: code %d, out:\n%s", code, out)
	}
}
