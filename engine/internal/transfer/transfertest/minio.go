//go:build integration

package transfertest

import (
	"context"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

// MinIOImage is the MinIO the integration tests run: release
// 2025-04-22T22-12-26Z, rebuilt by Bitnami. MinIO no longer publishes its
// community images (quay.io and Docker Hub answer unauthorized).
const MinIOImage = "docker.io/bitnamilegacy/minio:2025.4.22"

// RunMinIO starts MinIO for a test. The Bitnami image runs as a non-root
// user and wraps the server in its own scripts: the binary is started
// directly, on a data directory that user can write.
func RunMinIO(ctx context.Context) (*minio.MinioContainer, error) {
	return minio.Run(ctx, MinIOImage,
		testcontainers.WithEntrypoint("/opt/bitnami/minio/bin/minio"),
		testcontainers.WithCmd("server", "/bitnami/minio/data"),
	)
}
