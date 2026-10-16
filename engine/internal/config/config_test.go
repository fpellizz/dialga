package config

import (
	"strings"
	"testing"
)

const validDoc = `apiVersion: v1
targets:
  dev-sql:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: default
    instance: dev-sqlserver
    bucket: dev-sqlserver-backups
    sql: { user: admin }
    connect: { mode: external, local_port: 11433 }
`

func TestParseValidAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	name, tgt, err := cfg.Target("")
	if err != nil || name != "dev-sql" {
		t.Fatalf("Target(\"\") = %q, %v", name, err)
	}
	if tgt.Prefixes.Import != "import" || tgt.Prefixes.Export != "export" {
		t.Errorf("prefix defaults: %+v", tgt.Prefixes)
	}
	if tgt.MaxTasks != 2 || tgt.SQL.Password != PasswordPrompt {
		t.Errorf("defaults: max_tasks %d, password %q", tgt.MaxTasks, tgt.SQL.Password)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"wrong apiVersion", strings.Replace(validDoc, "v1", "v2", 1), "apiVersion"},
		{"no targets", "apiVersion: v1\ntargets: {}\n", "no targets"},
		{"unknown key", strings.Replace(validDoc, "region:", "regoin:", 1), "invalid YAML"},
		{"missing account", strings.Replace(validDoc, `account_id: "123456789012"`, `account_id: ""`, 1), "account_id"},
		{"bad mode", strings.Replace(validDoc, "mode: external", "mode: vpn", 1), "connect.mode"},
		{"external without port", strings.Replace(validDoc, ", local_port: 11433", "", 1), "local_port"},
		{"bad bridge tag", strings.Replace(validDoc, "mode: external, local_port: 11433", `mode: tunnel, bridge_tag: "a,b"`, 1), "connect.bridge_tag"},
		{"tunnel bad port", strings.Replace(validDoc, "mode: external, local_port: 11433", "mode: tunnel, local_port: -1", 1), "connect.local_port"},
		{"bad password source", strings.Replace(validDoc, "user: admin", "user: admin, password: hunter2", 1), "sql.password"},
		{"upper case name", strings.Replace(validDoc, "dev-sql:", "Dev:", 1), "name must be"},
		{"bad bucket", strings.Replace(validDoc, "dev-sqlserver-backups", "Bad_Bucket", 1), "bucket"},
		{"too many tasks", strings.Replace(validDoc, "sql: { user: admin }", "max_tasks: 3\n    sql: { user: admin }", 1), "max_tasks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseTunnelMode(t *testing.T) {
	for _, connect := range []string{"mode: tunnel", "mode: tunnel, local_port: 41433, bridge_tag: my-bridge"} {
		doc := strings.Replace(validDoc, "mode: external, local_port: 11433", connect, 1)
		if _, err := Parse([]byte(doc)); err != nil {
			t.Fatalf("connect { %s }: %v", connect, err)
		}
	}
}

func TestPasswordSource(t *testing.T) {
	tests := []struct {
		in       string
		wantKind string
		wantRef  string
		wantErr  bool
	}{
		{"prompt", PasswordPrompt, "", false},
		{"keyring", PasswordKeyring, "", false},
		{"env:DIALGA_SQL_PASSWORD", PasswordEnv, "DIALGA_SQL_PASSWORD", false},
		{"secretsmanager:arn:aws:secretsmanager:eu-west-1:123456789012:secret:rds/dev-AbC123", PasswordSecretsManager,
			"arn:aws:secretsmanager:eu-west-1:123456789012:secret:rds/dev-AbC123", false},
		{"env:1BAD", "", "", true},
		{"env:", "", "", true},
		{"prompt:x", "", "", true},
		{"secretsmanager:not-an-arn", "", "", true},
		{"plaintext-password", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := SQL{Password: tc.in}.PasswordSource()
			if (err != nil) != tc.wantErr || got.Kind != tc.wantKind || got.Ref != tc.wantRef {
				t.Fatalf("PasswordSource(%q) = %+v, %v", tc.in, got, err)
			}
		})
	}
}

func TestTargetSelection(t *testing.T) {
	doc := validDoc + strings.ReplaceAll(strings.SplitN(validDoc, "targets:\n", 2)[1], "dev-sql:", "prod-sql:")
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Names(), ","); got != "dev-sql,prod-sql" {
		t.Fatalf("Names() = %s", got)
	}
	if _, _, err := cfg.Target(""); err == nil || !strings.Contains(err.Error(), "choose a target") {
		t.Fatalf("Target(\"\") with two targets: %v", err)
	}
	if _, _, err := cfg.Target("nope"); err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("Target(nope): %v", err)
	}
	if name, _, err := cfg.Target("prod-sql"); err != nil || name != "prod-sql" {
		t.Fatalf("Target(prod-sql) = %q, %v", name, err)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte(validDoc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse of Marshal output: %v\n%s", err, out)
	}
	if again.Targets["dev-sql"] != cfg.Targets["dev-sql"] {
		t.Fatalf("round trip changed the target:\n%+v\n%+v", again.Targets["dev-sql"], cfg.Targets["dev-sql"])
	}
}
