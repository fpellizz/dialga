package inspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
)

// TargetFor looks up the engine version and the free storage of a configured
// target. AWS only, read-only: no SQL session, so any connect mode works.
// Without CloudWatch the free storage is unknown, not an error.
func TargetFor(name string, t config.Target) func(context.Context) (Target, error) {
	return func(ctx context.Context) (Target, error) {
		cfg, _, err := awsx.Load(ctx, t.AWSProfile, t.Region, t.AccountID)
		if err != nil {
			return Target{}, err
		}
		inst, err := awsx.DescribeInstance(ctx, cfg, t.Instance)
		if err != nil {
			return Target{}, fmt.Errorf("RDS instance %s: %w", t.Instance, err)
		}
		if !strings.HasPrefix(inst.Engine, "sqlserver") {
			return Target{}, fmt.Errorf("RDS instance %s runs %s, not SQL Server", t.Instance, inst.Engine)
		}
		major, err := EngineMajor(inst.EngineVersion)
		if err != nil {
			return Target{}, err
		}
		tg := Target{Name: name, Instance: t.Instance, Engine: inst.Engine, EngineVersion: inst.EngineVersion, Major: major}
		if free, err := awsx.FreeStorageBytes(ctx, cfg, t.Instance); err == nil {
			n := int64(free)
			tg.FreeBytes = &n
		}
		return tg, nil
	}
}
