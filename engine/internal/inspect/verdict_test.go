package inspect

import (
	"strings"
	"testing"

	"github.com/decisyon/dialga/engine/internal/check"
)

func readBackup(major int, mutate func(*Backup)) Backup {
	b := Backup{
		Files:  []string{"/data/app.bak"},
		Header: &Header{DatabaseName: "app", BackupType: "full", Major: major, Version: "x", Product: Product(major)},
		DatabaseFiles: []DatabaseFile{
			{LogicalName: "app", Type: "D", FileGroup: "PRIMARY", SizeBytes: 8 << 20},
			{LogicalName: "app_log", Type: "L", SizeBytes: 8 << 20},
		},
		RequiredBytes: 16 << 20,
		BackupSets:    1,
	}
	if mutate != nil {
		mutate(&b)
	}
	return b
}

// finding returns the status and message of the named check, "" when absent.
func finding(b Backup, name string) (check.Status, string) {
	for _, f := range b.Findings {
		if f.Check == name {
			return f.Status, f.Message
		}
	}
	return "", ""
}

func TestJudgeVersion(t *testing.T) {
	tests := []struct {
		backup, target int
		want           Outcome
		message        string
	}{
		{13, 14, Restorable, "SQL Server 2016"},
		{14, 14, Restorable, "not newer than SQL Server 2017"},
		{15, 14, NotRestorable, "backup from SQL Server 2019"},
		{16, 14, NotRestorable, "the target runs SQL Server 2017"},
		{16, 16, Restorable, "SQL Server 2022"},
		{15, 16, Restorable, "SQL Server 2019"},
		{17, 16, NotRestorable, "backup from SQL Server 2025"},
		{9, 14, NotRestorable, "from SQL Server 2008 on"},
		{0, 14, NotRestorable, "not readable"},
	}
	for _, tc := range tests {
		b := readBackup(tc.backup, nil)
		judge(&b, Target{Name: "dev", Major: tc.target})
		st, msg := finding(b, "version")
		if b.Outcome != tc.want || !strings.Contains(msg, tc.message) {
			t.Errorf("backup %d on %d: outcome %s, version %s %q", tc.backup, tc.target, b.Outcome, st, msg)
		}
		newer := tc.backup > tc.target
		if newer != (b.Remedy != "") || (newer && !strings.Contains(b.Remedy, ".bacpac")) {
			t.Errorf("backup %d on %d: remedy %q", tc.backup, tc.target, b.Remedy)
		}
	}
	b := readBackup(16, nil)
	judge(&b, Target{Major: 14})
	if !strings.Contains(b.Remedy, "compatibility level to 140") {
		t.Errorf("remedy %q", b.Remedy)
	}
}

func TestJudgeRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Backup)
		check  string
		status check.Status
		want   Outcome
	}{
		{"full", nil, "type", check.OK, Restorable},
		{"differential", func(b *Backup) { b.Header.BackupType = "differential" }, "type", check.Warning, Restorable},
		{"log", func(b *Backup) { b.Header.BackupType = "log" }, "type", check.Warning, Restorable},
		{"partial", func(b *Backup) { b.Header.BackupType = "partial" }, "type", check.Failed, NotRestorable},
		{"not encrypted", nil, "encryption", check.OK, Restorable},
		{"encrypted", func(b *Backup) { b.Header.Encryption = "aes_256" }, "encryption", check.Failed, NotRestorable},
		{"tde", func(b *Backup) { b.DatabaseFiles[0].TDE = true }, "tde", check.Failed, NotRestorable},
		{"filestream", func(b *Backup) {
			b.DatabaseFiles = append(b.DatabaseFiles, DatabaseFile{LogicalName: "fs", Type: "S", FileGroup: "FS"})
		}, "filestream", check.Failed, NotRestorable},
		{"full-text 2005", func(b *Backup) {
			b.DatabaseFiles = append(b.DatabaseFiles, DatabaseFile{LogicalName: "ft", Type: "F"})
		}, "filestream", check.Warning, Restorable},
		{"two backup sets", func(b *Backup) { b.BackupSets = 2 }, "sets", check.Warning, Restorable},
		{"one backup set", nil, "sets", "", Restorable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := readBackup(14, tc.mutate)
			judge(&b, Target{Major: 14})
			if st, msg := finding(b, tc.check); st != tc.status || b.Outcome != tc.want {
				t.Fatalf("%s: %s %q, outcome %s", tc.check, st, msg, b.Outcome)
			}
		})
	}
}

func TestJudgeSpace(t *testing.T) {
	free := func(n int64) *int64 { return &n }
	tests := []struct {
		name     string
		required int64
		free     *int64
		want     check.Status
		message  string
	}{
		{"unknown", 1 << 30, nil, check.Warning, "free storage unknown"},
		{"half", 50, free(100), check.OK, "(50.0%)"},
		{"80% exactly", 80, free(100), check.OK, "(80.0%)"},
		{"above 80%", 81, free(100), check.Warning, "over 80% of the free storage"},
		{"all of it", 100, free(100), check.Warning, "(100.0%)"},
		{"more than free", 101, free(100), check.Failed, "not enough space"},
		{"nothing free", 1, free(0), check.Failed, "not enough space"},
		{"nothing needed", 0, free(0), check.OK, "(0.0%)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := judgeSpace(tc.required, Target{Instance: "dev-sqlserver", FreeBytes: tc.free})
			if f.Check != "space" || f.Status != tc.want || !strings.Contains(f.Message, tc.message) {
				t.Fatalf("%s %q", f.Status, f.Message)
			}
		})
	}
}

func TestOverall(t *testing.T) {
	target := &Target{Name: "dev"}
	failed := &Finding{Status: check.Failed}
	tests := []struct {
		name     string
		target   *Target
		outcomes []Outcome
		space    *Finding
		want     Outcome
	}{
		{"headers only", nil, []Outcome{Read, Read}, nil, Read},
		{"headers only, one unreadable", nil, []Outcome{Read, Unreadable}, nil, Unreadable},
		{"all restorable", target, []Outcome{Restorable, Restorable}, &Finding{Status: check.Warning}, Restorable},
		{"one not restorable", target, []Outcome{Restorable, NotRestorable}, &Finding{Status: check.OK}, NotRestorable},
		{"unreadable wins", target, []Outcome{NotRestorable, Unreadable}, &Finding{Status: check.OK}, Unreadable},
		{"no space", target, []Outcome{Restorable}, failed, NotRestorable},
	}
	for _, tc := range tests {
		res := &Result{Target: tc.target, Space: tc.space}
		for _, o := range tc.outcomes {
			res.Backups = append(res.Backups, Backup{Outcome: o})
		}
		if got := overall(res); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestVerdictDetail(t *testing.T) {
	bs := []Backup{
		{Files: []string{"/d/a.bak"}, Outcome: Restorable, Findings: []Finding{{Status: check.Warning}}},
		{Files: []string{"/d/s1.bak", "/d/s2.bak"}, Outcome: NotRestorable},
		{Files: []string{"/d/junk.bak"}, Outcome: Unreadable},
	}
	st, detail := verdictDetail(bs, "dev")
	if st != check.Failed || detail != "1 of 3 restorable on dev · not restorable: s1.bak+s2.bak · unreadable: junk.bak" {
		t.Fatalf("%s %q", st, detail)
	}
	if st, _ := verdictDetail(bs[:1], "dev"); st != check.Warning {
		t.Fatalf("warnings only: %s", st)
	}
}
