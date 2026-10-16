// Package session opens a SQL Server session on a configured target: AWS
// credentials with the account guard, the RDS endpoint, then a TDS
// connection with the certificate verified (through the tunnel or direct).
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// ErrPromptNeeded means sql.password is "prompt" and DIALGA_SQL_PASSWORD is
// empty: only an interactive caller (the CLI on a terminal) can ask for it.
var ErrPromptNeeded = errors.New(
	"sql.password is prompt: set DIALGA_SQL_PASSWORD, or use env:VAR in the configuration")

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

// Open connects to the SQL Server of t and checks the login. The caller
// closes the returned pool.
func Open(ctx context.Context, t config.Target, password string) (*sql.DB, error) {
	db, _, err := OpenWithAWS(ctx, t, password)
	return db, err
}

// OpenWithAWS is Open that also returns the AWS configuration it checked,
// for the callers that reach S3 too. The caller closes the returned pool.
func OpenWithAWS(ctx context.Context, t config.Target, password string) (*sql.DB, aws.Config, error) {
	if t.Connect.Mode == config.ModeTunnel {
		return nil, aws.Config{}, config.ErrTunnelNotYet
	}
	cfg, _, err := awsx.Load(ctx, t.AWSProfile, t.Region, t.AccountID)
	if err != nil {
		return nil, aws.Config{}, err
	}
	inst, err := awsx.DescribeInstance(ctx, cfg, t.Instance)
	if err != nil {
		return nil, aws.Config{}, fmt.Errorf("RDS instance %s: %w", t.Instance, err)
	}
	addr := mssql.Addr(t.Connect.Mode == config.ModeDirect, t.Connect.LocalPort, inst.Endpoint, inst.Port)
	db, err := mssql.Open(addr, inst.Endpoint, t.SQL.User, password)
	if err != nil {
		return nil, aws.Config{}, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, aws.Config{}, fmt.Errorf("SQL Server on %s: %w", addr, err)
	}
	return db, cfg, nil
}

// OpenTasks opens a session on t for reading its native tasks; close ends it.
func OpenTasks(ctx context.Context, t config.Target, password string) (mssql.TaskReader, func(), error) {
	db, err := Open(ctx, t, password)
	if err != nil {
		return nil, nil, err
	}
	return mssql.Tasks{DB: db}, func() { _ = db.Close() }, nil
}
