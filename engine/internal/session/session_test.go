package session

import (
	"testing"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

func TestPrivateTunnelTargetTakesAFreePort(t *testing.T) {
	tgt := config.Target{Region: "us-east-1", AWSProfile: "sales",
		Connect: config.Connect{Mode: config.ModeTunnel, LocalPort: 41433}}
	if got := PrivateTunnelTarget(tgt, awsx.Instance{Endpoint: "db.example.rds.amazonaws.com", Port: 1433}); got.LocalPort != 0 {
		t.Fatalf("private tunnel on port %d, want a free one (0)", got.LocalPort)
	}
}

func TestTunnelTarget(t *testing.T) {
	tgt := config.Target{Region: "us-east-1", AWSProfile: "sales",
		Connect: config.Connect{Mode: config.ModeTunnel, LocalPort: 41433, BridgeTag: "my-bridge"}}
	inst := awsx.Instance{Endpoint: "db.example.rds.amazonaws.com", Port: 1433, VPC: "vpc-a"}
	want := tunnel.Target{Region: "us-east-1", Profile: "sales", VPC: "vpc-a", Host: "db.example.rds.amazonaws.com",
		Port: 1433, LocalPort: 41433, BridgeTag: "my-bridge"}
	if got := TunnelTarget(tgt, inst, 41433); got != want {
		t.Fatalf("TunnelTarget() = %+v", got)
	}
}
