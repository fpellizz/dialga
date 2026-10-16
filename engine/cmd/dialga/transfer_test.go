package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/transfer"
	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

const testBucket = "dev-sqlserver-backups"

// transferApp is an app on doc whose S3 is fake.
func transferApp(t *testing.T, doc string, fake *transfertest.S3) *app {
	t.Helper()
	a, _, _ := testApp(t, doc)
	a.openS3 = func(context.Context, config.Target) (transfer.Client, error) { return fake, nil }
	return a
}

func localFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadDryRun(t *testing.T) {
	fake := transfertest.New()
	a := transferApp(t, testConfig, fake)
	path := localFile(t, "sales.bak", "backup")
	code, out, _ := run(a, "upload", path)
	if code != 0 || !strings.Contains(out, "Dry run: upload on dev-sql") ||
		!strings.Contains(out, "PUT s3://dev-sqlserver-backups/import/sales.bak · multipart, 1 part, CRC64NVME") ||
		!strings.Contains(out, "Run again with --apply to upload.") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if fake.Calls("CreateMultipartUpload") != 0 {
		t.Fatal("a dry run started an upload")
	}
}

func TestUploadApply(t *testing.T) {
	fake := transfertest.New()
	a := transferApp(t, testConfig, fake)
	path := localFile(t, "sales.bak", "backup")
	code, out, errOut := run(a, "upload", path, "--prefix", "incoming", "--apply")
	if code != 0 || !strings.Contains(out, "OK    Upload sales.bak (6 B)") || !strings.Contains(out, "CRC64NVME verified") ||
		!strings.Contains(out, "Uploaded 1 file(s).") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if obj := fake.Get(testBucket, "incoming/sales.bak"); obj == nil || string(obj.Data) != "backup" {
		t.Fatal("the object is not in S3")
	}
	// Not a terminal: plain progress lines, the last one always at 100%.
	if !strings.HasSuffix(errOut, "sales.bak  100%  6 B of 6 B\n") && !strings.Contains(errOut, "sales.bak  100%  6 B of 6 B  ") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestUploadRejectedFiles(t *testing.T) {
	fake := transfertest.New()
	fake.Put(testBucket, "import/old.bak", []byte("old"), "")
	a := transferApp(t, testConfig, fake)
	old, fresh := localFile(t, "old.bak", "new"), localFile(t, "fresh.bak", "fresh")
	code, out, _ := run(a, "upload", old, fresh)
	if code != exitCheckFailed || !strings.Contains(out, "REJECTED  "+old+": already in s3://dev-sqlserver-backups/import/old.bak") {
		t.Fatalf("dry run: code %d, out %q", code, out)
	}
	// The others go; the exit status still says something was rejected.
	code, out, _ = run(a, "upload", old, fresh, "--apply")
	if code != exitCheckFailed || !strings.Contains(out, "Uploaded 1 file(s); 1 rejected.") ||
		fake.Get(testBucket, "import/fresh.bak") == nil || string(fake.Get(testBucket, "import/old.bak").Data) != "old" {
		t.Fatalf("apply: code %d, out %q", code, out)
	}
}

func TestUploadJSON(t *testing.T) {
	fake := transfertest.New()
	a := transferApp(t, testConfig, fake)
	path := localFile(t, "sales.bak", "backup")
	code, out, errOut := run(a, "-o", "json", "upload", path, "--apply")
	var doc struct {
		Applied bool              `json:"applied"`
		OK      bool              `json:"ok"`
		Items   []transfer.Item   `json:"items"`
		Results []transfer.Result `json:"results"`
		Steps   []struct {
			Status string `json:"status"`
		} `json:"steps"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil || !doc.Applied || !doc.OK || len(doc.Results) != 1 ||
		!doc.Results[0].Verified || len(doc.Steps) != 1 || doc.Steps[0].Status != "ok" || doc.Items[0].Key != "import/sales.bak" {
		t.Fatalf("code %d, out %q", code, out)
	}
	events := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(errOut))
	for sc.Scan() {
		var line struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("stderr line %q: %v", sc.Text(), err)
		}
		events[line.Event]++
	}
	if events["step"] != 2 || events["progress"] < 1 {
		t.Fatalf("events on stderr %v", events)
	}

	code, out, _ = run(a, "-o", "json", "upload", path)
	if code != exitCheckFailed || !strings.Contains(out, `"applied":false`) || !strings.Contains(out, `"reason":"already in`) {
		t.Fatalf("dry run json: code %d, out %q", code, out)
	}
}

func TestUploadConfirmation(t *testing.T) {
	protected := strings.Replace(testConfig, "    sql:", "    protected: true\n    sql:", 1)
	fake := transfertest.New()
	a := transferApp(t, protected, fake)
	path := localFile(t, "sales.bak", "backup")
	if code, out, _ := run(a, "upload", path); code != 0 || !strings.Contains(out, `Running it asks to retype "dev-sql".`) {
		t.Fatalf("dry run: code %d, out %q", code, out)
	}
	if code, _, errOut := run(a, "upload", path, "--apply"); code != exitFailure || !strings.Contains(errOut, "pass --confirm dev-sql") {
		t.Fatalf("no confirmation: code %d, stderr %q", code, errOut)
	}
	if code, _, errOut := run(a, "upload", path, "--apply", "--confirm", "prod"); code != exitFailure || !strings.Contains(errOut, "not confirmed") {
		t.Fatalf("wrong confirmation: code %d, stderr %q", code, errOut)
	}
	if fake.Calls("CreateMultipartUpload") != 0 {
		t.Fatal("uploaded without the confirmation")
	}
	if code, out, _ := run(a, "upload", path, "--apply", "--confirm", "dev-sql"); code != 0 || fake.Get(testBucket, "import/sales.bak") == nil {
		t.Fatalf("confirmed: code %d, out %q", code, out)
	}
	// Replacing it asks for the confirmation of the target, still.
	if code, out, _ := run(a, "upload", path, "--overwrite"); code != 0 || !strings.Contains(out, "replacing the object in S3  [destructive]") {
		t.Fatalf("overwrite: code %d, out %q", code, out)
	}
}

func TestUploadFailure(t *testing.T) {
	fake := transfertest.New()
	fake.OnUploadPart = func(context.Context, int32, int) error {
		return transfertest.Error("UploadPart", 403, "AccessDenied")
	}
	a := transferApp(t, testConfig, fake)
	code, out, errOut := run(a, "upload", localFile(t, "sales.bak", "backup"), "--apply")
	if code != exitFailure || !strings.Contains(out, "FAIL  Upload sales.bak") || !strings.Contains(out, "nothing is left in S3") ||
		!strings.Contains(out, "Failed: 0 of 1 file(s) uploaded.") || strings.Contains(errOut, "dialga:") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
	if fake.Uploads() != 0 {
		t.Fatal("parts left in S3")
	}
}

func TestUploadFlagErrors(t *testing.T) {
	a := transferApp(t, testConfig, transfertest.New())
	path := localFile(t, "sales.bak", "backup")
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"upload", path, path, "--key", "x.bak"}, "a key names one file"},
		{[]string{"upload", path, "--key", "x.bak", "--prefix", "p"}, "not both"},
		{[]string{"upload", path, "--concurrency", "0"}, "--concurrency must be between 1 and 32"},
		{[]string{"upload", path, "-t", "nope"}, "unknown target"},
		{[]string{"download", "s3://other-bucket/x.bak"}, "the bucket must be dev-sqlserver-backups"},
	}
	for _, tc := range tests {
		if code, _, errOut := run(a, tc.args...); code != exitFailure || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: code %d, stderr %q", tc.args, code, errOut)
		}
	}
}

