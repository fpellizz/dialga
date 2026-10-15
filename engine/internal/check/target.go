package check

import (
	"errors"
	"fmt"

	"github.com/decisyon/dialga/engine/internal/config"
)

// ErrPromptNeeded means sql.password is "prompt" and DIALGA_SQL_PASSWORD is
// empty: only an interactive caller (the CLI on a terminal) can ask for it.
var ErrPromptNeeded = errors.New(
	"sql.password is prompt: set DIALGA_SQL_PASSWORD, or use env:VAR in the configuration")

// RequestFor maps a configured target onto a check request.
func RequestFor(t config.Target) (Request, error) {
	if t.Connect.Mode == config.ModeTunnel {
		return Request{}, errors.New("connect.mode tunnel arrives in v0.4: open the tunnel yourself and use mode external")
	}
	req := Request{
		Profile:         t.AWSProfile,
		Region:          t.Region,
		Instance:        t.Instance,
		LocalPort:       t.Connect.LocalPort,
		SQLUser:         t.SQL.User,
		ExpectedAccount: t.AccountID,
		Direct:          t.Connect.Mode == config.ModeDirect,
	}
	return req, req.Validate()
}

// PasswordFromEnv resolves sql.password without a terminal: env:VAR reads
// VAR, prompt reads DIALGA_SQL_PASSWORD (ErrPromptNeeded when it is empty).
func PasswordFromEnv(t config.Target, getenv func(string) string) (string, error) {
	src, err := t.SQL.PasswordSource()
	if err != nil {
		return "", err
	}
	switch src.Kind {
	case config.PasswordEnv:
		if v := getenv(src.Ref); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", src.Ref)
	case config.PasswordPrompt:
		if v := getenv("DIALGA_SQL_PASSWORD"); v != "" {
			return v, nil
		}
		return "", ErrPromptNeeded
	default:
		return "", fmt.Errorf("password source %q is not supported yet", src.Kind)
	}
}
