package transfer

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

var testTarget = config.Target{
	AccountID: "123456789012", Region: "eu-west-1", AWSProfile: "default", Instance: "dev-sqlserver",
	Bucket: testBucket, Prefixes: config.Prefixes{Import: "import", Export: "export"},
}

func reasons(b *Batch) map[string]string {
	m := map[string]string{}
	for _, r := range b.Rejected {
		m[filepath.Base(r.Path)] = r.Reason
	}
	return m
}

func TestPlanUploadsRejectsWithReasons(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte{1}, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("sales.bak", 12*MiB)
	exists := write("exists.bak", 10)
	fake := transfertest.New()
	fake.Put(testBucket, "import/exists.bak", []byte("0123456789"), "")
	other := filepath.Join(t.TempDir(), "sales.bak")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []string{good, write("with space.bak", 10), write("empty.bak", 0), dir, filepath.Join(dir, "missing.bak"), exists, other}
	b, err := PlanUploads(context.Background(), fake, "dev-sql", testTarget, UploadRequest{Files: files, Options: Options{PartSize: 5 * MiB}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 1 || b.Items[0].Key != "import/sales.bak" || b.Items[0].Parts != 3 || len(b.Plan.Actions) != 1 {
		t.Fatalf("items %+v", b.Items)
	}
	want := map[string]string{
		"with space.bak":   "no spaces or quotes allowed: rename the file",
		"empty.bak":        "the file is empty",
		filepath.Base(dir): "not a regular file",
		"missing.bak":      "cannot read it: " + noSuchFile,
		"exists.bak":       "already in s3://dev-sqlserver-backups/import/exists.bak (10 B, same size): choose overwrite",
		"sales.bak":        "same key as " + good,
	}
	got := reasons(b)
	if len(got) != len(want) || len(b.Rejected) != len(files)-1 {
		t.Fatalf("rejected %+v", b.Rejected)
	}
	for name, reason := range want {
		if !strings.Contains(got[name], reason) {
			t.Errorf("%s: reason %q, want %q", name, got[name], reason)
		}
	}
	if b.Confirmation != "" || len(b.Warnings) != 0 {
		t.Fatalf("confirmation %q, warnings %v", b.Confirmation, b.Warnings)
	}
}

func TestPlanUploadsPreview(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", 12*MiB)
	b, err := PlanUploads(context.Background(), transfertest.New(), "dev-sql", testTarget,
		UploadRequest{Files: []string{path}, Options: Options{PartSize: 5 * MiB}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := b.Plan.Preview(&out); err != nil {
		t.Fatal(err)
	}
	want := `Dry run: upload on dev-sql (instance dev-sqlserver). Nothing is changed.
  1. Upload sales.bak (12 MiB)
       PUT s3://dev-sqlserver-backups/import/sales.bak · multipart, 3 parts of 5 MiB, 3 at a time, CRC64NVME
       from ` + path + ` (12 MiB) · If-None-Match: * (never replaces an object)
`
	if out.String() != want {
		t.Fatalf("preview:\n%s\nwant:\n%s", out.String(), want)
	}
	if a := b.Plan.Actions[0]; a.ID != "upload-1" || a.Kind != core.KindAWS || a.Risk != core.Change {
		t.Fatalf("action %+v", a)
	}
}

func TestPlanUploadsConfirmation(t *testing.T) {
	a, _ := randomFile(t, "a.bak", 10)
	bFile, _ := randomFile(t, "b.bak", 10)
	fake := transfertest.New()
	fake.Put(testBucket, "import/a.bak", []byte("old"), "")
	plan := func(tgt config.Target, files ...string) *Batch {
		b, err := PlanUploads(context.Background(), fake, "dev-sql", tgt, UploadRequest{Files: files, Overwrite: true})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := plan(testTarget, bFile); b.Confirmation != "" || b.Items[0].Replaces {
		t.Fatalf("new object: confirmation %q", b.Confirmation)
	}
	b := plan(testTarget, a, bFile)
	if b.Confirmation != "import/a.bak" || b.Plan.Actions[0].Risk != core.Destructive || !b.Items[0].Replaces ||
		!strings.Contains(b.Plan.Actions[0].Statement, "replaces the object in S3 (3 B)") {
		t.Fatalf("replacing one object: confirmation %q, action %+v", b.Confirmation, b.Plan.Actions[0])
	}
	fake.Put(testBucket, "import/b.bak", []byte("old"), "")
	if b := plan(testTarget, a, bFile); b.Confirmation != "replace 2 objects" {
		t.Fatalf("replacing two: %q", b.Confirmation)
	}
	prod := testTarget
	prod.Protected = true
	if b := plan(prod, a); b.Confirmation != "dev-sql" {
		t.Fatalf("protected target: %q", b.Confirmation)
	}
}

func TestPlanUploadsKeysAndPrefixes(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", 10)
	fake := transfertest.New()
	str := func(s string) *string { return &s }
	tests := []struct {
		name    string
		req     UploadRequest
		wantKey string
		wantErr string
	}{
		{"default prefix", UploadRequest{Files: []string{path}}, "import/sales.bak", ""},
		{"explicit key", UploadRequest{Files: []string{path}, Key: "restore/2026/sales.bak"}, "restore/2026/sales.bak", ""},
		{"other prefix", UploadRequest{Files: []string{path}, Prefix: str("incoming/")}, "incoming/sales.bak", ""},
		{"bucket root", UploadRequest{Files: []string{path}, Prefix: str("")}, "sales.bak", ""},
		{"key for two files", UploadRequest{Files: []string{path, path}, Key: "x.bak"}, "", "a key names one file"},
		{"key and prefix", UploadRequest{Files: []string{path}, Key: "x.bak", Prefix: str("p")}, "", "not both"},
		{"wildcard key", UploadRequest{Files: []string{path}, Key: "sales*.bak"}, "", "wildcard"},
		{"folder key", UploadRequest{Files: []string{path}, Key: "import/"}, "", "names a folder"},
		{"quote in prefix", UploadRequest{Files: []string{path}, Prefix: str("it's")}, "", "invalid prefix"},
		{"parent in key", UploadRequest{Files: []string{path}, Key: "../x.bak"}, "", "parent directory"},
		{"no files", UploadRequest{}, "", "no files"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := PlanUploads(context.Background(), fake, "dev-sql", testTarget, tc.req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(b.Items) != 1 || b.Items[0].Key != tc.wantKey {
				t.Fatalf("plan %+v, err %v", b, err)
			}
		})
	}
}

func TestPlanUploadsWarnsAboutKMS(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", 10)
	fake := transfertest.New()
	fake.Encryption = types.ServerSideEncryptionAwsKms
	b, err := PlanUploads(context.Background(), fake, "dev-sql", testTarget, UploadRequest{Files: []string{path}})
	if err != nil || len(b.Warnings) != 1 || !strings.Contains(b.Warnings[0], "SSE-KMS") {
		t.Fatalf("warnings %v, err %v", b.Warnings, err)
	}
	fake.Encryption = types.ServerSideEncryptionAes256
	if b, _ := PlanUploads(context.Background(), fake, "dev-sql", testTarget, UploadRequest{Files: []string{path}}); len(b.Warnings) != 0 {
		t.Fatalf("SSE-S3 warnings %v", b.Warnings)
	}
}

func TestPlanUploadsExecute(t *testing.T) {
	a, dataA := randomFile(t, "a.bak", 6*MiB)
	bFile, _ := randomFile(t, "b.bak", KiB)
	fake := transfertest.New()
	var steps []string
	o := quick(nil)
	o.OnProgress = func(p Progress) {
		if len(steps) == 0 || steps[len(steps)-1] != p.Step {
			steps = append(steps, p.Step)
		}
	}
	b, err := PlanUploads(context.Background(), fake, "dev-sql", testTarget, UploadRequest{Files: []string{a, bFile}, Options: o})
	if err != nil {
		t.Fatal(err)
	}
	var events []core.Event
	if err := b.Execute(context.Background(), "", func(e core.Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[1].Status != core.OK || !strings.Contains(events[1].Detail, "CRC64NVME verified") {
		t.Fatalf("events %+v", events)
	}
	res := b.Results()
	if len(res) != 2 || res[0].Key != "import/a.bak" || !res[1].Verified || !bytes.Equal(fake.Get(testBucket, "import/a.bak").Data, dataA) {
		t.Fatalf("results %+v", res)
	}
	if strings.Join(steps, ",") != "upload-1,upload-2" {
		t.Fatalf("progress steps %v", steps)
	}
}

func TestPlanDownload(t *testing.T) {
	fake := transfertest.New()
	data := randomObject(t, fake, "export/sales.bak", 3*MiB, types.ChecksumAlgorithmCrc64nvme)
	fake.Put(testBucket, "root.bak", []byte("r"), "")
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.bak")
	if err := os.WriteFile(existing, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		req        DownloadRequest
		wantKey    string
		wantPath   string
		wantReject string
		wantErr    string
	}{
		{"bare name under the export prefix", DownloadRequest{Source: "sales.bak", Dest: dir}, "export/sales.bak", filepath.Join(dir, "sales.bak"), "", ""},
		{"key", DownloadRequest{Source: "export/sales.bak", Dest: filepath.Join(dir, "copy.bak")}, "export/sales.bak", filepath.Join(dir, "copy.bak"), "", ""},
		{"s3 URL", DownloadRequest{Source: "s3://dev-sqlserver-backups/root.bak", Dest: dir}, "root.bak", filepath.Join(dir, "root.bak"), "", ""},
		{"replaces when asked", DownloadRequest{Source: "sales.bak", Dest: existing, Overwrite: true}, "export/sales.bak", existing, "", ""},
		{"another bucket", DownloadRequest{Source: "s3://other/root.bak", Dest: dir}, "", "", "", "the bucket must be dev-sqlserver-backups"},
		{"wildcard", DownloadRequest{Source: "export/*.bak", Dest: dir}, "", "", "", "wildcard"},
		{"missing folder", DownloadRequest{Source: "sales.bak", Dest: filepath.Join(dir, "no", "x.bak")}, "", "", "", "does not exist"},
		{"missing object", DownloadRequest{Source: "nope.bak", Dest: dir}, "", "", "s3://dev-sqlserver-backups/export/nope.bak does not exist", ""},
		{"existing file", DownloadRequest{Source: "sales.bak", Dest: existing}, "", "", "already exists (4 B): choose overwrite", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := PlanDownload(context.Background(), fake, "dev-sql", testTarget, tc.req)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			case err != nil:
				t.Fatal(err)
			case tc.wantReject != "":
				if len(b.Rejected) != 1 || !strings.Contains(b.Rejected[0].Reason, tc.wantReject) || len(b.Plan.Actions) != 0 {
					t.Fatalf("rejected %+v", b.Rejected)
				}
				return
			}
			if len(b.Items) != 1 || b.Items[0].Key != tc.wantKey || b.Items[0].Path != tc.wantPath || len(b.Rejected) != 0 {
				t.Fatalf("items %+v, rejected %+v", b.Items, b.Rejected)
			}
			if tc.req.Overwrite && (b.Confirmation != "old.bak" || b.Plan.Actions[0].Risk != core.Destructive) {
				t.Fatalf("replacing: confirmation %q", b.Confirmation)
			}
		})
	}

	b, err := PlanDownload(context.Background(), fake, "dev-sql", testTarget, DownloadRequest{Source: "sales.bak", Dest: dir, Options: Options{PartSize: MiB}})
	if err != nil {
		t.Fatal(err)
	}
	wantStatement := "GET s3://dev-sqlserver-backups/export/sales.bak · 3 ranges of 1 MiB, 3 at a time, CRC64NVME verified\n" +
		"to " + filepath.Join(dir, "sales.bak") + " through a temporary file · never replaces a file"
	if a := b.Plan.Actions[0]; a.Statement != wantStatement || a.Title != "Download sales.bak (3 MiB)" {
		t.Fatalf("action %q\n%s", a.Title, a.Statement)
	}
	if err := b.Execute(context.Background(), "", func(core.Event) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sales.bak")); !bytes.Equal(got, data) || len(b.Results()) != 1 {
		t.Fatal("the download is not the object")
	}
	full := quick(nil)
	full.free = func(string) (uint64, error) { return 10, nil }
	b, err = PlanDownload(context.Background(), fake, "dev-sql", testTarget, DownloadRequest{Source: "sales.bak", Dest: t.TempDir(), Options: full})
	if err != nil || len(b.Rejected) != 1 || !strings.Contains(b.Rejected[0].Reason, "not enough free space") {
		t.Fatalf("full disk: %+v, %v", b.Rejected, err)
	}
}

// noSuchFile is the text the OS gives for a missing file.
var noSuchFile = func() string {
	_, err := os.Open(filepath.Join(os.TempDir(), "dialga-test-missing-file"))
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return "no such file or directory"
}()
