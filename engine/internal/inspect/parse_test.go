package inspect

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// fixture loads the rows a real SQL Server 2022 returned (testdata, written
// by the integration test with -update).
func fixture(t *testing.T, name string) []row {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var rows []row
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rows
}

func TestParseHeaderFixtures(t *testing.T) {
	tests := []struct {
		file string
		want Header
	}{
		{"headeronly-2017.json", Header{
			DatabaseName: "t2017", ServerName: "5cd814ff04bc", BackupType: "full",
			Version: "14.0.3560", Major: 14, Product: "SQL Server 2017", CompatibilityLevel: 140,
			Collation: "SQL_Latin1_General_CP1_CI_AS", RecoveryModel: "FULL",
			BackupSize: 2641920, CompressedSize: 2641920,
			StartedAt: "2026-10-16T08:33:25", FinishedAt: "2026-10-16T08:33:25",
		}},
		{"headeronly-2022.json", Header{
			DatabaseName: "t2022", ServerName: "3c8ecb037496", BackupType: "full",
			Version: "16.0.4305", Major: 16, Product: "SQL Server 2022", CompatibilityLevel: 160,
			Collation: "SQL_Latin1_General_CP1_CI_AS", RecoveryModel: "FULL",
			Compressed: true, Checksums: true,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			rows := fixture(t, tc.file)
			if len(rows) != 1 {
				t.Fatalf("%d rows", len(rows))
			}
			got, err := parseHeader(rows[0])
			if err != nil {
				t.Fatal(err)
			}
			// Sizes and times of the 2022 backup are not worth pinning.
			if tc.want.BackupSize == 0 {
				tc.want.BackupSize, tc.want.CompressedSize = got.BackupSize, got.CompressedSize
				tc.want.StartedAt, tc.want.FinishedAt = got.StartedAt, got.FinishedAt
				if got.CompressedSize >= got.BackupSize {
					t.Errorf("compressed %d, backup %d", got.CompressedSize, got.BackupSize)
				}
			}
			if got != tc.want {
				t.Fatalf("header\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseHeaderEncryptedAndErrors(t *testing.T) {
	r := fixture(t, "headeronly-2017.json")[0]
	r["KeyAlgorithm"], r["EncryptorThumbprint"] = "aes_256", "0x0102"
	if h, err := parseHeader(r); err != nil || h.Encryption != "aes_256" {
		t.Fatalf("encrypted: %+v %v", h, err)
	}
	r["KeyAlgorithm"] = ""
	if h, _ := parseHeader(r); h.Encryption != "encrypted" {
		t.Fatalf("thumbprint only: %q", h.Encryption)
	}
	r["SoftwareVersionMajor"] = "fourteen"
	if _, err := parseHeader(r); err == nil || !strings.Contains(err.Error(), "SoftwareVersionMajor") {
		t.Fatalf("bad number: %v", err)
	}
	delete(r, "Collation")
	r["SoftwareVersionMajor"] = "14"
	if _, err := parseHeader(r); err == nil || !strings.Contains(err.Error(), "no column Collation") {
		t.Fatalf("missing column: %v", err)
	}
}

func TestParseFilesFixtures(t *testing.T) {
	for _, name := range []string{"2017", "2022"} {
		files, total, err := parseFiles(fixture(t, "filelistonly-"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		want := []DatabaseFile{
			{LogicalName: "t" + name, PhysicalName: "/var/opt/mssql/data/t" + name + ".mdf", Type: "D", FileGroup: "PRIMARY", SizeBytes: 8 << 20},
			{LogicalName: "t" + name + "_log", PhysicalName: "/var/opt/mssql/data/t" + name + "_log.ldf", Type: "L", SizeBytes: 8 << 20},
		}
		if len(files) != 2 || files[0] != want[0] || files[1] != want[1] || total != 16<<20 {
			t.Fatalf("%s: files %+v, total %d", name, files, total)
		}
	}
	rows := fixture(t, "filelistonly-2017.json")
	rows[0]["TDEThumbprint"], rows[1]["Type"] = "0xAB", "S"
	files, _, _ := parseFiles(rows)
	if !files[0].TDE || files[1].Type != "S" {
		t.Fatalf("tde/filestream: %+v", files)
	}
	if _, _, err := parseFiles(nil); err == nil {
		t.Fatal("no rows: no error")
	}
}

func TestParseLabelFixtures(t *testing.T) {
	set, families, family, err := parseLabel(fixture(t, "labelonly-2017.json")[0])
	if err != nil || set == "" || families != 1 || family != 1 {
		t.Fatalf("single: %q %d/%d %v", set, family, families, err)
	}
	_, families, family, err = parseLabel(fixture(t, "labelonly-striped.json")[0])
	if err != nil || families != 2 || family != 2 {
		t.Fatalf("striped: %d/%d %v", family, families, err)
	}
	if _, _, _, err := parseLabel(row{"MediaSetId": "x", "FamilyCount": "1", "FamilySequenceNumber": "2"}); err == nil {
		t.Fatal("family beyond the count: no error")
	}
}

func TestProductAndBackupType(t *testing.T) {
	products := map[int]string{9: "SQL Server 2005", 13: "SQL Server 2016", 14: "SQL Server 2017",
		15: "SQL Server 2019", 16: "SQL Server 2022", 17: "SQL Server 2025", 18: "SQL Server 18.x"}
	for major, want := range products {
		if got := Product(major); got != want {
			t.Errorf("Product(%d) = %q, want %q", major, got, want)
		}
	}
	if got := productName(10, 50); got != "SQL Server 2008 R2" {
		t.Errorf("10.50 = %q", got)
	}
	types := map[int64]string{1: "full", 2: "log", 5: "differential", 7: "partial", 3: "type 3"}
	for n, want := range types {
		if got := backupType(n); got != want {
			t.Errorf("backupType(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestEngineMajor(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"14.00.3540.1.v1", 14, true},
		{"16.00.4215.2.v1", 16, true},
		{"16.0.4305.5", 16, true},
		{"", 0, false},
		{"v14.00", 0, false},
		{"14", 0, false},
		{"0.1", 0, false},
	}
	for _, tc := range tests {
		got, err := EngineMajor(tc.in)
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("EngineMajor(%q) = %d, %v", tc.in, got, err)
		}
	}
}

func TestCell(t *testing.T) {
	guid := []byte{0x76, 0x5F, 0x70, 0xCD, 0xB0, 0x42, 0xDD, 0x40, 0x9C, 0x57, 0x43, 0x1B, 0xE6, 0x49, 0xFB, 0xB7}
	tests := []struct {
		v      any
		dbType string
		want   string
	}{
		{nil, "NVARCHAR", ""},
		{"t2017", "NVARCHAR", "t2017"},
		{int64(140), "TINYINT", "140"},
		{true, "BIT", "1"},
		{false, "BIT", "0"},
		{time.Date(2026, 10, 16, 8, 33, 25, 0, time.UTC), "DATETIME", "2026-10-16T08:33:25"},
		{[]byte("2641920"), "DECIMAL", "2641920"},
		{guid, "UNIQUEIDENTIFIER", "CD705F76-42B0-40DD-9C57-431BE649FBB7"},
		{[]byte{0xAB, 0x01}, "VARBINARY", "0xAB01"},
	}
	for _, tc := range tests {
		if got := cell(tc.v, tc.dbType); got != tc.want {
			t.Errorf("cell(%v, %s) = %q, want %q", tc.v, tc.dbType, got, tc.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 16 << 20: "16.0 MiB",
		97<<30 + 300<<20: "97.3 GiB", 3 << 40: "3.0 TiB"}
	for n, want := range tests {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStripes(t *testing.T) {
	l := func(host string, family, families int) label {
		return label{mount: mount{host: host, container: "/bak/" + host}, mediaSet: "set", families: families, family: family}
	}
	b := stripes([]label{l("b", 2, 2), l("a", 1, 2)})
	if b.Outcome != "" || strings.Join(b.Files, ",") != "a,b" || strings.Join(b.disks, ",") != "/bak/a,/bak/b" {
		t.Fatalf("in order: %+v", b)
	}
	b = stripes([]label{l("a", 1, 3), l("c", 3, 3)})
	if b.Outcome != Unreadable || !strings.Contains(b.Error, "2 of 3 files given, stripe 2 missing") || b.Remedy == "" {
		t.Fatalf("missing stripe: %+v", b)
	}
	b = stripes([]label{l("a", 1, 2), l("copy-of-a", 1, 2)})
	if b.Outcome != Unreadable || !strings.Contains(b.Error, "stripe 1 of 2") {
		t.Fatalf("same stripe twice: %+v", b)
	}
}

func TestSQLErrorAndHint(t *testing.T) {
	err := mssql.Error{Number: errTerminated, Message: "RESTORE LABELONLY is terminating abnormally.", All: []mssql.Error{
		{Number: 3241, Message: "The media family on device '/bak/2.bak' is incorrectly formed."},
		{Number: errTerminated, Message: "RESTORE LABELONLY is terminating abnormally."},
	}}
	names := map[string]string{"/bak/1.bak": "a.bak", "/bak/2.bak": "junk.bak"}
	if got := sqlError(err, names); got != "The media family on device 'junk.bak' is incorrectly formed." {
		t.Fatalf("sqlError = %q", got)
	}
	if h := hint(err, "SQL Server 2022"); !strings.Contains(h, "newer than SQL Server 2022") || !strings.Contains(h, "2025-latest") {
		t.Fatalf("hint = %q", h)
	}
	if h := hint(mssql.Error{Number: 3201, Message: "Cannot open backup device"}, ""); !strings.Contains(h, "permissions") {
		t.Fatalf("hint 3201 = %q", h)
	}
	if h := hint(errors.New("network"), ""); h != "" {
		t.Fatalf("hint for a non-SQL error = %q", h)
	}
}
