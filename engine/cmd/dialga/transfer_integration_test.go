//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/transfer"
)

// TestUploadDownloadOnMinIO runs the commands with the real SDK against
// MinIO, standing in for the bucket of the target.
func TestUploadDownloadOnMinIO(t *testing.T) {
	ctx := context.Background()
	ctr, err := minio.Run(ctx, "quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("http://" + endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(ctr.Username, ctr.Password, ""),
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(testBucket)}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := testApp(t, testConfig)
	a.openS3 = func(context.Context, config.Target) (transfer.Client, error) { return client, nil }

	data := make([]byte, 40<<20+77)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sales.bak")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(a, "upload", path, "--apply")
	if code != 0 || !strings.Contains(out, "OK    Upload sales.bak (40 MiB)") || !strings.Contains(out, "CRC64NVME verified") {
		t.Fatalf("upload: code %d, out %q, stderr %q", code, out, errOut)
	}
	if code, out, _ := run(a, "upload", path, "--apply"); code != exitCheckFailed || !strings.Contains(out, "already in") {
		t.Fatalf("second upload: code %d, out %q", code, out)
	}
	dir := t.TempDir()
	code, out, errOut = run(a, "download", "s3://"+testBucket+"/import/sales.bak", dir, "--apply")
	got, _ := os.ReadFile(filepath.Join(dir, "sales.bak"))
	if code != 0 || !strings.Contains(out, "CRC64NVME verified") || !bytes.Equal(got, data) {
		t.Fatalf("download: code %d, out %q, stderr %q", code, out, errOut)
	}
}
