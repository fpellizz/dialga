package discover

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/decisyon/dialga/engine/internal/awsx"
)

// Instance is an RDS SQL Server instance a profile can see.
type Instance struct {
	Identifier    string `json:"identifier"`
	Endpoint      string `json:"endpoint,omitempty"`
	Port          int    `json:"port,omitempty"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion"`
	Class         string `json:"class"`
	Status        string `json:"status"`
	// MasterUser is the default SQL login of a new target.
	MasterUser  string `json:"masterUser,omitempty"`
	OptionGroup string `json:"optionGroup,omitempty"`
	// BackupRestore says whether the option group has SQLSERVER_BACKUP_RESTORE,
	// without which native backup and restore through S3 do not work.
	BackupRestore bool `json:"backupRestore"`
}

// Instances is what a profile sees in a region.
type Instances struct {
	Profile string `json:"profile"`
	Region  string `json:"region"`
	// AccountID is the account the profile points to: the account guard of
	// a new target.
	AccountID string     `json:"accountId"`
	Instances []Instance `json:"instances"`
}

// RDSAPI is the part of the RDS client the discovery calls (tests fake it).
type RDSAPI interface {
	rds.DescribeDBInstancesAPIClient
	rds.DescribeOptionGroupsAPIClient
}

// Discover lists the SQL Server instances of profile in region, with the
// account the profile points to.
func Discover(ctx context.Context, profile, region string) (Instances, error) {
	cfg, account, err := awsx.Load(ctx, profile, region, "")
	if err != nil {
		return Instances{}, err
	}
	list, err := SQLServerInstances(ctx, rds.NewFromConfig(cfg))
	if err != nil {
		return Instances{}, err
	}
	return Instances{Profile: profile, Region: region, AccountID: account, Instances: list}, nil
}

// SQLServerInstances lists the RDS instances whose engine is SQL Server
// (sqlserver-*; RDS Custom is left out), sorted by identifier, and looks up
// once per option group whether it has the native backup option.
func SQLServerInstances(ctx context.Context, client RDSAPI) ([]Instance, error) {
	list := []Instance{}
	pages := rds.NewDescribeDBInstancesPaginator(client, &rds.DescribeDBInstancesInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, db := range page.DBInstances {
			engine := aws.ToString(db.Engine)
			if !strings.HasPrefix(engine, "sqlserver-") {
				continue
			}
			inst := Instance{
				Identifier:    aws.ToString(db.DBInstanceIdentifier),
				Engine:        engine,
				EngineVersion: aws.ToString(db.EngineVersion),
				Class:         aws.ToString(db.DBInstanceClass),
				Status:        aws.ToString(db.DBInstanceStatus),
				MasterUser:    aws.ToString(db.MasterUsername),
			}
			if db.Endpoint != nil {
				inst.Endpoint = aws.ToString(db.Endpoint.Address)
				inst.Port = int(aws.ToInt32(db.Endpoint.Port))
			}
			if len(db.OptionGroupMemberships) > 0 {
				inst.OptionGroup = aws.ToString(db.OptionGroupMemberships[0].OptionGroupName)
			}
			list = append(list, inst)
		}
	}
	slices.SortFunc(list, func(a, b Instance) int { return strings.Compare(a.Identifier, b.Identifier) })

	hasOption := map[string]bool{}
	for i, inst := range list {
		if inst.OptionGroup == "" {
			continue
		}
		has, seen := hasOption[inst.OptionGroup]
		if !seen {
			var err error
			if has, err = optionGroupHas(ctx, client, inst.OptionGroup, awsx.NativeBackupOption); err != nil {
				return nil, fmt.Errorf("option group %s: %w", inst.OptionGroup, err)
			}
			hasOption[inst.OptionGroup] = has
		}
		list[i].BackupRestore = has
	}
	return list, nil
}

func optionGroupHas(ctx context.Context, client rds.DescribeOptionGroupsAPIClient, group, option string) (bool, error) {
	pages := rds.NewDescribeOptionGroupsPaginator(client, &rds.DescribeOptionGroupsInput{
		OptionGroupName: aws.String(group),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return false, err
		}
		for _, og := range page.OptionGroupsList {
			for _, o := range og.Options {
				if aws.ToString(o.OptionName) == option {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
