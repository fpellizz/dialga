package transfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

// randomObject stores size random bytes at key with a whole-object checksum.
func randomObject(t *testing.T, fake *transfertest.S3, key string, size int, algo types.ChecksumAlgorithm) []byte {
	t.Helper()
	path, data := randomFile(t, "source", size)
	_ = os.Remove(path)
	fake.Put(testBucket, key, data, algo)
	return data
}

// onlyFile fails unless dir holds just name: no temporary file left.
func onlyFile(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if (name == "" && len(names) != 0) || (name != "" && (len(names) != 1 || names[0] != name)) {
		t.Fatalf("files in %s: %v, want only %q", dir, names, name)
	}
}

func TestDownloadRanges(t *testing.T) {
	tests := []struct {
		name string
		algo types.ChecksumAlgorithm
		size int
	}{
		{"CRC64NVME", types.ChecksumAlgorithmCrc64nvme, 5*MiB + 123},
		{"CRC32", types.ChecksumAlgorithmCrc32, 3 * MiB},
		{"CRC32C", types.ChecksumAlgorithmCrc32c, MiB + 1},
		{"empty object", types.ChecksumAlgorithmCrc64nvme, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := transfertest.New()
			data := randomObject(t, fake, "export/sales.bak", tc.size, tc.algo)
			dir := t.TempDir()
			o := quick(nil)
			o.PartSize, o.Concurrency = 512*KiB, 3
			var last Progress
			o.OnProgress = func(p Progress) { last = p }
			res, err := Download{Bucket: testBucket, Key: "export/sales.bak", Path: filepath.Join(dir, "sales.bak")}.Run(context.Background(), fake, o)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "sales.bak"))
			if !bytes.Equal(got, data) || !res.Verified || res.ChecksumAlgorithm != string(tc.algo) ||
				res.Checksum != transfertest.Checksum(tc.algo, data) {
				t.Fatalf("result %+v", res)
			}
			if last.Done != int64(tc.size) || last.Parts != last.PartsTotal || last.PartsTotal != (tc.size+512*KiB-1)/(512*KiB) {
				t.Fatalf("last progress %+v", last)
			}
			onlyFile(t, dir, "sales.bak")
		})
	}
}

func TestDownloadUnverifiableChecksums(t *testing.T) {
	tests := []struct {
		name string
		obj  *transfertest.Object
		want string
	}{
		{"composite", &transfertest.Object{Data: []byte("abc"), ETag: `"e-3"`, ChecksumType: types.ChecksumTypeComposite,
			Checksums: map[types.ChecksumAlgorithm]string{types.ChecksumAlgorithmCrc32c: "AAAAAA==-3"}}, "per part"},
		{"SHA-256", &transfertest.Object{Data: []byte("abc"), ETag: `"e"`,
			Checksums: map[types.ChecksumAlgorithm]string{types.ChecksumAlgorithmSha256: "x"}}, "SHA"},
		{"none", &transfertest.Object{Data: []byte("abc"), ETag: `"e"`}, "no checksum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := transfertest.New()
			fake.PutObject(testBucket, "k", tc.obj)
			dest := filepath.Join(t.TempDir(), "k")
			res, err := Download{Bucket: testBucket, Key: "k", Path: dest}.Run(context.Background(), fake, quick(nil))
			got, _ := os.ReadFile(dest)
			if err != nil || res.Verified || !strings.Contains(res.Note, tc.want) || string(got) != "abc" {
				t.Fatalf("result %+v, err %v", res, err)
			}
		})
	}
}

func TestDownloadChecksumMismatchKeepsNothing(t *testing.T) {
	fake := transfertest.New()
	fake.PutObject(testBucket, "k", &transfertest.Object{Data: []byte("damaged"), ETag: `"e"`,
		Checksums: map[types.ChecksumAlgorithm]string{types.ChecksumAlgorithmCrc64nvme: transfertest.Checksum(types.ChecksumAlgorithmCrc64nvme, []byte("original"))}})
	dir := t.TempDir()
	_, err := Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k")}.Run(context.Background(), fake, quick(nil))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	onlyFile(t, dir, "")
}

func TestDownloadRetriesARange(t *testing.T) {
	fake := transfertest.New()
	data := randomObject(t, fake, "k", 2*MiB, types.ChecksumAlgorithmCrc64nvme)
	fake.OnGetObject = func(_ context.Context, rng string, attempt int) error {
		if rng == "bytes=1048576-2097151" && attempt == 1 {
			return transfertest.Error("GetObject", 503, "SlowDown")
		}
		return nil
	}
	s := &sleeps{}
	o := quick(s)
	o.PartSize = MiB
	dest := filepath.Join(t.TempDir(), "k")
	res, err := Download{Bucket: testBucket, Key: "k", Path: dest}.Run(context.Background(), fake, o)
	got, _ := os.ReadFile(dest)
	if err != nil || !res.Verified || !bytes.Equal(got, data) || fake.Calls("GetObject") != 3 || len(s.d) != 1 {
		t.Fatalf("result %+v, err %v, gets %d, backoffs %v", res, err, fake.Calls("GetObject"), s.d)
	}
}

