package session

import (
	"context"
	"fmt"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// OpenDatabases opens a session on t for reading its databases; close ends it.
func OpenDatabases(ctx context.Context, t config.Target, password string) (mssql.DatabaseReader, func(), error) {
	return Sessions{}.OpenDatabases(ctx, t, password)
}

// OpenDatabases is the package OpenDatabases, with the shared tunnel when there is one.
func (s Sessions) OpenDatabases(ctx context.Context, t config.Target, password string) (mssql.DatabaseReader, func(), error) {
	db, err := s.Open(ctx, t, password)
	if err != nil {
		return nil, nil, err
	}
	return mssql.Catalog{DB: db}, func() { _ = db.Close() }, nil
}

// InstanceInfo is the AWS side of a target: its RDS instance and free storage.
type InstanceInfo struct {
	Instance awsx.Instance
	// FreeBytes is the free storage from CloudWatch when FreeErr is nil.
	FreeBytes float64
	FreeErr   error
}

// DescribeTarget reads the RDS instance of t, with the account guard, and
// its free storage. AWS reads only: no SQL password needed.
func DescribeTarget(ctx context.Context, t config.Target) (InstanceInfo, error) {
	cfg, _, err := awsx.Load(ctx, t.AWSProfile, t.Region, t.AccountID)
	if err != nil {
		return InstanceInfo{}, err
	}
	inst, err := awsx.DescribeInstance(ctx, cfg, t.Instance)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("RDS instance %s: %w", t.Instance, err)
	}
	info := InstanceInfo{Instance: inst}
	info.FreeBytes, info.FreeErr = awsx.FreeStorageBytes(ctx, cfg, t.Instance)
	return info, nil
}
