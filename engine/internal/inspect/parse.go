package inspect

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/decisyon/dialga/engine/internal/check"
)

// label is what RESTORE LABELONLY says about one file: the media set it
// belongs to and its place among the stripes ("media families").
type label struct {
	mount
	mediaSet         string
	families, family int
}

// fields reads the columns of a row; the first problem sticks in err.
type fields struct {
	r   row
	err error
}

func (f *fields) str(col string) string {
	v, ok := f.r[col]
	if !ok && f.err == nil {
		f.err = fmt.Errorf("no column %s in the answer", col)
	}
	return v
}

func (f *fields) int(col string) int64 {
	v := f.str(col)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil && f.err == nil {
		f.err = fmt.Errorf("column %s: %q is not a whole number", col, v)
	}
	return n
}

func (f *fields) bool(col string) bool { return f.str(col) == "1" }

func parseLabel(r row) (mediaSet string, families, family int, err error) {
	f := fields{r: r}
	mediaSet = f.str("MediaSetId")
	families, family = int(f.int("FamilyCount")), int(f.int("FamilySequenceNumber"))
	if f.err == nil && (mediaSet == "" || families < 1 || family < 1 || family > families) {
		f.err = fmt.Errorf("unexpected media label: set %q, family %d of %d", mediaSet, family, families)
	}
	return mediaSet, families, family, f.err
}

// parseHeader reads a row of RESTORE HEADERONLY.
func parseHeader(r row) (Header, error) {
	f := fields{r: r}
	h := Header{
		DatabaseName:       f.str("DatabaseName"),
		ServerName:         f.str("ServerName"),
		BackupType:         backupType(f.int("BackupType")),
		Major:              int(f.int("SoftwareVersionMajor")),
		CompatibilityLevel: int(f.int("CompatibilityLevel")),
		Collation:          f.str("Collation"),
		RecoveryModel:      f.str("RecoveryModel"),
		Compressed:         f.bool("Compressed"),
		Checksums:          f.bool("HasBackupChecksums"),
		CopyOnly:           f.bool("IsCopyOnly"),
		Encryption:         f.str("KeyAlgorithm"),
		BackupSize:         f.int("BackupSize"),
		CompressedSize:     f.int("CompressedBackupSize"),
		StartedAt:          f.str("BackupStartDate"),
		FinishedAt:         f.str("BackupFinishDate"),
	}
	minor, build := f.int("SoftwareVersionMinor"), f.int("SoftwareVersionBuild")
	h.Version = fmt.Sprintf("%d.%d.%d", h.Major, minor, build)
	h.Product = productName(h.Major, int(minor))
	if h.Encryption == "" && f.str("EncryptorThumbprint") != "" {
		h.Encryption = "encrypted"
	}
	return h, f.err
}

// parseFiles reads the rows of RESTORE FILELISTONLY and sums their sizes.
func parseFiles(rows []row) ([]DatabaseFile, int64, error) {
	if len(rows) == 0 {
		return nil, 0, errors.New("no database file in the backup")
	}
	files := make([]DatabaseFile, 0, len(rows))
	var total int64
	for _, r := range rows {
		f := fields{r: r}
		df := DatabaseFile{
			LogicalName:  f.str("LogicalName"),
			PhysicalName: f.str("PhysicalName"),
			Type:         f.str("Type"),
			FileGroup:    f.str("FileGroupName"),
			SizeBytes:    f.int("Size"),
			TDE:          f.str("TDEThumbprint") != "",
		}
		if f.err != nil {
			return nil, 0, f.err
		}
		files = append(files, df)
		total += df.SizeBytes
	}
	return files, total, nil
}

func backupType(n int64) string {
	switch n {
	case 1:
		return "full"
	case 2:
		return "log"
	case 4:
		return "file"
	case 5:
		return "differential"
	case 6:
		return "file differential"
	case 7:
		return "partial"
	case 8:
		return "partial differential"
	}
	return fmt.Sprintf("type %d", n)
}

