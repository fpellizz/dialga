// Package transfer moves backup files between the workstation and the S3
// bucket of a target, with the requirements of §3.9 of the plan (the limits
// of brows3r turned into rules):
//   - multipart uploads with parts sized on the file (10,000 parts at most,
//     objects up to 5 TiB) and parallel ranged downloads;
//   - per-part retries with exponential backoff and jitter, and a timeout
//     that follows the part size and the observed throughput;
//   - CRC checksums of every part and of the whole object: S3 verifies them
//     when the upload completes, and they are read back afterwards;
//   - a real cancel: an interrupted upload is aborted, so no parts stay in
//     S3 (a pause keeps them for Resume);
//   - nothing is replaced unless asked, neither in S3 nor on disk.
//
// No ACLs and no encryption headers are sent: the default encryption of the
// bucket applies (SSE-S3), which RDS can read.
//
// A transfer is a change, so it is a core.Plan built by PlanUploads or
// PlanDownload: dry-run first, then Execute.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
)

// Client is the part of the S3 API the transfers use. *s3.Client implements
// it; the tests use the in-memory one of package transfertest.
type Client interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	ListParts(context.Context, *s3.ListPartsInput, ...func(*s3.Options)) (*s3.ListPartsOutput, error)
	GetBucketEncryption(context.Context, *s3.GetBucketEncryptionInput, ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error)
}

// Open returns the S3 client of a target, after the account guard.
func Open(ctx context.Context, t config.Target) (Client, error) {
	cfg, _, err := awsx.Load(ctx, t.AWSProfile, t.Region, t.AccountID)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg), nil
}

var (
	// ErrInvalidRequest marks the errors of the request itself (a key, a
	// part size, a flag), as opposed to the failures of S3 or of the disk.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrPaused, as the cause of the context cancellation
	// (context.WithCancelCause), stops an upload without aborting it: the
	// parts stay in S3 and Resume completes it.
	ErrPaused = errors.New("paused")
)

// requestError is an ErrInvalidRequest with its own message.
type requestError struct{ msg string }

func (e requestError) Error() string        { return e.msg }
func (e requestError) Is(target error) bool { return target == ErrInvalidRequest }

func invalidf(format string, args ...any) error {
	return requestError{fmt.Sprintf(format, args...)}
}

// Defaults and limits of Options.
const (
	DefaultConcurrency = 4
	MaxConcurrency     = 32
	DefaultMaxAttempts = 5
	DefaultMinTimeout  = time.Minute
)

// Options tunes a transfer; the zero value is the default.
type Options struct {
	// PartSize of an upload (0: from the file size, see UploadLayout) or the
	// range size of a download (0: DefaultRangeSize).
	PartSize int64
	// Concurrency is how many parts or ranges are in flight (0: 4).
	Concurrency int
	// MaxAttempts per part or range (0: 5).
	MaxAttempts int
	// MinTimeout is the floor of the timeout of one attempt (0: 1 minute).
	MinTimeout time.Duration
	// OnProgress receives the progress at most 4 times a second, and once
	// more at the end. Calls never overlap.
	OnProgress func(Progress)
	// Journal, when set, receives what a later Resume needs (uploads).
	Journal Journal

	// Replaced by the tests.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	rand  func() float64
	free  func(dir string) (uint64, error)
}

func (o Options) validate() error {
	switch {
	case o.Concurrency < 0 || o.Concurrency > MaxConcurrency:
		return invalidf("concurrency must be between 1 and %d", MaxConcurrency)
	case o.MaxAttempts < 0:
		return invalidf("max attempts must be positive")
	}
	return nil
}

