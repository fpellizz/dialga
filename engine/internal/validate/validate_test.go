package validate

import (
	"strings"
	"testing"
)

func TestDBName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "app_db", false},
		{"digits and dash", "crm-2026", false},
		{"leading underscore", "_staging", false},
		{"max length", strings.Repeat("a", 100), false},
		{"too long", strings.Repeat("a", 101), true},
		{"empty", "", true},
		{"leading dash", "-app", true},
		{"space", "app db", true},
		{"quote", "app'db", true},
		{"bracket", "app]db", true},
		{"dot", "app.db", true},
		{"system master", "master", true},
		{"system case-insensitive", "MSDB", true},
		{"rds admin db", "rdsadmin", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := DBName(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DBName(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestS3Key(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"import prefix", "import/app_db.bak", false},
		{"striped backup", "import/app_part*.bak", false},
		{"export with timestamp", "export/app_db_20261015-152922.bak", false},
		{"root object", "app_db.bak", false},
		{"empty", "", true},
		{"space", "import/app db.bak", true},
		{"single quote", "import/app'db.bak", true},
		{"leading slash", "/import/app_db.bak", true},
		{"parent dir", "import/../secret.bak", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := S3Key(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("S3Key(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestPrincipalName(t *testing.T) {
	for name, ok := range map[string]bool{
		"ana": true, "first.last": true, "svc-app_1": true, "_x": true, "9lives": true, strings.Repeat("a", 128): true,
		"": false, "-x": false, ".x": false, "a b": false, "a;b": false, "a]b": false, "a'b": false, "##MS##": false,
		strings.Repeat("a", 129): false, "é": false,
	} {
		if err := PrincipalName(name); (err == nil) != ok {
			t.Errorf("PrincipalName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}
