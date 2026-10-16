package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Upload puts a local file at s3://Bucket/Key with a multipart upload and a
// CRC64NVME checksum of the whole object.
type Upload struct {
	Path   string
	Bucket string
	Key    string
	// Overwrite allows replacing an object. Without it the upload completes
	// only while the key is still free (If-None-Match: *).
	Overwrite bool
	// Size and ModTime, when set, are the file the plan saw: a file that
	// changed since is refused.
	Size    int64
	ModTime time.Time
}

// abortTimeout bounds the AbortMultipartUpload that cleans up after a
// failure or a cancel (it runs after ctx has ended).
const abortTimeout = 30 * time.Second

var errAttemptTimeout = errors.New("no answer within the timeout")

// Run uploads the file. When ctx ends, or a part fails for good, the
// multipart upload is aborted so no parts stay in S3; a cancel whose cause
// is ErrPaused keeps them for Resume.
func (u Upload) Run(ctx context.Context, c Client, o Options) (Result, error) {
	return u.run(ctx, c, "", o)
}

// Resume completes the multipart upload uploadID of the same file: the parts
// S3 already has with the expected size and checksum are kept, the others
// are uploaded again. The part size must be the one of the first run (the
// same Options.PartSize, or 0 both times).
func (u Upload) Resume(ctx context.Context, c Client, uploadID string, o Options) (Result, error) {
	if uploadID == "" {
		return Result{}, invalidf("no upload id to resume")
	}
	return u.run(ctx, c, uploadID, o)
}

// uploader is one run of an Upload.
type uploader struct {
	u      Upload
	c      Client
	o      Options
	f      *os.File
	mod    time.Time
	layout Layout
	id     string
	tr     *tracker
	pace   *pacer

	mu    sync.Mutex
	parts []types.CompletedPart // by part number - 1
	sums  []uint64
	done  []bool
	full  uint64 // CRC64NVME of the whole file, once complete
}

func (u Upload) run(ctx context.Context, c Client, uploadID string, o Options) (Result, error) {
	if err := o.validate(); err != nil {
		return Result{}, err
	}
	o = o.withDefaults()
	f, err := os.Open(u.Path)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	switch {
	case !st.Mode().IsRegular():
		return Result{}, invalidf("%s is not a regular file", u.Path)
	case u.Size != 0 && (st.Size() != u.Size || !st.ModTime().Equal(u.ModTime)):
		return Result{}, fmt.Errorf("%s changed since the plan: plan the upload again", u.Path)
	}
	layout, err := UploadLayout(st.Size(), o.PartSize)
	if err != nil {
		return Result{}, err
	}
	up := &uploader{
		u: u, c: c, o: o, f: f, mod: st.ModTime(), layout: layout, id: uploadID,
		pace:  &pacer{floor: o.MinTimeout},
		parts: make([]types.CompletedPart, layout.Parts),
		sums:  make([]uint64, layout.Parts),
		done:  make([]bool, layout.Parts),
	}
	up.tr = newTracker(Progress{Path: u.Path, Key: u.Key, Total: layout.Size, PartsTotal: layout.Parts}, o)
	if uploadID == "" {
		out, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(u.Bucket), Key: aws.String(u.Key),
			ChecksumAlgorithm: types.ChecksumAlgorithmCrc64nvme, ChecksumType: types.ChecksumTypeFullObject,
			ContentType: aws.String("application/octet-stream"),
		})
		if err != nil {
			return Result{}, fmt.Errorf("start the multipart upload of s3://%s/%s: %w", u.Bucket, u.Key, err)
		}
		up.id = aws.ToString(out.UploadId)
	} else if err := up.listParts(ctx); err != nil {
		return Result{}, err
	}
	if o.Journal != nil {
		o.Journal.UploadStarted(up.state())
	}
	if err := up.upload(ctx); err != nil {
		return Result{}, up.fail(ctx, err)
	}
	if o.Journal != nil {
		o.Journal.UploadEnded(up.id, nil)
	}
	elapsed, rate := up.tr.finish()
	return up.verify(ctx, elapsed, rate)
}

func (up *uploader) state() UploadState {
	return UploadState{
		UploadID: up.id, Bucket: up.u.Bucket, Key: up.u.Key, Path: up.u.Path,
		Size: up.layout.Size, ModTime: up.mod, PartSize: up.layout.PartSize, Parts: up.layout.Parts,
		Checksum: types.ChecksumAlgorithmCrc64nvme,
	}
}

// upload sends the parts S3 does not have yet, then completes the object.
func (up *uploader) upload(ctx context.Context) error {
	var todo []int
	var doneBytes int64
	doneParts := 0
	for n := 1; n <= up.layout.Parts; n++ {
		if up.done[n-1] {
			_, size := up.layout.Part(n)
			doneBytes += size
			doneParts++
		} else {
			todo = append(todo, n)
		}
	}
	up.tr.begin(doneBytes, doneParts)
	if err := parallel(ctx, up.o.Concurrency, todo, up.part); err != nil {
		return err
	}
	return up.complete(ctx)
}

