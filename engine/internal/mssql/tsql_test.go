package mssql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden files are the T-SQL of mssql-import.sh, produced by the script
// itself: MSSQL_IMPORT=<path>/mssql-import.sh testdata/gen-golden.sh testdata/golden
func TestStatementsMatchMssqlImport(t *testing.T) {
	const bucket = "golden-bucket"
	tests := []struct {
		golden string
		got    string
	}{
		{"restore.sql", RestoreDatabase("golden_db", S3ARN(bucket, "import/golden.bak"), RestoreOptions{})},
		{"restore-norecovery.sql", RestoreDatabase("golden_db", S3ARN(bucket, "import/golden_full.bak"), RestoreOptions{NoRecovery: true})},
		{"restore-differential.sql", RestoreDatabase("golden_db", S3ARN(bucket, "import/golden_diff.bak"), RestoreOptions{Differential: true})},
		{"backup.sql", BackupDatabase("golden_db", S3ARN(bucket, "export/golden_db_20261016-000000.bak"))},
		{"backup-quote.sql", BackupDatabase("o-neil_db", S3ARN(bucket, "export/it's here.bak"))},
		{"finish.sql", FinishRestore("golden_db")},
		{"drop.sql", DropDatabase("golden_db")},
		{"cancel.sql", CancelTask(42)},
	}
	for _, tc := range tests {
		t.Run(tc.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "golden", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			if got := tc.got + "\n"; got != string(want) {
				t.Fatalf("statement differs from mssql-import.sh\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestStr(t *testing.T) {
	for in, want := range map[string]string{
		"":         "N''",
		"sales":    "N'sales'",
		"o'neil":   "N'o''neil'",
		"''; drop": "N'''''; drop'",
		"città_db": "N'città_db'",
	} {
		if got := Str(in); got != want {
			t.Errorf("Str(%q) = %s, want %s", in, got, want)
		}
	}
	if got := S3ARN("b", "import/x.bak"); got != "arn:aws:s3:::b/import/x.bak" || strings.Contains(got, "//") {
		t.Fatalf("S3ARN = %s", got)
	}
}
