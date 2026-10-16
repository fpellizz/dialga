// Package awsx holds the AWS calls the engine shares: credentials with the
// account guard, and the RDS instance lookup.
package awsx

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// WrongAccountError means the profile points to another account than the
// target's: every command stops before touching anything.
type WrongAccountError struct {
	Profile, Got, Want string
}

func (e *WrongAccountError) Error() string {
	return fmt.Sprintf("profile %s points to account %s, expected %s", e.Profile, e.Got, e.Want)
}

// Load returns the AWS configuration of profile and region and the account
// it points to. With expectedAccount set, any other account is a
// *WrongAccountError.
func Load(ctx context.Context, profile, region, expectedAccount string) (aws.Config, string, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(profile), config.WithRegion(region))
	if err != nil {
		return aws.Config{}, "", err
	}
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, "", err
	}
	account := aws.ToString(ident.Account)
	if expectedAccount != "" && account != expectedAccount {
		return aws.Config{}, account, &WrongAccountError{Profile: profile, Got: account, Want: expectedAccount}
	}
	return cfg, account, nil
}

// Instance is what the engine needs to know about an RDS DB instance.
type Instance struct {
	Endpoint      string
	Port          int
	Status        string
	Engine        string
	EngineVersion string
	// OptionGroupStatus is the status of the first option group ("in-sync").
	OptionGroupStatus string
}

// DescribeInstance looks up an RDS DB instance by identifier.
func DescribeInstance(ctx context.Context, cfg aws.Config, id string) (Instance, error) {
	out, err := rds.NewFromConfig(cfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	})
	if err != nil {
		return Instance{}, err
	}
	if len(out.DBInstances) == 0 || out.DBInstances[0].Endpoint == nil {
		return Instance{}, errors.New("instance not found")
	}
	db := out.DBInstances[0]
	inst := Instance{
		Endpoint:      aws.ToString(db.Endpoint.Address),
		Port:          int(aws.ToInt32(db.Endpoint.Port)),
		Status:        aws.ToString(db.DBInstanceStatus),
		Engine:        aws.ToString(db.Engine),
		EngineVersion: aws.ToString(db.EngineVersion),
	}
	if inst.Port == 0 {
		inst.Port = 1433
	}
	if len(db.OptionGroupMemberships) > 0 {
		inst.OptionGroupStatus = aws.ToString(db.OptionGroupMemberships[0].Status)
	}
	return inst, nil
}
