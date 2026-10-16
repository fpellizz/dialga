package session

import (
	"errors"
	"testing"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

func TestPasswordFromEnv(t *testing.T) {
	env := map[string]string{"MY_PW": "from-var", "DIALGA_SQL_PASSWORD": "from-dialga"}
	getenv := func(k string) string { return env[k] }
	tests := []struct {
		name string
		ref  string
		want string // empty: an error is expected
	}{
		{"env var", "env:MY_PW", "from-var"},
		{"prompt uses DIALGA_SQL_PASSWORD", "prompt", "from-dialga"},
		{"empty env var", "env:OTHER_PW", ""},
		{"keyring not active yet", "keyring", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PasswordFromEnv(config.Target{SQL: config.SQL{Password: tc.ref}}, getenv)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("PasswordFromEnv(%q) = %q, %v", tc.ref, got, err)
			}
		})
	}
	delete(env, "DIALGA_SQL_PASSWORD")
	if _, err := PasswordFromEnv(config.Target{SQL: config.SQL{Password: "prompt"}}, getenv); !errors.Is(err, ErrPromptNeeded) {
		t.Fatalf("prompt without DIALGA_SQL_PASSWORD: %v", err)
	}
}

func TestPrivateTunnelTargetTakesAFreePort(t *testing.T) {
	tgt := config.Target{Region: "us-east-1", AWSProfile: "sales",
		Connect: config.Connect{Mode: config.ModeTunnel, LocalPort: 41433}}
	if got := PrivateTunnelTarget(tgt, awsx.Instance{Endpoint: "db.example.rds.amazonaws.com", Port: 1433}); got.LocalPort != 0 {
		t.Fatalf("private tunnel on port %d, want a free one (0)", got.LocalPort)
	}
}

func TestTunnelTarget(t *testing.T) {
	tgt := config.Target{Region: "us-east-1", AWSProfile: "sales",
		Connect: config.Connect{Mode: config.ModeTunnel, LocalPort: 41433, BridgeTag: "my-bridge"}}
	inst := awsx.Instance{Endpoint: "db.example.rds.amazonaws.com", Port: 1433, VPC: "vpc-a"}
	want := tunnel.Target{Region: "us-east-1", Profile: "sales", VPC: "vpc-a", Host: "db.example.rds.amazonaws.com",
		Port: 1433, LocalPort: 41433, BridgeTag: "my-bridge"}
	if got := TunnelTarget(tgt, inst, 41433); got != want {
		t.Fatalf("TunnelTarget() = %+v", got)
	}
}
