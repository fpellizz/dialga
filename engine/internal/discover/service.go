package discover

import (
	"context"
	"os"

	"github.com/decisyon/dialga/engine/internal/awsx"
)

// Service is what the CLI and the API ask about this computer and AWS.
// System is the real one; tests use fakes.
type Service interface {
	// Profiles lists the AWS profiles of the shared files.
	Profiles() ([]Profile, error)
	// Instances lists the SQL Server instances of a profile in a region.
	Instances(ctx context.Context, profile, region string) (Instances, error)
	// Prerequisites reports the programs Dialga runs.
	Prerequisites(ctx context.Context) []Prerequisite
	// CheckBucket runs HeadBucket on bucket with the profile (read-only).
	CheckBucket(ctx context.Context, profile, region, bucket string) error
}

// System asks this computer and AWS.
type System struct {
	// Getenv finds the shared files (default os.Getenv).
	Getenv func(string) string
}

func (s System) getenv(key string) string {
	if s.Getenv == nil {
		return os.Getenv(key)
	}
	return s.Getenv(key)
}

// Profiles implements Service.
func (s System) Profiles() ([]Profile, error) {
	return Profiles(ProfileFiles(s.getenv))
}

// Instances implements Service.
func (System) Instances(ctx context.Context, profile, region string) (Instances, error) {
	return Discover(ctx, profile, region)
}

// Prerequisites implements Service.
func (s System) Prerequisites(ctx context.Context) []Prerequisite {
	p := LocalPrograms()
	p.Getenv = s.getenv
	return p.Prerequisites(ctx)
}

// CheckBucket implements Service.
func (System) CheckBucket(ctx context.Context, profile, region, bucket string) error {
	cfg, _, err := awsx.Load(ctx, profile, region, "")
	if err != nil {
		return err
	}
	return awsx.HeadBucket(ctx, cfg, bucket)
}