func TestDownload(t *testing.T) {
	fake := transfertest.New()
	fake.Put(testBucket, "export/sales.bak", []byte("exported"), types.ChecksumAlgorithmCrc64nvme)
	a := transferApp(t, testConfig, fake)
	dir := t.TempDir()
	code, out, _ := run(a, "download", "sales.bak", dir)
	if code != 0 || !strings.Contains(out, "GET s3://dev-sqlserver-backups/export/sales.bak · 1 range, CRC64NVME verified") {
		t.Fatalf("dry run: code %d, out %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "sales.bak")); err == nil {
		t.Fatal("a dry run wrote the file")
	}
	code, out, _ = run(a, "download", "sales.bak", dir, "--apply")
	got, _ := os.ReadFile(filepath.Join(dir, "sales.bak"))
	if code != 0 || string(got) != "exported" || !strings.Contains(out, "Downloaded 1 file(s).") {
		t.Fatalf("apply: code %d, out %q", code, out)
	}
	// It exists now: rejected without --overwrite.
	code, out, _ = run(a, "download", "sales.bak", dir, "--apply")
	if code != exitCheckFailed || !strings.Contains(out, "already exists") || !strings.Contains(out, "Downloaded 0 file(s); 1 rejected.") {
		t.Fatalf("existing file: code %d, out %q", code, out)
	}
	if code, out, _ := run(a, "download", "sales.bak", dir, "--overwrite", "--apply", "--confirm", "sales.bak"); code != 0 {
		t.Fatalf("overwrite: code %d, out %q", code, out)
	}
}