// Product names a SQL Server major version: 14 → SQL Server 2017.
func Product(major int) string {
	years := map[int]string{8: "2000", 9: "2005", 10: "2008", 11: "2012", 12: "2014",
		13: "2016", 14: "2017", 15: "2019", 16: "2022", 17: "2025"}
	if y, ok := years[major]; ok {
		return "SQL Server " + y
	}
	return fmt.Sprintf("SQL Server %d.x", major)
}

func productName(major, minor int) string {
	if major == 10 && minor == 50 {
		return "SQL Server 2008 R2"
	}
	return Product(major)
}

// EngineMajor is the major version of a SQL Server or RDS engine version:
// 14.00.3540.1.v1 → 14.
func EngineMajor(version string) (int, error) {
	head, _, ok := strings.Cut(version, ".")
	n, err := strconv.Atoi(head)
	if !ok || err != nil || n < 1 || n > 99 {
		return 0, fmt.Errorf("unexpected SQL Server version %q", version)
	}
	return n, nil
}

func majorOf(version string) int {
	n, _ := EngineMajor(version)
	return n
}

// readMedia reads the label of every file and groups the files into backups:
// one per media set, its stripes in order. A file SQL Server cannot read is
// a backup of its own, unreadable.
func readMedia(ctx context.Context, q querier, disks []mount, names map[string]string, reader string) ([]Backup, []string, error) {
	var backups []Backup
	index := map[string]int{} // media set → position in backups
	members := map[int][]label{}
	for _, d := range disks {
		rows, err := q.query(ctx, "RESTORE LABELONLY FROM DISK = N'"+d.container+"'")
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		l := label{mount: d}
		if err == nil && len(rows) == 0 {
			err = errors.New("RESTORE LABELONLY returned no label")
		}
		if err == nil {
			l.mediaSet, l.families, l.family, err = parseLabel(rows[0])
		}
		if err != nil {
			backups = append(backups, unreadable(Backup{Files: []string{d.host}}, err, names, reader))
			continue
		}
		i, ok := index[l.mediaSet]
		if !ok {
			i = len(backups)
			index[l.mediaSet] = i
			backups = append(backups, Backup{})
		}
		members[i] = append(members[i], l)
	}
	for i, ls := range members {
		backups[i] = stripes(ls)
	}
	var probs []string
	for _, b := range backups {
		if b.Outcome == Unreadable {
			probs = append(probs, b.Name()+": "+b.Error)
		}
	}
	return backups, probs, nil
}

// stripes puts the files of one media set in order and checks they are all
// there, once each.
func stripes(ls []label) Backup {
	slices.SortStableFunc(ls, func(a, b label) int { return a.family - b.family })
	var b Backup
	have := map[int]bool{}
	for _, l := range ls {
		b.Files = append(b.Files, l.host)
		b.disks = append(b.disks, l.container)
		if have[l.family] {
			b.Outcome = Unreadable
			b.Error = fmt.Sprintf("two files are stripe %d of %d of the same backup: give each stripe once", l.family, l.families)
			return b
		}
		have[l.family] = true
	}
	if n := ls[0].families; len(ls) < n {
		var missing []string
		for f := 1; f <= n; f++ {
			if !have[f] {
				missing = append(missing, strconv.Itoa(f))
			}
		}
		b.Outcome = Unreadable
		b.Error = fmt.Sprintf("striped backup: %d of %d files given, stripe %s missing", len(ls), n, strings.Join(missing, ", "))
		b.Remedy = "Add all the files of the backup: SQL Server reads a striped backup only with every stripe"
	}
	return b
}

// unreadable marks b as a backup SQL Server could not read.
func unreadable(b Backup, err error, names map[string]string, reader string) Backup {
	b.Outcome = Unreadable
	b.Error = sqlError(err, names)
	b.Remedy = hint(err, reader)
	return b
}

