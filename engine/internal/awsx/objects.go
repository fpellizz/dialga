package awsx

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ErrObjectNotFound means the bucket has no object with that key.
var ErrObjectNotFound = errors.New("object not found")

// Bucket reads the objects of one bucket: the backups a restore reads and
// the keys an export must not overwrite.
type Bucket struct {
	Client *s3.Client
	Name   string
}

// NewBucket returns the bucket name reached with cfg.
func NewBucket(cfg aws.Config, name string) Bucket {
	return Bucket{Client: s3.NewFromConfig(cfg), Name: name}
}

// Size returns the size in bytes of the object at key, or ErrObjectNotFound.
func (b Bucket) Size(ctx context.Context, key string) (int64, error) {
	out, err := b.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.Name), Key: aws.String(key)})
	if err != nil {
		return 0, objectError(err)
	}
	return aws.ToInt64(out.ContentLength), nil
}

// Keys lists the keys that start with prefix, in the order S3 returns them
// (UTF-8 binary order).
func (b Bucket) Keys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(b.Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.Name), Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, bucketError(err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// objectError turns the HeadObject answers into a readable reason. Without
// s3:ListBucket a missing object answers 403, not 404.
func objectError(err error) error {
	var re *awshttp.ResponseError
	if !errors.As(err, &re) {
		return err
	}
	switch re.HTTPStatusCode() {
	case 404:
		return ErrObjectNotFound
	case 403:
		return errors.New("access denied: the profile cannot read the object (or it does not exist and s3:ListBucket is missing)")
	}
	return bucketError(err)
}
