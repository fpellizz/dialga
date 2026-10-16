package check

import (
	"testing"

	"github.com/decisyon/dialga/engine/internal/config"
)

func TestRequestValidate(t *testing.T) {
	valid := Request{Profile: "sales", Region: "us-east-1", Instance: "mssql-dev", LocalPort: 11433, SQLUser: "god"}
	tests := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{"valid", func(*Request) {}, false},
		{"region eu-south-1", func(r *Request) { r.Region = "eu-south-1" }, false},
		{"region gov", func(r *Request) { r.Region = "us-gov-west-1" }, false},
		{"empty profile", func(r *Request) { r.Profile = "" }, true},
		{"profile with space", func(r *Request) { r.Profile = "my profile" }, true},
		{"bad region", func(r *Request) { r.Region = "us_east_1" }, true},
		{"instance starting with digit", func(r *Request) { r.Instance = "1db" }, true},
		{"instance with quote", func(r *Request) { r.Instance = "db'x" }, true},
		{"port zero", func(r *Request) { r.LocalPort = 0 }, true},
		{"port too high", func(r *Request) { r.LocalPort = 70000 }, true},
		{"login with semicolon", func(r *Request) { r.SQLUser = "god;x" }, true},
		{"expected account", func(r *Request) { r.ExpectedAccount = "123456789012" }, false},
		{"expected account too short", func(r *Request) { r.ExpectedAccount = "12345" }, true},
		{"direct needs no local port", func(r *Request) { r.Direct = true; r.LocalPort = 0 }, false},
		{"tunnel on a free port", func(r *Request) { r.Tunnel = true; r.LocalPort = 0 }, false},
		{"tunnel with bridge tag", func(r *Request) { r.Tunnel = true; r.BridgeTag = "ssm-bridge" }, false},
		{"tunnel negative port", func(r *Request) { r.Tunnel = true; r.LocalPort = -1 }, true},
		{"tunnel and direct", func(r *Request) { r.Tunnel = true; r.Direct = true }, true},
		{"bridge tag with comma", func(r *Request) { r.Tunnel = true; r.BridgeTag = "a,b" }, true},
		{"bucket", func(r *Request) { r.Bucket = "mssql-dev-backups-123456789012" }, false},
		{"bucket with dots", func(r *Request) { r.Bucket = "my.backups.bucket" }, false},
		{"bucket upper case", func(r *Request) { r.Bucket = "My-Bucket" }, true},
		{"bucket with slash", func(r *Request) { r.Bucket = "bucket/key" }, true},
		{"bucket too short", func(r *Request) { r.Bucket = "ab" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.mutate(&r)
			if err := r.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestRequestFor(t *testing.T) {
	base := config.Target{AccountID: "123456789012", Region: "us-east-1", AWSProfile: "sales", Instance: "mssql-dev",
		Bucket: "mssql-dev-backups", SQL: config.SQL{User: "god"}}
	tests := []struct {
		name    string
		connect config.Connect
		want    func(Request) bool
	}{
		{"external", config.Connect{Mode: config.ModeExternal, LocalPort: 11433},
			func(r Request) bool { return r.LocalPort == 11433 && !r.Direct && !r.Tunnel }},
		{"direct", config.Connect{Mode: config.ModeDirect}, func(r Request) bool { return r.Direct && !r.Tunnel }},
		{"tunnel", config.Connect{Mode: config.ModeTunnel, BridgeTag: "my-bridge"},
			func(r Request) bool { return r.Tunnel && !r.Direct && r.LocalPort == 0 && r.BridgeTag == "my-bridge" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tgt := base
			tgt.Connect = tc.connect
			req, err := RequestFor(tgt)
			if err != nil || !tc.want(req) || req.ExpectedAccount != "123456789012" || req.Bucket != "mssql-dev-backups" {
				t.Fatalf("RequestFor() = %+v, %v", req, err)
			}
		})
	}
}
