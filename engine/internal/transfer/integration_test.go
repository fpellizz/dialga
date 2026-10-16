//go:build integration

package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

var (
	minioOnce   sync.Once
	minioCtr    *minio.MinioContainer
	minioClient *s3.Client
	minioErr    error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if minioCtr != nil {
		_ = minioCtr.Terminate(context.Background())
	}
	os.Exit(code)
}

// minioS3 starts MinIO once for the package and returns a client on a new
// empty bucket for the test.
func minioS3(t *testing.T) (*s3.Client, string) {
	t.Helper()
	ctx := context.Background()
	minioOnce.Do(func() {
		minioCtr, minioErr = transfertest.RunMinIO(ctx)
		if minioErr != nil {
			return
		}
		var endpoint string
		if endpoint, minioErr = minioCtr.ConnectionString(ctx); minioErr != nil {
			return
		}
		minioClient = s3.New(s3.Options{
			Region: "us-east-1", BaseEndpoint: aws.String("http://" + endpoint), UsePathStyle: true,
			Credentials: credentials.NewStaticCredentialsProvider(minioCtr.Username, minioCtr.Password, ""),
		})
	})
	if minioErr != nil {
		t.Fatalf("start MinIO: %v", minioErr)
	}
	bucket := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	if _, err := minioClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	return minioClient, bucket
}

func openUploads(t *testing.T, c *s3.Client, bucket string) int {
	t.Helper()
	out, err := c.ListMultipartUploads(context.Background(), &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatal(err)
	}
	return len(out.Uploads)
}

func TestMinIOUploadThenDownload(t *testing.T) {
	c, bucket := minioS3(t)
	path, data := randomFile(t, "sales.bak", 50*MiB+12345)
	o := quick(nil)
	o.PartSize = 5 * MiB
	up, err := Upload{Path: path, Bucket: bucket, Key: "import/sales.bak"}.Run(context.Background(), c, o)
	if err != nil {
		t.Fatal(err)
	}
	if !up.Verified || up.Checksum != crc64NVME.encode(crc64NVME.update(0, data)) {
		t.Fatalf("upload %+v", up)
	}
	head, err := c.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("import/sales.bak"), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil || aws.ToString(head.ChecksumCRC64NVME) != up.Checksum || head.ChecksumType != types.ChecksumTypeFullObject ||
		!strings.HasSuffix(aws.ToString(head.ETag), `-11"`) {
		t.Fatalf("head %+v, %v", head, err)
	}

	dest := filepath.Join(t.TempDir(), "sales.bak")
	o.PartSize = 7 * MiB
	down, err := Download{Bucket: bucket, Key: "import/sales.bak", Path: dest}.Run(context.Background(), c, o)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) || !down.Verified || down.Checksum != up.Checksum {
		t.Fatalf("download %+v, %v", down, err)
	}
}

func TestMinIODownloadOfASinglePut(t *testing.T) {
	c, bucket := minioS3(t)
	data := bytes.Repeat([]byte("dialga "), 300_000)
	// A plain PUT with a CRC32, as other tools upload.
	if _, err := c.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("export/one.bak"), Body: bytes.NewReader(data),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "one.bak")
	o := quick(nil)
	o.PartSize = 512 * KiB
	res, err := Download{Bucket: bucket, Key: "export/one.bak", Path: dest}.Run(context.Background(), c, o)
	got, _ := os.ReadFile(dest)
	if err != nil || !res.Verified || res.ChecksumAlgorithm != "CRC32" || !bytes.Equal(got, data) {
		t.Fatalf("result %+v, err %v", res, err)
	}
}

func TestMinIOCancelLeavesNoUpload(t *testing.T) {
	c, bucket := minioS3(t)
	path, _ := randomFile(t, "sales.bak", 30*MiB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := &journal{onPart: func(p PartState) {
		if p.Number == 2 {
			cancel()
		}
	}}
	o := quick(nil)
	o.PartSize, o.Concurrency, o.Journal = 5*MiB, 1, j
	_, err := Upload{Path: path, Bucket: bucket, Key: "import/sales.bak"}.Run(ctx, c, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if n := openUploads(t, c, bucket); n != 0 {
		t.Fatalf("%d multipart uploads left after the cancel", n)
	}
	if _, err := c.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("import/sales.bak")}); !isNotFound(err) {
		t.Fatalf("object after the cancel: %v", err)
	}
}

func TestMinIOPauseThenResume(t *testing.T) {
	c, bucket := minioS3(t)
	path, data := randomFile(t, "sales.bak", 22*MiB)
	ctx, cancel := context.WithCancelCause(context.Background())
	j := &journal{onPart: func(p PartState) {
		if p.Number == 2 {
			cancel(ErrPaused)
		}
	}}
	o := quick(nil)
	o.PartSize, o.Concurrency, o.Journal = 5*MiB, 1, j
	up := Upload{Path: path, Bucket: bucket, Key: "import/sales.bak"}
	if _, err := up.Run(ctx, c, o); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v", err)
	}
	if n := openUploads(t, c, bucket); n != 1 {
		t.Fatalf("%d multipart uploads after the pause, want 1", n)
	}
	o.Journal = nil
	res, err := up.Resume(context.Background(), c, j.started[0].UploadID, o)
	if err != nil || !res.Verified || res.Checksum != crc64NVME.encode(crc64NVME.update(0, data)) {
		t.Fatalf("resume %+v, %v", res, err)
	}
	if n := openUploads(t, c, bucket); n != 0 {
		t.Fatalf("%d multipart uploads after the resume", n)
	}
}

func TestMinIOExistingObjectIsNotReplaced(t *testing.T) {
	c, bucket := minioS3(t)
	if _, err := c.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("import/sales.bak"), Body: strings.NewReader("original"),
	}); err != nil {
		t.Fatal(err)
	}
	path, _ := randomFile(t, "sales.bak", 6*MiB)
	tgt := testTarget
	tgt.Bucket = bucket
	b, err := PlanUploads(context.Background(), c, "dev-sql", tgt, UploadRequest{Files: []string{path}})
	if err != nil || len(b.Rejected) != 1 || !strings.Contains(b.Rejected[0].Reason, "already in") || len(b.Plan.Actions) != 0 {
		t.Fatalf("plan %+v, %v", b, err)
	}
	// The same refusal when the object appears after the plan: If-None-Match.
	o := quick(nil)
	o.PartSize = 5 * MiB
	_, err = Upload{Path: path, Bucket: bucket, Key: "import/sales.bak"}.Run(context.Background(), c, o)
	if err == nil || !strings.Contains(err.Error(), "appeared during the upload") {
		t.Fatalf("err = %v", err)
	}
	if n := openUploads(t, c, bucket); n != 0 {
		t.Fatalf("%d multipart uploads left", n)
	}
	out, err := c.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("import/sales.bak")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	var got bytes.Buffer
	if _, err := got.ReadFrom(out.Body); err != nil || got.String() != "original" {
		t.Fatalf("the object was replaced: %q, %v", got.String(), err)
	}
}
