package awsx

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestLatestMinimum(t *testing.T) {
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	points := []cwtypes.Datapoint{
		{Timestamp: aws.Time(now.Add(-10 * time.Minute)), Minimum: aws.Float64(30)},
		{Timestamp: aws.Time(now), Minimum: aws.Float64(10)},
		{Timestamp: aws.Time(now.Add(-5 * time.Minute)), Minimum: aws.Float64(20)},
		{Timestamp: aws.Time(now.Add(time.Minute))}, // no value: ignored
	}
	if got, err := latestMinimum(points); err != nil || got != 10 {
		t.Fatalf("latestMinimum = %v, %v; want 10", got, err)
	}
	if _, err := latestMinimum(nil); !errors.Is(err, ErrNoDatapoint) {
		t.Fatalf("no datapoints: err %v", err)
	}
}

func TestBucketError(t *testing.T) {
	answer := func(code int) error {
		return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
			Err:      errors.New("raw"),
		}}
	}
	for code, want := range map[int]string{
		404: "bucket not found",
		403: "access denied: the profile cannot read the bucket",
		301: "the bucket is in another region",
	} {
		if got := bucketError(answer(code)); got == nil || got.Error() != want {
			t.Errorf("HTTP %d: %v, want %q", code, got, want)
		}
	}
	if got := bucketError(answer(500)); got == nil || !strings.Contains(got.Error(), "raw") {
		t.Errorf("HTTP 500 should keep the original error, got %v", got)
	}
	if bucketError(nil) != nil {
		t.Error("nil error changed")
	}
}

func TestWrongAccountError(t *testing.T) {
	err := error(&WrongAccountError{Profile: "sales", Got: "111111111111", Want: "222222222222"})
	var wrong *WrongAccountError
	if !errors.As(err, &wrong) || err.Error() != "profile sales points to account 111111111111, expected 222222222222" {
		t.Fatalf("err %v", err)
	}
}
