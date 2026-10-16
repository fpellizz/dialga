package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func instance(id, vpc string, state ec2types.InstanceStateName) ec2types.Instance {
	return ec2types.Instance{InstanceId: aws.String(id), VpcId: aws.String(vpc), State: &ec2types.InstanceState{Name: state}}
}

func TestSelectBridge(t *testing.T) {
	const vpc = "vpc-a"
	running, stopped := ec2types.InstanceStateNameRunning, ec2types.InstanceStateNameStopped
	tests := []struct {
		name      string
		instances []ec2types.Instance
		want      string // empty: an error is expected
		wantErr   string
	}{
		{"one running", []ec2types.Instance{instance("i-1", vpc, running)}, "i-1", ""},
		{"running here, another VPC too", []ec2types.Instance{instance("i-2", "vpc-b", running), instance("i-1", vpc, running)}, "i-1", ""},
		{"terminated ignored", []ec2types.Instance{instance("i-0", vpc, ec2types.InstanceStateNameTerminated), instance("i-1", vpc, running)}, "i-1", ""},
		{"several running", []ec2types.Instance{instance("i-1", vpc, running), instance("i-2", vpc, running)}, "", "2 running instances tagged Name=ssm-bridge in vpc-a (i-1, i-2)"},
		{"stopped", []ec2types.Instance{instance("i-1", vpc, stopped)}, "", "not running (i-1 is stopped): start it"},
		{"pending", []ec2types.Instance{instance("i-1", vpc, ec2types.InstanceStateNamePending)}, "", "i-1 is pending"},
		{"other VPC only", []ec2types.Instance{instance("i-2", "vpc-b", running), instance("i-3", "vpc-b", running)}, "", "the VPC of the RDS instance (only in vpc-b)"},
		{"none", nil, "", "no instance tagged Name=ssm-bridge: create the bridge"},
		{"only terminated", []ec2types.Instance{instance("i-0", vpc, ec2types.InstanceStateNameShuttingDown)}, "", "no instance tagged"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectBridge(tc.instances, DefaultBridgeTag, vpc)
			if got != tc.want || (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("selectBridge() = %q, %v; want %q, error with %q", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// fakeEC2 answers DescribeInstances with fixed instances and records the filters.
type fakeEC2 struct {
	instances []ec2types.Instance
	err       error
	filters   []ec2types.Filter
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.filters = in.Filters
	if f.err != nil {
		return nil, f.err
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: f.instances}}}, nil
}

func TestFindBridge(t *testing.T) {
	f := &fakeEC2{instances: []ec2types.Instance{instance("i-1", "vpc-a", ec2types.InstanceStateNameRunning)}}
	got, err := FindBridge(context.Background(), f, "my-bridge", "vpc-a")
	if err != nil || got != "i-1" {
		t.Fatalf("FindBridge() = %q, %v", got, err)
	}
	if len(f.filters) != 1 || aws.ToString(f.filters[0].Name) != "tag:Name" || f.filters[0].Values[0] != "my-bridge" {
		t.Fatalf("filters %+v", f.filters)
	}
	f.err = errors.New("UnauthorizedOperation")
	if _, err := FindBridge(context.Background(), f, "my-bridge", "vpc-a"); err == nil || !strings.Contains(err.Error(), "UnauthorizedOperation") {
		t.Fatalf("EC2 error: %v", err)
	}
}
