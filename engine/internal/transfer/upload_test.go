package transfer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/transfer/transfertest"
)

const testBucket = "dev-sqlserver-backups"

// randomFile writes size random bytes to a new file.
func randomFile(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size+8)
	rnd := rand.New(rand.NewPCG(uint64(size), 7))
	for i := 0; i < size; i += 8 {
		binary.LittleEndian.PutUint64(data[i:], rnd.Uint64())
	}
	data = data[:size]
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// sleeps records the backoffs instead of waiting.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

// quick returns options that never wait, with the jitter in the middle.
func quick(s *sleeps) Options {
	if s == nil {
		s = &sleeps{}
	}
	return Options{rand: func() float64 { return 0.5 }, sleep: s.sleep}
}

// journal records the calls of the Journal.
type journal struct {
	mu      sync.Mutex
	started []UploadState
	parts   []PartState
	ended   []error
	onPart  func(PartState)
}

func (j *journal) UploadStarted(s UploadState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.started = append(j.started, s)
}

func (j *journal) PartDone(_ string, p PartState) {
	j.mu.Lock()
	j.parts = append(j.parts, p)
	j.mu.Unlock()
	if j.onPart != nil {
		j.onPart(p)
	}
}

func (j *journal) UploadEnded(_ string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ended = append(j.ended, err)
}

func TestUploadMultipart(t *testing.T) {
	path, data := randomFile(t, "sales.bak", 12*MiB+321)
	fake := transfertest.New()
	j := &journal{}
	var last Progress
	o := quick(nil)
	o.PartSize, o.Concurrency, o.Journal = 5*MiB, 2, j
	o.OnProgress = func(p Progress) { last = p }
	res, err := Upload{Path: path, Bucket: testBucket, Key: "import/sales.bak"}.Run(context.Background(), fake, o)
	if err != nil {
		t.Fatal(err)
	}
	want := transfertest.Checksum(types.ChecksumAlgorithmCrc64nvme, data)
	if !res.Verified || res.Checksum != want || res.ChecksumAlgorithm != "CRC64NVME" || res.Size != int64(len(data)) {
		t.Fatalf("result %+v, want checksum %s", res, want)
	}
	obj := fake.Get(testBucket, "import/sales.bak")
	if obj == nil || !bytes.Equal(obj.Data, data) || !strings.HasSuffix(obj.ETag, `-3"`) {
		t.Fatal("the object in S3 is not the file in 3 parts")
	}
	if last.Done != last.Total || last.Parts != 3 || last.PartsTotal != 3 || last.ETASeconds != 0 {
		t.Fatalf("last progress %+v", last)
	}
	if len(j.started) != 1 || j.started[0].Parts != 3 || j.started[0].PartSize != 5*MiB || len(j.parts) != 3 ||
		len(j.ended) != 1 || j.ended[0] != nil || fake.Uploads() != 0 {
		t.Fatalf("journal %+v, open uploads %d", j, fake.Uploads())
	}
}

func TestUploadSmallFileIsOnePart(t *testing.T) {
	path, data := randomFile(t, "tiny.bak", 3*KiB)
	fake := transfertest.New()
	res, err := Upload{Path: path, Bucket: testBucket, Key: "tiny.bak"}.Run(context.Background(), fake, quick(nil))
	if err != nil || !res.Verified || fake.Calls("UploadPart") != 1 || !bytes.Equal(fake.Get(testBucket, "tiny.bak").Data, data) {
		t.Fatalf("result %+v, err %v, parts %d", res, err, fake.Calls("UploadPart"))
	}
}

func TestUploadRetriesWithBackoff(t *testing.T) {
	path, data := randomFile(t, "sales.bak", 12*MiB)
	fake := transfertest.New()
	fake.OnUploadPart = func(_ context.Context, part int32, attempt int) error {
		if part == 2 && attempt <= 2 {
			return transfertest.Error("UploadPart", 500, "InternalError")
		}
		return nil
	}
	s := &sleeps{}
	o := quick(s)
	o.PartSize = 5 * MiB
	res, err := Upload{Path: path, Bucket: testBucket, Key: "sales.bak"}.Run(context.Background(), fake, o)
	if err != nil || !res.Verified || !bytes.Equal(fake.Get(testBucket, "sales.bak").Data, data) {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if n := fake.Calls("UploadPart"); n != 5 {
		t.Fatalf("%d UploadPart calls, want 3 parts + 2 retries", n)
	}
	// Equal jitter with rand 0.5: 3/4 of 500 ms, then of 1 s.
	if len(s.d) != 2 || s.d[0] != 375*time.Millisecond || s.d[1] != 750*time.Millisecond {
		t.Fatalf("backoffs %v", s.d)
	}
}

func TestUploadFailuresAbort(t *testing.T) {
	tests := []struct {
		name         string
		fail         error
		wantAttempts int
		wantErr      string
	}{
		{"access denied is not retried", transfertest.Error("UploadPart", 403, "AccessDenied"), 1, "attempt 1"},
		{"server errors until the last attempt", transfertest.Error("UploadPart", 503, "SlowDown"), 3, "attempt 3"},
		{"a vanished upload is not retried", transfertest.Error("UploadPart", 404, "NoSuchUpload"), 1, "attempt 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := randomFile(t, "sales.bak", 2*KiB)
			fake := transfertest.New()
			fake.OnUploadPart = func(context.Context, int32, int) error { return tc.fail }
			o := quick(nil)
			o.MaxAttempts = 3
			_, err := Upload{Path: path, Bucket: testBucket, Key: "sales.bak"}.Run(context.Background(), fake, o)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "aborted") {
				t.Fatalf("err = %v", err)
			}
			if fake.Calls("UploadPart") != tc.wantAttempts || fake.Calls("AbortMultipartUpload") != 1 ||
				fake.Calls("CompleteMultipartUpload") != 0 || fake.Uploads() != 0 {
				t.Fatalf("parts %d, aborts %d, open uploads %d", fake.Calls("UploadPart"),
					fake.Calls("AbortMultipartUpload"), fake.Uploads())
			}
		})
	}
}

