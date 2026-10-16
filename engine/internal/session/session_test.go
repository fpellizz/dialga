package session

import (
	"context"
	"errors"
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
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

func TestOpenRejectsTunnelMode(t *testing.T) {
	_, err := Open(context.Background(), config.Target{Connect: config.Connect{Mode: config.ModeTunnel}}, "pw")
	if !errors.Is(err, config.ErrTunnelNotYet) {
		t.Fatalf("err %v", err)
	}
}