// hint is what to try when SQL Server cannot read a file.
func hint(err error, reader string) string {
	for _, n := range sqlNumbers(err) {
		switch n {
		case 3169, 3241, 3242, 3243: // newer version, media incorrectly formed, not a backup, format
			return fmt.Sprintf("Not a SQL Server backup, damaged, or made by a SQL Server newer than %s, "+
				"which reads it here: for a SQL Server 2025 backup use the image "+
				"mcr.microsoft.com/mssql/server:2025-latest", reader)
		case 3201: // cannot open backup device
			return "The container cannot read the file: check its permissions"
		}
	}
	return ""
}

// restoreFrom is the FROM clause of a backup.
func restoreFrom(disks []string) string {
	parts := make([]string, len(disks))
	for i, d := range disks {
		parts[i] = "DISK = N'" + d + "'"
	}
	return " FROM " + strings.Join(parts, ", ")
}

// readHeaders runs RESTORE HEADERONLY on every readable backup.
func readHeaders(ctx context.Context, q querier, backups []Backup, names map[string]string, reader string) ([]string, error) {
	var probs []string
	for i := range backups {
		b := &backups[i]
		if b.Outcome != "" {
			continue
		}
		rows, err := q.query(ctx, "RESTORE HEADERONLY"+restoreFrom(b.disks))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && len(rows) == 0 {
			err = errors.New("no backup set in the media")
		}
		var h Header
		if err == nil {
			h, err = parseHeader(rows[0])
		}
		if err != nil {
			*b = unreadable(*b, err, names, reader)
			probs = append(probs, b.Name()+": "+b.Error)
			continue
		}
		b.Header, b.BackupSets = &h, len(rows)
	}
	return probs, nil
}

// readFiles runs RESTORE FILELISTONLY on the first backup set of every
// readable backup.
func readFiles(ctx context.Context, q querier, backups []Backup, names map[string]string, reader string) ([]string, error) {
	var probs []string
	for i := range backups {
		b := &backups[i]
		if b.Outcome != "" {
			continue
		}
		rows, err := q.query(ctx, "RESTORE FILELISTONLY"+restoreFrom(b.disks)+" WITH FILE = 1")
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			b.DatabaseFiles, b.RequiredBytes, err = parseFiles(rows)
		}
		if err != nil {
			*b = unreadable(*b, err, names, reader)
			probs = append(probs, b.Name()+": "+b.Error)
		}
	}
	return probs, nil
}

// Name is how the reports call a backup: its file names (the database may
// be in several backups).
func (b Backup) Name() string {
	names := make([]string, len(b.Files))
	for i, f := range b.Files {
		names[i] = filepath.Base(f)
	}
	return strings.Join(names, "+")
}

// withProblems appends what could not be read in a step to its detail.
func withProblems(detail string, probs []string) string {
	if len(probs) == 0 {
		return detail
	}
	return detail + " · " + strings.Join(probs, " · ")
}

// stepStatus is failed when some backup could not be read in the step.
func stepStatus(probs []string) check.Status {
	if len(probs) > 0 {
		return check.Failed
	}
	return check.OK
}

func mediaDetail(files int, backups []Backup) string {
	striped := 0
	for _, b := range backups {
		if len(b.Files) > 1 {
			striped++
		}
	}
	s := fmt.Sprintf("%d file(s), %d backup(s)", files, len(backups))
	if striped > 0 {
		s += fmt.Sprintf(", %d striped", striped)
	}
	return s
}

func headerDetail(backups []Backup) string {
	var out []string
	for _, b := range backups {
		if h := b.Header; h != nil {
			out = append(out, fmt.Sprintf("%s: %s, %s (%s), %s", b.Name(), h.DatabaseName, h.Product, h.Version, h.BackupType))
		}
	}
	return strings.Join(out, " · ")
}

func filesDetail(backups []Backup, required int64) string {
	n := 0
	for _, b := range backups {
		n += len(b.DatabaseFiles)
	}
	return fmt.Sprintf("%d database file(s), %s needed on restore", n, FormatBytes(required))
}

// FormatBytes renders n in binary units with one decimal: 12.0 MiB.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