func TestUploadTimeoutIsRetriedWithMoreTime(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", KiB)
	fake := transfertest.New()
	var mu sync.Mutex
	var deadlines []time.Duration
	fake.OnUploadPart = func(ctx context.Context, _ int32, attempt int) error {
		d, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, time.Until(d))
		mu.Unlock()
		if attempt == 1 {
			<-ctx.Done() // no answer
			return ctx.Err()
		}
		return nil
	}
	o := quick(nil)
	o.MinTimeout = 100 * time.Millisecond
	res, err := Upload{Path: path, Bucket: testBucket, Key: "sales.bak"}.Run(context.Background(), fake, o)
	if err != nil || !res.Verified {
		t.Fatalf("result %+v, err %v", res, err)
	}
	// The floor first, then twice as long after the timeout.
	if len(deadlines) != 2 || deadlines[0] > 100*time.Millisecond || deadlines[1] <= 100*time.Millisecond || deadlines[1] > 200*time.Millisecond {
		t.Fatalf("attempt deadlines %v", deadlines)
	}
}

func TestUploadCancelAborts(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", 12*MiB)
	fake := transfertest.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.OnUploadPart = func(ctx context.Context, part int32, _ int) error {
		if part == 2 {
			cancel() // Ctrl-C while part 2 is in flight
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	o := quick(nil)
	o.PartSize, o.Concurrency = 5*MiB, 1
	_, err := Upload{Path: path, Bucket: testBucket, Key: "sales.bak"}.Run(ctx, fake, o)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing is left in S3") {
		t.Fatalf("err = %v", err)
	}
	if fake.Calls("AbortMultipartUpload") != 1 || fake.Uploads() != 0 || fake.Get(testBucket, "sales.bak") != nil {
		t.Fatalf("aborts %d, open uploads %d", fake.Calls("AbortMultipartUpload"), fake.Uploads())
	}
}

func TestUploadPauseThenResume(t *testing.T) {
	path, data := randomFile(t, "sales.bak", 20*MiB)
	fake := transfertest.New()
	ctx, cancel := context.WithCancelCause(context.Background())
	j := &journal{}
	j.onPart = func(p PartState) {
		if p.Number == 2 {
			cancel(ErrPaused)
		}
	}
	o := quick(nil)
	o.PartSize, o.Concurrency, o.Journal = 5*MiB, 1, j
	up := Upload{Path: path, Bucket: testBucket, Key: "sales.bak"}
	_, err := up.Run(ctx, fake, o)
	if !errors.Is(err, ErrPaused) || fake.Calls("AbortMultipartUpload") != 0 || fake.Uploads() != 1 {
		t.Fatalf("err %v, aborts %d, open uploads %d", err, fake.Calls("AbortMultipartUpload"), fake.Uploads())
	}
	if len(j.ended) != 1 || !errors.Is(j.ended[0], ErrPaused) {
		t.Fatalf("journal end %v", j.ended)
	}
	id := j.started[0].UploadID
	sent := fake.Calls("UploadPart")

	o.Journal = nil
	res, err := up.Resume(context.Background(), fake, id, o)
	if err != nil || !res.Verified || !bytes.Equal(fake.Get(testBucket, "sales.bak").Data, data) {
		t.Fatalf("resume: result %+v, err %v", res, err)
	}
	if again := fake.Calls("UploadPart") - sent; again != 2 {
		t.Fatalf("resume sent %d parts, want the 2 missing", again)
	}
	if _, err := up.Resume(context.Background(), fake, id, o); err == nil || !strings.Contains(err.Error(), "no longer in S3") {
		t.Fatalf("resume of a completed upload: %v", err)
	}
}

func TestUploadRefusals(t *testing.T) {
	path, _ := randomFile(t, "sales.bak", 2*KiB)
	st, _ := os.Stat(path)

	fake := transfertest.New()
	_, err := Upload{Path: path, Bucket: testBucket, Key: "k", Size: st.Size() + 1, ModTime: st.ModTime()}.Run(context.Background(), fake, quick(nil))
	if err == nil || !strings.Contains(err.Error(), "changed since the plan") || fake.Calls("CreateMultipartUpload") != 0 {
		t.Fatalf("changed file: %v", err)
	}

	// The key was free at plan time; an object put there meanwhile stays.
	fake.Put(testBucket, "k", []byte("someone else"), "")
	_, err = Upload{Path: path, Bucket: testBucket, Key: "k"}.Run(context.Background(), fake, quick(nil))
	if err == nil || !strings.Contains(err.Error(), "appeared during the upload") || fake.Uploads() != 0 ||
		string(fake.Get(testBucket, "k").Data) != "someone else" {
		t.Fatalf("existing object: %v", err)
	}
	if _, err := (Upload{Path: path, Bucket: testBucket, Key: "k", Overwrite: true}).Run(context.Background(), fake, quick(nil)); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	if _, err := (Upload{Path: filepath.Dir(path), Bucket: testBucket, Key: "d"}).Run(context.Background(), fake, quick(nil)); err == nil {
		t.Fatal("uploaded a folder")
	}
	if _, err := (Upload{Path: path}).Resume(context.Background(), fake, "", quick(nil)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("resume without id: %v", err)
	}
	if _, err := (Upload{Path: path}).Run(context.Background(), fake, Options{Concurrency: 99}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("concurrency 99: %v", err)
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempt  int
		min, max time.Duration
	}{
		{1, 250 * time.Millisecond, 500 * time.Millisecond},
		{3, time.Second, 2 * time.Second},
		{6, 8 * time.Second, 16 * time.Second},
		{7, 10 * time.Second, 20 * time.Second}, // capped
		{60, 10 * time.Second, 20 * time.Second},
	}
	for _, tc := range tests {
		lo, hi := backoff(tc.attempt, func() float64 { return 0 }), backoff(tc.attempt, func() float64 { return 1 })
		if lo != tc.min || hi != tc.max {
			t.Errorf("attempt %d: %s..%s, want %s..%s", tc.attempt, lo, hi, tc.min, tc.max)
		}
	}
}

func TestPacerTimeout(t *testing.T) {
	p := &pacer{floor: time.Minute}
	// Nothing measured yet: a slow link of 256 KiB/s is assumed.
	if got := p.timeout(16*MiB, 0); got != 4*64*time.Second {
		t.Fatalf("first timeout of 16 MiB = %s", got)
	}
	if got := p.timeout(MiB, 0); got != time.Minute {
		t.Fatalf("small part = %s, want the floor", got)
	}
	p.observe(64*MiB, 2*time.Second) // 32 MiB/s
	if got := p.timeout(64*MiB, 0); got != time.Minute {
		t.Fatalf("fast link = %s, want the floor", got)
	}
	if got := p.timeout(512*MiB, 0); got != 64*time.Second {
		t.Fatalf("512 MiB at 32 MiB/s = %s, want 4 x 16s", got)
	}
	if got := p.timeout(512*MiB, 2); got != 256*time.Second {
		t.Fatalf("after two timeouts = %s", got)
	}
	p.slower()
	if got := p.timeout(512*MiB, 0); got != 128*time.Second {
		t.Fatalf("after a timeout of another part = %s", got)
	}
	p.observe(52*MiB, time.Second) // the moving average goes 30% of the way: 16 + 0.3 x 36
	if math.Abs(p.rate-26.8*MiB) > 1 {
		t.Fatalf("rate %v", p.rate)
	}
	if got := (&pacer{}).timeout(5*GiB, 0); got != maxTimeout {
		t.Fatalf("huge part = %s, want the cap", got)
	}
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("connection reset by peer"), true},
		{errAttemptTimeout, true},
		{context.Canceled, false},
		{transfertest.Error("UploadPart", 500, "InternalError"), true},
		{transfertest.Error("UploadPart", 503, "SlowDown"), true},
		{transfertest.Error("UploadPart", 429, "TooManyRequests"), true},
		{transfertest.Error("UploadPart", 400, "RequestTimeout"), true},
		{transfertest.Error("UploadPart", 400, "BadDigest"), true},
		{transfertest.Error("UploadPart", 400, "InvalidArgument"), false},
		{transfertest.Error("UploadPart", 403, "AccessDenied"), false},
		{transfertest.Error("UploadPart", 404, "NoSuchUpload"), false},
		{transfertest.Error("GetObject", 412, "PreconditionFailed"), false},
	}
	for _, tc := range tests {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%v) = %v", tc.err, got)
		}
	}
}
