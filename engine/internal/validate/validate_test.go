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

func TestCollationName(t *testing.T) {
	for name, ok := range map[string]bool{
		"SQL_Latin1_General_CP1_CI_AS": true, "Latin1_General_100_CI_AS_SC_UTF8": true, "Japanese_XJIS_140_BIN2": true,
		"x": true, "a" + strings.Repeat("b", 127): true,
		"": false, "_Latin1": false, "1Latin": false, "Latin1 General": false, "Latin1-General": false,
		"Latin1;DROP": false, "Latin1'": false, "[Latin1]": false, "a" + strings.Repeat("b", 128): false, "Latin1_é": false,
	} {
		if err := CollationName(name); (err == nil) != ok {
			t.Errorf("CollationName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}

func TestObjectName(t *testing.T) {
	for name, ok := range map[string]bool{
		"dbo": true, "Order Details": true, "a]b": true, "o'neil": true, "a.b": true, "é": true, "表": true,
		strings.Repeat("a", 128): true, strings.Repeat("表", 128): true,
		"": false, strings.Repeat("a", 129): false, "a\nb": false, "a\x00b": false, "tab\there": false, "a\x7f": false,
		string([]byte{0xff, 0xfe}): false,
	} {
		if err := ObjectName(name); (err == nil) != ok {
			t.Errorf("ObjectName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}
