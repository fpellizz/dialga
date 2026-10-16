package discover

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/decisyon/dialga/engine/internal/awsx"
)

// ErrBucketListDenied means the profile lacks s3:ListAllMyBuckets: the
// caller falls back to a typed bucket name.
var ErrBucketListDenied = errors.New("the AWS user cannot list buckets (s3:ListAllMyBuckets): type the bucket name")

// Bucket is an S3 bucket of the account.
type Bucket struct {
	Name      string     `json:"name"`
	Region    string     `json:"region,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

// S3API is the part of the S3 client the bucket listing calls (tests fake it).
type S3API interface {
	s3.ListBucketsAPIClient
}

// Buckets lists the buckets of profile that live in region.
func Buckets(ctx context.Context, profile, region string) ([]Bucket, error) {
	cfg, _, err := awsx.Load(ctx, profile, region, "")
	if err != nil {
		return nil, err
	}
	return RegionBuckets(ctx, s3.NewFromConfig(cfg), region)
}

// RegionBuckets lists the buckets in region (the BucketRegion filter keeps
// the others out), following the pages, sorted by name. A denied listing is
// ErrBucketListDenied.
func RegionBuckets(ctx context.Context, client S3API, region string) ([]Bucket, error) {
	list := []Bucket{}
	pages := s3.NewListBucketsPaginator(client, &s3.ListBucketsInput{BucketRegion: aws.String(region)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			if isAccessDenied(err) {
				return nil, ErrBucketListDenied
			}
			return nil, err
		}
		for _, b := range page.Buckets {
			list = append(list, Bucket{
				Name:      aws.ToString(b.Name),
				Region:    aws.ToString(b.BucketRegion),
				CreatedAt: b.CreationDate,
			})
		}
	}
	slices.SortFunc(list, func(a, b Bucket) int { return strings.Compare(a.Name, b.Name) })
	return list, nil
}

func isAccessDenied(err error) bool {
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.ErrorCode() == "AccessDenied" || ae.ErrorCode() == "AllAccessDisabled"
}
