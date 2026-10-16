package tunnel

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// FindBridge returns the running instance tagged Name=tag in vpc. It is
// looked up again on every connection, so a recreated bridge is picked up.
func FindBridge(ctx context.Context, client EC2API, tag, vpc string) (string, error) {
	p := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:Name"), Values: []string{tag}}},
	})
	var instances []ec2types.Instance
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("ec2 DescribeInstances (bridge Name=%s): %w", tag, err)
		}
		for _, r := range page.Reservations {
			instances = append(instances, r.Instances...)
		}
	}
	return selectBridge(instances, tag, vpc)
}

// selectBridge picks the bridge among the instances that carry its tag:
// exactly one running in the VPC, otherwise an error that says why.
func selectBridge(instances []ec2types.Instance, tag, vpc string) (string, error) {
	var running, notRunning, otherVPCs []string
	for _, i := range instances {
		id := aws.ToString(i.InstanceId)
		var state ec2types.InstanceStateName
		if i.State != nil {
			state = i.State.Name
		}
		switch {
		case state == ec2types.InstanceStateNameTerminated || state == ec2types.InstanceStateNameShuttingDown:
			// Gone or going: terminated instances stay listed for a while.
		case aws.ToString(i.VpcId) != vpc:
			if v := aws.ToString(i.VpcId); !slices.Contains(otherVPCs, v) {
				otherVPCs = append(otherVPCs, v)
			}
		case state == ec2types.InstanceStateNameRunning:
			running = append(running, id)
		default:
			notRunning = append(notRunning, fmt.Sprintf("%s is %s", id, state))
		}
	}
	switch {
	case len(running) == 1:
		return running[0], nil
	case len(running) > 1:
		return "", fmt.Errorf("%d running instances tagged Name=%s in %s (%s): keep one, or set connect.bridge_tag",
			len(running), tag, vpc, strings.Join(running, ", "))
	case len(notRunning) > 0:
		return "", fmt.Errorf("the bridge tagged Name=%s in %s is not running (%s): start it",
			tag, vpc, strings.Join(notRunning, ", "))
	case len(otherVPCs) > 0:
		return "", fmt.Errorf("no instance tagged Name=%s in %s, the VPC of the RDS instance (only in %s)",
			tag, vpc, strings.Join(otherVPCs, ", "))
	default:
		return "", fmt.Errorf("no instance tagged Name=%s: create the bridge, or set connect.bridge_tag", tag)
	}
}