func (o Options) withDefaults() Options {
	if o.Concurrency == 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.MinTimeout == 0 {
		o.MinTimeout = DefaultMinTimeout
	}
	if o.OnProgress == nil {
		o.OnProgress = func(Progress) {}
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.sleep == nil {
		o.sleep = sleep
	}
	if o.rand == nil {
		o.rand = rand.Float64
	}
	if o.free == nil {
		o.free = freeBytes
	}
	return o
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Journal receives the state that resuming an upload needs (plan v0.5):
// the upload when it starts (or resumes), every part once S3 has it, and the
// end. err is nil when the object is complete, ErrPaused when the parts were
// kept for Resume, the failure otherwise (the upload was aborted). Calls come
// from several goroutines.
type Journal interface {
	UploadStarted(UploadState)
	PartDone(uploadID string, part PartState)
	UploadEnded(uploadID string, err error)
}

// UploadState identifies a multipart upload and the file it comes from.
type UploadState struct {
	UploadID string
	Bucket   string
	Key      string
	Path     string
	Size     int64
	ModTime  time.Time
	PartSize int64
	Parts    int
	Checksum types.ChecksumAlgorithm
}

// PartState is a part S3 has.
type PartState struct {
	Number   int
	Size     int64
	ETag     string
	Checksum string // base64, as S3 shows it
}

// Result describes a finished transfer.
type Result struct {
	Path   string `json:"path"`
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	// ChecksumAlgorithm and Checksum (base64) of the whole object.
	ChecksumAlgorithm string `json:"checksumAlgorithm,omitempty"`
	Checksum          string `json:"checksum,omitempty"`
	// Verified: the checksum computed here matches the one S3 keeps.
	Verified bool `json:"verified"`
	// Note says why the checksum was not verified.
	Note           string  `json:"note,omitempty"`
	Seconds        float64 `json:"seconds"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
}

// statusCode is the HTTP status of an S3 error, 0 when there was no answer.
func statusCode(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func errorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	var nk *types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nk) || statusCode(err) == 404
}

func isNoSuchUpload(err error) bool {
	var nu *types.NoSuchUpload
	return errors.As(err, &nu) || errorCode(err) == "NoSuchUpload"
}

func isPreconditionFailed(err error) bool {
	return statusCode(err) == 412 || errorCode(err) == "PreconditionFailed"
}

// retryable says whether another attempt can succeed: no answer at all
// (network), throttling, server errors and the 4xx that mean a damaged or
// slow request. Other 4xx (access denied, no such upload...) will not change.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	switch errorCode(err) {
	case "RequestTimeout", "BadDigest", "IncompleteBody", "XAmzContentSHA256Mismatch",
		"SlowDown", "InternalError", "ServiceUnavailable":
		return true
	}
	switch s := statusCode(err); {
	case s == 0, s == 408, s == 429, s >= 500:
		return true
	}
	return false
}

// Backoff between attempts: exponential from 500 ms, capped at 20 s, with
// "equal jitter" (half fixed, half random) so parallel parts spread out.
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 20 * time.Second
)

func backoff(attempt int, rnd func() float64) time.Duration {
	d := backoffMax
	if attempt < 10 {
		d = min(backoffBase<<(attempt-1), backoffMax)
	}
	return d/2 + time.Duration(rnd()*float64(d/2))
}

// pacer sets the timeout of one attempt from the size of the part and the
// throughput of the parts so far: timeoutFactor times the expected duration,
// never less than the floor, doubled after each timeout of the same part.
// Before the first part completes it assumes a slow link.
type pacer struct {
	mu    sync.Mutex
	rate  float64 // bytes per second of one stream (moving average), 0: none yet
	floor time.Duration
}

const (
	assumedRate   = 256 * KiB // bytes per second, until the first part completes
	timeoutFactor = 4
	maxTimeout    = 4 * time.Hour
)

func (p *pacer) timeout(n int64, timeouts int) time.Duration {
	p.mu.Lock()
	rate := p.rate
	p.mu.Unlock()
	if rate <= 0 {
		rate = assumedRate
	}
	d := max(time.Duration(timeoutFactor*float64(n)/rate*float64(time.Second)), p.floor)
	for range timeouts {
		d *= 2
	}
	return min(d, maxTimeout)
}

// observe records a part of n bytes that took d.
func (p *pacer) observe(n int64, d time.Duration) {
	if d <= 0 {
		return
	}
	r := float64(n) / d.Seconds()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rate == 0 {
		p.rate = r
	} else {
		p.rate = 0.7*p.rate + 0.3*r
	}
}

// slower records a timeout: the link is slower than measured.
func (p *pacer) slower() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rate /= 2
}

// noSDKRetry turns off the retries of the SDK for the calls the transfer
// retries itself (parts and ranges), so the attempts and their timeouts are
// the ones counted here.
func noSDKRetry(o *s3.Options) { o.RetryMaxAttempts = 1 }