// part uploads part n, retrying what can succeed on another attempt.
func (up *uploader) part(ctx context.Context, n int, buf []byte) error {
	off, size := up.layout.Part(n)
	sum, err := crc64NVME.sumReader(io.NewSectionReader(up.f, off, size), buf)
	if err != nil {
		return fmt.Errorf("read part %d of %s: %w", n, up.u.Path, err)
	}
	checksum := crc64NVME.encode(sum)
	for attempt, timeouts := 1, 0; ; attempt++ {
		timeout := up.pace.timeout(size, timeouts)
		start := up.o.now()
		etag, err := up.send(ctx, n, off, size, checksum, timeout)
		if err == nil {
			up.pace.observe(size, up.o.now().Sub(start))
			up.record(n, size, etag, sum, checksum)
			return nil
		}
		up.tr.set(n, 0)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errAttemptTimeout) {
			timeouts++
			up.pace.slower()
		}
		if !retryable(err) || attempt >= up.o.MaxAttempts {
			return fmt.Errorf("part %d of %d, attempt %d: %w", n, up.layout.Parts, attempt, err)
		}
		if err := up.o.sleep(ctx, backoff(attempt, up.o.rand)); err != nil {
			return err
		}
	}
}

// send is one attempt of UploadPart, with its own timeout. The checksum goes
// in the request: S3 refuses a part whose bytes do not match it.
func (up *uploader) send(ctx context.Context, n int, off, size int64, checksum string, timeout time.Duration) (string, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := &partReader{r: io.NewSectionReader(up.f, off, size), report: func(pos int64) { up.tr.set(n, pos) }}
	out, err := up.c.UploadPart(actx, &s3.UploadPartInput{
		Bucket: aws.String(up.u.Bucket), Key: aws.String(up.u.Key), UploadId: aws.String(up.id),
		PartNumber: aws.Int32(int32(n)), Body: body, ContentLength: aws.Int64(size),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc64nvme, ChecksumCRC64NVME: aws.String(checksum),
	}, noSDKRetry)
	if err != nil {
		return "", attemptError(ctx, actx, timeout, err)
	}
	if got := aws.ToString(out.ChecksumCRC64NVME); got != "" && got != checksum {
		return "", fmt.Errorf("S3 computed CRC64NVME %s, the file has %s", got, checksum)
	}
	return aws.ToString(out.ETag), nil
}

// attemptError tells a timeout of the attempt from the other failures.
func attemptError(ctx, actx context.Context, timeout time.Duration, err error) error {
	if ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w (%s)", errAttemptTimeout, timeout.Round(time.Second))
	}
	return err
}

func (up *uploader) record(n int, size int64, etag string, sum uint64, checksum string) {
	up.mu.Lock()
	up.parts[n-1] = types.CompletedPart{
		PartNumber: aws.Int32(int32(n)), ETag: aws.String(etag), ChecksumCRC64NVME: aws.String(checksum),
	}
	up.sums[n-1], up.done[n-1] = sum, true
	up.mu.Unlock()
	up.tr.partDone(n, size)
	if up.o.Journal != nil {
		up.o.Journal.PartDone(up.id, PartState{Number: n, Size: size, ETag: etag, Checksum: checksum})
	}
}

// complete assembles the parts. S3 checks the CRC64NVME of the whole object,
// combined here from the parts, against its own.
func (up *uploader) complete(ctx context.Context) error {
	full := up.sums[0]
	for i := 1; i < len(up.sums); i++ {
		_, n := up.layout.Part(i + 1)
		full = crc64NVME.combine(full, up.sums[i], n)
	}
	in := &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(up.u.Bucket), Key: aws.String(up.u.Key), UploadId: aws.String(up.id),
		MultipartUpload:   &types.CompletedMultipartUpload{Parts: up.parts},
		ChecksumCRC64NVME: aws.String(crc64NVME.encode(full)), ChecksumType: types.ChecksumTypeFullObject,
		MpuObjectSize: aws.Int64(up.layout.Size),
	}
	if !up.u.Overwrite {
		in.IfNoneMatch = aws.String("*")
	}
	if _, err := up.c.CompleteMultipartUpload(ctx, in); err != nil {
		if isPreconditionFailed(err) {
			return fmt.Errorf("s3://%s/%s appeared during the upload and was not replaced", up.u.Bucket, up.u.Key)
		}
		return fmt.Errorf("complete the multipart upload: %w", err)
	}
	up.full = full
	return nil
}

