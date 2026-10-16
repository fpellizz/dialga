// Package awsx holds the AWS calls the engine shares: credentials with the
// account guard, and the RDS instance lookup.
package awsx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	// OptionGroup is the first option group and its status ("in-sync").
	OptionGroup       string
	OptionGroupStatus string
	// AllocatedGiB and MaxAllocatedGiB (0: no storage autoscaling).
	AllocatedGiB    int
	MaxAllocatedGiB int
	// BackupWindow is the daily automated backup window, UTC (04:29-04:59).
	BackupWindow string
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
		Endpoint:        aws.ToString(db.Endpoint.Address),
		Port:            int(aws.ToInt32(db.Endpoint.Port)),
		Status:          aws.ToString(db.DBInstanceStatus),
		Engine:          aws.ToString(db.Engine),
		EngineVersion:   aws.ToString(db.EngineVersion),
		AllocatedGiB:    int(aws.ToInt32(db.AllocatedStorage)),
		MaxAllocatedGiB: int(aws.ToInt32(db.MaxAllocatedStorage)),
		BackupWindow:    aws.ToString(db.PreferredBackupWindow),
	}
	if inst.Port == 0 {
		inst.Port = 1433
	}
	if len(db.OptionGroupMemberships) > 0 {
		inst.OptionGroup = aws.ToString(db.OptionGroupMemberships[0].OptionGroupName)
		inst.OptionGroupStatus = aws.ToString(db.OptionGroupMemberships[0].Status)
	}
	return inst, nil
}

// NativeBackupOption is the option group option that native backup and
// restore through S3 need.
const NativeBackupOption = "SQLSERVER_BACKUP_RESTORE"

// HasOption reports whether the option group contains the option.
func HasOption(ctx context.Context, cfg aws.Config, optionGroup, option string) (bool, error) {
	out, err := rds.NewFromConfig(cfg).DescribeOptionGroups(ctx, &rds.DescribeOptionGroupsInput{
		OptionGroupName: aws.String(optionGroup),
	})
	if err != nil {
		return false, err
	}
	for _, og := range out.OptionGroupsList {
		for _, o := range og.Options {
			if aws.ToString(o.OptionName) == option {
				return true, nil
			}
		}
	}
	return false, nil
}

// HeadBucket checks that the bucket exists and the profile can reach it.
func HeadBucket(ctx context.Context, cfg aws.Config, bucket string) error {
	_, err := s3.NewFromConfig(cfg).HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	return bucketError(err)
}

// bucketError turns the common HeadBucket answers into a readable reason.
func bucketError(err error) error {
	var re *awshttp.ResponseError
	if err == nil || !errors.As(err, &re) {
		return err
	}
	switch re.HTTPStatusCode() {
	case 301:
		return errors.New("the bucket is in another region")
	case 403:
		return errors.New("access denied: the profile cannot read the bucket")
	case 404:
		return errors.New("bucket not found")
	}
	return err
}

// ErrNoDatapoint means CloudWatch returned no recent value.
var ErrNoDatapoint = errors.New("no recent datapoint")

// FreeStorageBytes is the lowest FreeStorageSpace of the instance in the
// last 20 minutes, from CloudWatch.
func FreeStorageBytes(ctx context.Context, cfg aws.Config, instance string) (float64, error) {
	now := time.Now().UTC()
	out, err := cloudwatch.NewFromConfig(cfg).GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/RDS"),
		MetricName: aws.String("FreeStorageSpace"),
		Dimensions: []cwtypes.Dimension{{Name: aws.String("DBInstanceIdentifier"), Value: aws.String(instance)}},
		StartTime:  aws.Time(now.Add(-20 * time.Minute)),
		EndTime:    aws.Time(now),
		Period:     aws.Int32(300),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticMinimum},
	})
	if err != nil {
		return 0, err
	}
	return latestMinimum(out.Datapoints)
}

func latestMinimum(points []cwtypes.Datapoint) (float64, error) {
	var latest *cwtypes.Datapoint
	for i := range points {
		p := &points[i]
		if p.Timestamp != nil && p.Minimum != nil && (latest == nil || p.Timestamp.After(*latest.Timestamp)) {
			latest = p
		}
	}
	if latest == nil {
		return 0, ErrNoDatapoint
	}
	return *latest.Minimum, nil
}
