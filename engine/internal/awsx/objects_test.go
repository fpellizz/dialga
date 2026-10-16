package awsx

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestObjectError(t *testing.T) {
	answer := func(code int) error {
		return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
			Err:      errors.New("raw"),
		}}
	}
	if got := objectError(answer(404)); !errors.Is(got, ErrObjectNotFound) {
		t.Errorf("HTTP 404: %v, want ErrObjectNotFound", got)
	}
	if got := objectError(answer(403)); got == nil || !strings.HasPrefix(got.Error(), "access denied") {
		t.Errorf("HTTP 403: %v", got)
	}
	if got := objectError(answer(301)); got == nil || got.Error() != "the bucket is in another region" {
		t.Errorf("HTTP 301: %v", got)
	}
	plain := errors.New("no network")
	if got := objectError(plain); got != plain {
		t.Errorf("plain error changed: %v", got)
	}
}