// verify reads the object back: size and checksum must be the file's.
func (up *uploader) verify(ctx context.Context, elapsed time.Duration, rate float64) (Result, error) {
	want := crc64NVME.encode(up.full)
	res := Result{
		Path: up.u.Path, Bucket: up.u.Bucket, Key: up.u.Key, Size: up.layout.Size,
		ChecksumAlgorithm: string(types.ChecksumAlgorithmCrc64nvme), Checksum: want,
		Seconds: elapsed.Seconds(), BytesPerSecond: rate,
	}
	head, err := up.c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(up.u.Bucket), Key: aws.String(up.u.Key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		return res, fmt.Errorf("uploaded, but reading it back failed: %w", err)
	}
	if got := aws.ToInt64(head.ContentLength); got != up.layout.Size {
		return res, fmt.Errorf("uploaded, but S3 has %d bytes instead of %d", got, up.layout.Size)
	}
	switch got := aws.ToString(head.ChecksumCRC64NVME); got {
	case want:
		res.Verified = true
	case "":
		res.Note = "S3 returned no CRC64NVME checksum"
	default:
		return res, fmt.Errorf("uploaded, but S3 has CRC64NVME %s and the file %s", got, want)
	}
	return res, nil
}

// fail cleans up after err: the multipart upload is aborted, with a context
// of its own since ctx may be over. A pause keeps it.
func (up *uploader) fail(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrPaused) {
		if up.o.Journal != nil {
			up.o.Journal.UploadEnded(up.id, ErrPaused)
		}
		return fmt.Errorf("upload of s3://%s/%s paused with %d of %d parts in S3 (upload id %s): %w",
			up.u.Bucket, up.u.Key, up.doneParts(), up.layout.Parts, up.id, ErrPaused)
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	_, aerr := up.c.AbortMultipartUpload(actx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(up.u.Bucket), Key: aws.String(up.u.Key), UploadId: aws.String(up.id),
	})
	if up.o.Journal != nil {
		up.o.Journal.UploadEnded(up.id, err)
	}
	if aerr != nil && !isNoSuchUpload(aerr) {
		return fmt.Errorf("%w; aborting the multipart upload failed too (%v): remove it with "+
			"aws s3api abort-multipart-upload --bucket %s --key %s --upload-id %s", err, aerr, up.u.Bucket, up.u.Key, up.id)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("upload cancelled (%w): the multipart upload was aborted, nothing is left in S3", context.Cause(ctx))
	}
	return fmt.Errorf("%w (the multipart upload was aborted, nothing is left in S3)", err)
}

func (up *uploader) doneParts() int {
	up.mu.Lock()
	defer up.mu.Unlock()
	n := 0
	for _, d := range up.done {
		if d {
			n++
		}
	}
	return n
}

// listParts reads the parts of the upload being resumed and keeps those
// whose size and checksum match the file.
func (up *uploader) listParts(ctx context.Context) error {
	buf := make([]byte, 256*KiB)
	var marker *string
	for {
		out, err := up.c.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(up.u.Bucket), Key: aws.String(up.u.Key), UploadId: aws.String(up.id),
			PartNumberMarker: marker,
		})
		if isNoSuchUpload(err) {
			return fmt.Errorf("upload %s is no longer in S3 (completed or aborted): upload the file again", up.id)
		}
		if err != nil {
			return fmt.Errorf("list the parts of upload %s: %w", up.id, err)
		}
		for _, p := range out.Parts {
			n := int(aws.ToInt32(p.PartNumber))
			if n < 1 || n > up.layout.Parts {
				continue // not of this layout: S3 drops it on completion
			}
			off, size := up.layout.Part(n)
			if aws.ToInt64(p.Size) != size || aws.ToString(p.ChecksumCRC64NVME) == "" {
				continue // uploaded again
			}
			sum, err := crc64NVME.sumReader(io.NewSectionReader(up.f, off, size), buf)
			if err != nil {
				return fmt.Errorf("read part %d of %s: %w", n, up.u.Path, err)
			}
			if checksum := crc64NVME.encode(sum); checksum == aws.ToString(p.ChecksumCRC64NVME) {
				up.parts[n-1] = types.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag, ChecksumCRC64NVME: p.ChecksumCRC64NVME}
				up.sums[n-1], up.done[n-1] = sum, true
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		marker = out.NextPartNumberMarker
	}
}

// partReader reports how far the SDK has read a part. Seeking back to the
// start (to retry, or after hashing the payload over plain HTTP) starts the
// count again; Len spares the SDK a seek to the end to learn the length.
type partReader struct {
	r      *io.SectionReader
	pos    int64
	report func(int64)
}

func (p *partReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.pos += int64(n)
	p.report(p.pos)
	return n, err
}

func (p *partReader) Seek(offset int64, whence int) (int64, error) {
	pos, err := p.r.Seek(offset, whence)
	if err == nil {
		p.pos = pos
		if whence == io.SeekStart {
			p.report(pos)
		}
	}
	return pos, err
}

func (p *partReader) Len() int { return int(p.r.Size() - p.pos) }

// parallel runs work for every item of todo on up to workers goroutines,
// each with its own buffer. The first error cancels the others and is the
// one returned.
func parallel(ctx context.Context, workers int, todo []int, work func(context.Context, int, []byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)
	for range min(workers, len(todo)) {
		wg.Go(func() {
			buf := make([]byte, 256*KiB)
			for n := range jobs {
				if err := work(ctx, n, buf); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		})
	}
feed:
	for _, n := range todo {
		select {
		case jobs <- n:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