func TestDownloadNeverReplacesUnlessAsked(t *testing.T) {
	fake := transfertest.New()
	data := randomObject(t, fake, "k", KiB, types.ChecksumAlgorithmCrc64nvme)
	dir := t.TempDir()
	dest := filepath.Join(dir, "k")
	if err := os.WriteFile(dest, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Download{Bucket: testBucket, Key: "k", Path: dest}.Run(context.Background(), fake, quick(nil))
	if got, _ := os.ReadFile(dest); err == nil || !strings.Contains(err.Error(), "already exists") || string(got) != "mine" {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Download{Bucket: testBucket, Key: "k", Path: dest, Overwrite: true}).Run(context.Background(), fake, quick(nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("overwrite did not replace the file")
	}
	onlyFile(t, dir, "k")
}

func TestDownloadObjectChanged(t *testing.T) {
	fake := transfertest.New()
	randomObject(t, fake, "k", 2*MiB, types.ChecksumAlgorithmCrc64nvme)
	dir := t.TempDir()
	_, err := Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k"), ETag: `"seen-by-the-plan"`}.Run(context.Background(), fake, quick(nil))
	if err == nil || !strings.Contains(err.Error(), "changed since the plan") {
		t.Fatalf("err = %v", err)
	}
	// Replaced while the ranges are read: If-Match stops the download.
	fake.OnGetObject = func(_ context.Context, rng string, _ int) error {
		if rng != "bytes=0-1048575" {
			fake.Put(testBucket, "k", []byte("new version"), "")
		}
		return nil
	}
	o := quick(nil)
	o.PartSize, o.Concurrency = MiB, 1
	_, err = Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k")}.Run(context.Background(), fake, o)
	if err == nil || !strings.Contains(err.Error(), "changed during the download") {
		t.Fatalf("err = %v", err)
	}
	onlyFile(t, dir, "")
}

func TestDownloadChecksFreeSpace(t *testing.T) {
	fake := transfertest.New()
	randomObject(t, fake, "k", 2*KiB, types.ChecksumAlgorithmCrc64nvme)
	dir := t.TempDir()
	o := quick(nil)
	o.free = func(string) (uint64, error) { return KiB, nil }
	_, err := Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k")}.Run(context.Background(), fake, o)
	if err == nil || !strings.Contains(err.Error(), "not enough free space") || fake.Calls("GetObject") != 0 {
		t.Fatalf("err = %v", err)
	}
	// An OS that cannot tell: the check is skipped.
	o.free = func(string) (uint64, error) { return 0, errors.ErrUnsupported }
	if _, err := (Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k")}).Run(context.Background(), fake, o); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCancelLeavesNothing(t *testing.T) {
	fake := transfertest.New()
	randomObject(t, fake, "k", 3*MiB, types.ChecksumAlgorithmCrc64nvme)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.OnGetObject = func(ctx context.Context, rng string, _ int) error {
		if rng != "bytes=0-1048575" {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	o := quick(nil)
	o.PartSize, o.Concurrency = MiB, 1
	dir := t.TempDir()
	_, err := Download{Bucket: testBucket, Key: "k", Path: filepath.Join(dir, "k")}.Run(ctx, fake, o)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("err = %v", err)
	}
	onlyFile(t, dir, "")
}

func TestDownloadMissingObject(t *testing.T) {
	_, err := Download{Bucket: testBucket, Key: "nope", Path: filepath.Join(t.TempDir(), "x")}.Run(context.Background(), transfertest.New(), quick(nil))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadFileMode(t *testing.T) {
	fake := transfertest.New()
	randomObject(t, fake, "k", KiB, types.ChecksumAlgorithmCrc64nvme)
	dir := t.TempDir()
	dest := filepath.Join(dir, "k")
	if _, err := (Download{Bucket: testBucket, Key: "k", Path: dest}).Run(context.Background(), fake, quick(nil)); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode %v, want 0644", st.Mode().Perm())
	}
	// A replaced file keeps its mode.
	if err := os.Chmod(dest, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Download{Bucket: testBucket, Key: "k", Path: dest, Overwrite: true}).Run(context.Background(), fake, quick(nil)); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o640 {
		t.Fatalf("replaced file mode %v, want 0640", st.Mode().Perm())
	}
}

func TestDownloadDoesNotReplaceAFileCreatedMeanwhile(t *testing.T) {
	fake := transfertest.New()
	randomObject(t, fake, "k", KiB, types.ChecksumAlgorithmCrc64nvme)
	dest := filepath.Join(t.TempDir(), "k")
	fake.OnGetObject = func(context.Context, string, int) error {
		// Created after the pre-flight, before the final placement.
		return os.WriteFile(dest, []byte("mine"), 0o600)
	}
	_, err := Download{Bucket: testBucket, Key: "k", Path: dest}.Run(context.Background(), fake, quick(nil))
	if got, _ := os.ReadFile(dest); err == nil || !strings.Contains(err.Error(), "already exists") || string(got) != "mine" {
		t.Fatalf("err = %v, file %q", err, got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "*.part*")); len(left) > 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
