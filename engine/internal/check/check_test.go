package check

import "testing"

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
