package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/check"
	"github.com/decisyon/dialga/engine/internal/jobs"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

func TestExportChecksFreeSpace(t *testing.T) {
	env, rds, _ := testEnv(t, testTarget)
	rds.states["sales"] = "ONLINE"
	rds.report = func(db string) (mssql.DatabaseReport, error) {
		return mssql.DatabaseReport{Database: mssql.DatabaseInfo{Name: db, State: "ONLINE"}, Files: []mssql.DatabaseFile{
			{Type: "ROWS", SizeMB: 1536}, {Type: "ROWS", SizeMB: 512}, {Type: "LOG", SizeMB: 4096},
		}}, nil
	}
	dir := t.TempDir()
	req := Request{Kind: jobs.Export, Export: []ExportItem{{Database: "sales", Key: "export/sales.bak", Destination: dir}}}
	tests := []struct {
		name       string
		free       uint64
		err        error
		wantStatus check.Status
		wantDetail string
	}{
		{"room for the data files", 3 << 30, nil, check.OK, "3 GiB free in " + dir + ", the data files of sales take 2 GiB"},
		{"less than the data files", 1 << 30, nil, check.Warning, "the backup may not fit"},
		{"cannot read the disk", 0, errors.New("statfs: permission denied"), check.Failed, "permission denied"},
		{"OS cannot tell", 0, errors.ErrUnsupported, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var asked string
			env.FreeSpace = func(d string) (uint64, error) { asked = d; return tc.free, tc.err }
			p, err := Build(context.Background(), env, req)
			if err != nil {
				t.Fatal(err)
			}
			var got *check.Event
			for i, c := range p.Items[0].Checks {
				if c.Step == "space" {
					got = &p.Items[0].Checks[i]
				}
			}
			switch {
			case tc.wantStatus == "" && got != nil:
				t.Fatalf("unexpected check %+v", *got)
			case tc.wantStatus == "":
			case got == nil || got.Status != tc.wantStatus || !strings.Contains(got.Detail, tc.wantDetail) || asked != dir:
				t.Fatalf("check %+v, asked %q", got, asked)
			}
			// Only a disk that cannot be read refuses the job.
			if p.Ready != (tc.wantStatus != check.Failed) {
				t.Fatalf("ready %v", p.Ready)
			}
		})
	}
}

// checksumReporter is a step Reporter that records the checksum, as the
// jobs reporter does once Item.Checksum is there.
type checksumReporter struct {
	got []string
}

func (r *checksumReporter) Item() string                          { return "sales" }
func (r *checksumReporter) Transfer(jobs.Direction, int64, int64) {}
func (r *checksumReporter) Task(jobs.TaskStatus)                  {}
func (r *checksumReporter) Upload(jobs.UploadState)               {}
func (r *checksumReporter) Log(jobs.Level, string)                {}
func (r *checksumReporter) Result(string)                         {}
func (r *checksumReporter) Checksum(algo, value string, verified bool, note string) {
	r.got = append(r.got, strings.Join([]string{algo, value, map[bool]string{true: "verified", false: "not verified"}[verified], note}, "|"))
}

func TestTransferStepsReportTheChecksum(t *testing.T) {
	env, _, s3 := testEnv(t, testTarget)
	ctx := context.Background()
	s3.Put(testBucket, "export/sales.bak", []byte("backup"), types.ChecksumAlgorithmCrc32c)
	r := &checksumReporter{}
	dl := DownloadParams{Database: "sales", Bucket: testBucket, Key: "export/sales.bak", Path: t.TempDir() + "/sales.bak"}
	if _, err := dl.Step(env, "download").Run(ctx, r); err != nil {
		t.Fatal(err)
	}
	want := "CRC32C|" + s3.Get(testBucket, "export/sales.bak").Checksums[types.ChecksumAlgorithmCrc32c] + "|verified|"
	if len(r.got) != 1 || r.got[0] != want {
		t.Fatalf("download checksum %v, want %q", r.got, want)
	}

	r = &checksumReporter{}
	file := backup(t, "sales.bak", "sales backup")
	up := UploadParams{Bucket: testBucket, Files: []UploadFile{{Path: file, Key: "import/sales.bak", Size: 12, ModTime: modTime(t, file)}}}
	if _, err := up.Step(env, "upload").Run(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 || !strings.HasPrefix(r.got[0], "CRC64NVME|") || !strings.Contains(r.got[0], "|verified|") {
		t.Fatalf("upload checksum %v", r.got)
	}

	r = &checksumReporter{}
	a, b := backup(t, "crm_1.bak", "one"), backup(t, "crm_2.bak", "two")
	stripes := UploadParams{Bucket: testBucket, Files: []UploadFile{
		{Path: a, Key: "import/crm_1.bak", Size: 3, ModTime: modTime(t, a)},
		{Path: b, Key: "import/crm_2.bak", Size: 3, ModTime: modTime(t, b)},
	}}
	if _, err := stripes.Step(env, "upload").Run(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 || r.got[0] != "CRC64NVME||verified|2 files, one checksum each: see the step detail" {
		t.Fatalf("stripes checksum %v", r.got)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime()
}
