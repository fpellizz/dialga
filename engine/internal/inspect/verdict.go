package inspect

import (
	"fmt"
	"strings"

	"github.com/decisyon/dialga/engine/internal/check"
)

// spaceWarnRatio: the restored files above this share of the free storage
// are a warning; above all of it, a failure.
const spaceWarnRatio = 0.8

// judge fills the findings, the outcome and the remedy of a backup whose
// header and files were read, against the engine of the target.
func judge(b *Backup, t Target) {
	h := b.Header
	add := func(name string, st check.Status, format string, args ...any) {
		b.Findings = append(b.Findings, Finding{Check: name, Status: st, Message: fmt.Sprintf(format, args...)})
	}
	target := Product(t.Major)
	switch {
	case h.Major == 0:
		add("version", check.Failed, "source version not readable")
	case h.Major > t.Major:
		add("version", check.Failed, "backup from %s (%s): the target runs %s, and RDS cannot restore a backup from a newer version",
			h.Product, h.Version, target)
		b.Remedy = fmt.Sprintf("Restore it on %s or newer (a local container), lower its compatibility level to %d, "+
			"export a .bacpac and import that: import runbook, procedure C (.bak newer than the target)", h.Product, t.Major*10)
	case h.Major < 10:
		add("version", check.Failed, "backup from %s: %s restores backups from SQL Server 2008 on", h.Product, target)
	default:
		add("version", check.OK, "%s (%s), not newer than %s on the target", h.Product, h.Version, target)
	}
	switch h.BackupType {
	case "full":
		add("type", check.OK, "full backup")
	case "differential":
		add("type", check.Warning, "differential backup: restore the full backup first, with NORECOVERY")
	case "log":
		add("type", check.Warning, "log backup: RDS applies it with rds_restore_log, after a full or differential backup restored with NORECOVERY")
	default:
		add("type", check.Failed, "%s backup: the RDS native restore takes full, differential and log backups only", h.BackupType)
	}
	if h.Encryption != "" {
		add("encryption", check.Failed, "backup encrypted (%s): it needs the certificate of the source, which RDS cannot import", h.Encryption)
	} else {
		add("encryption", check.OK, "backup not encrypted")
	}
	for _, f := range b.DatabaseFiles {
		if f.TDE {
			add("tde", check.Failed, "file %s uses Transparent Data Encryption: RDS cannot restore it without the certificate of the source", f.LogicalName)
			break
		}
	}
	for _, f := range b.DatabaseFiles {
		switch f.Type {
		case "S":
			add("filestream", check.Failed, "file %s (filegroup %s) is FILESTREAM, FileTable or In-Memory OLTP: RDS does not support FILESTREAM",
				f.LogicalName, f.FileGroup)
		case "F":
			add("filestream", check.Warning, "full-text catalog %s in the SQL Server 2005 format", f.LogicalName)
		}
	}
	if b.BackupSets > 1 {
		add("sets", check.Warning, "%d backup sets in the media: RDS restores the first one; better a .bak with one backup", b.BackupSets)
	}
	b.Outcome = Restorable
	for _, f := range b.Findings {
		if f.Status == check.Failed {
			b.Outcome = NotRestorable
		}
	}
}

// judgeSpace compares what the restored databases take with the free storage.
func judgeSpace(required int64, t Target) Finding {
	f := Finding{Check: "space", Status: check.OK}
	if t.FreeBytes == nil {
		f.Status = check.Warning
		f.Message = fmt.Sprintf("%s needed on restore; free storage unknown (CloudWatch not readable): check it before the restore",
			FormatBytes(required))
		return f
	}
	free := *t.FreeBytes
	share := 0.0
	switch {
	case free > 0:
		share = float64(required) / float64(free)
	case required > 0:
		share = 1e9 // nothing free at all
	}
	f.Message = fmt.Sprintf("%s needed on restore, %s free on %s (%.1f%%)",
		FormatBytes(required), FormatBytes(free), t.Instance, share*100)
	switch {
	case share > 1:
		f.Status = check.Failed
		f.Message += ": not enough space, grow the storage first"
	case share > spaceWarnRatio:
		f.Status = check.Warning
		f.Message += fmt.Sprintf(": over %.0f%% of the free storage", spaceWarnRatio*100)
	}
	return f
}

// verdictDetail sums up the outcomes of the backups.
func verdictDetail(backups []Backup, target string) (check.Status, string) {
	var ok, bad, broken []string
	warn := false
	for _, b := range backups {
		switch b.Outcome {
		case Restorable:
			ok = append(ok, b.Name())
		case NotRestorable:
			bad = append(bad, b.Name())
		case Unreadable:
			broken = append(broken, b.Name())
		}
		for _, f := range b.Findings {
			warn = warn || f.Status == check.Warning
		}
	}
	detail := fmt.Sprintf("%d of %d restorable on %s", len(ok), len(backups), target)
	if len(bad) > 0 {
		detail += " · not restorable: " + strings.Join(bad, ", ")
	}
	if len(broken) > 0 {
		detail += " · unreadable: " + strings.Join(broken, ", ")
	}
	switch {
	case len(bad) > 0 || len(broken) > 0:
		return check.Failed, detail
	case warn:
		return check.Warning, detail
	}
	return check.OK, detail
}

// describe is the target line of the report.
func (t Target) describe() string {
	s := fmt.Sprintf("%s · %s %s (%s)", t.Instance, t.Engine, t.EngineVersion, Product(t.Major))
	if t.FreeBytes == nil {
		return s + " · free storage unknown (CloudWatch not readable)"
	}
	return s + " · " + FormatBytes(*t.FreeBytes) + " free"
}
