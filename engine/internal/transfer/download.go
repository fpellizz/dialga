package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Download gets s3://Bucket/Key into the local file Path with parallel
// ranged GETs, through a temporary file in the same folder that becomes
// Path only once complete and verified.
type Download struct {
	Bucket string
	Key    string
	Path   string
	// Overwrite allows replacing an existing file.
	Overwrite bool
	// ETag, when set, is the object the plan saw: an object that changed
	// since is refused. Every range asks for that ETag (If-Match).
	ETag string
}

// Run downloads the object. When ctx ends, or a range fails for good, the
// temporary file is removed.
func (d Download) Run(ctx context.Context, c Client, o Options) (Result, error) {
	if err := o.validate(); err != nil {
		return Result{}, err
	}
	o = o.withDefaults()
	head, err := headObject(ctx, c, d.Bucket, d.Key)
	if err != nil {
		return Result{}, err
	}
	etag := aws.ToString(head.ETag)
	if d.ETag != "" && etag != d.ETag {
		return Result{}, fmt.Errorf("s3://%s/%s changed since the plan: plan the download again", d.Bucket, d.Key)
	}
	if !d.Overwrite {
		if err := mustNotExist(d.Path); err != nil {
			return Result{}, err
		}
	}
	size := aws.ToInt64(head.ContentLength)
	dir := filepath.Dir(d.Path)
	if err := enoughSpace(o.free, dir, size); err != nil {
		return Result{}, err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(d.Path)+".*.part")
	if err != nil {
		return Result{}, err
	}
	dl := &downloader{
		d: d, c: c, o: o, f: f, etag: etag, sum: objectChecksum(head),
		layout: rangeLayout(size, o.PartSize), pace: &pacer{floor: o.MinTimeout},
	}
	dl.sums = make([]uint64, dl.layout.Parts)
	dl.tr = newTracker(Progress{Path: d.Path, Key: d.Key, Total: size, PartsTotal: dl.layout.Parts}, o)
	defer dl.tr.stop()
	res, err := dl.run(ctx)
	if err == nil {
		err = f.Chmod(fileMode(d.Path, d.Overwrite))
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err == nil {
		err = place(f.Name(), d.Path, d.Overwrite)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return Result{}, fmt.Errorf("download cancelled (%w): nothing was written to %s", context.Cause(ctx), d.Path)
		}
		return Result{}, err
	}
	return res, nil
}

// fileMode is the mode of a downloaded file: the one of the file it
// replaces, or 0644 like any new file (the temporary file is 0600).
func fileMode(path string, overwrite bool) fs.FileMode {
	if overwrite {
		if st, err := os.Stat(path); err == nil {
			return st.Mode().Perm()
		}
	}
	return 0o644
}

// downloader is one run of a Download.
type downloader struct {
	d      Download
	c      Client
	o      Options
	f      *os.File
	etag   string
	sum    objectSum
	layout Layout
	tr     *tracker
	pace   *pacer
	sums   []uint64 // CRC of each range; each worker writes its own
}

func (dl *downloader) run(ctx context.Context) (Result, error) {
	if err := dl.f.Truncate(dl.layout.Size); err != nil {
		return Result{}, err
	}
	todo := make([]int, dl.layout.Parts)
	for i := range todo {
		todo[i] = i + 1
	}
	dl.tr.begin(0, 0)
	if err := parallel(ctx, dl.o.Concurrency, todo, dl.fetch); err != nil {
		return Result{}, err
	}
	if err := dl.f.Sync(); err != nil {
		return Result{}, err
	}
	elapsed, rate := dl.tr.finish()
	res := Result{
		Path: dl.d.Path, Bucket: dl.d.Bucket, Key: dl.d.Key, Size: dl.layout.Size,
		Seconds: elapsed.Seconds(), BytesPerSecond: rate, Note: dl.sum.note,
	}
	if dl.sum.c == nil {
		return res, nil
	}
	var full uint64
	for i, s := range dl.sums {
		if i == 0 {
			full = s
			continue
		}
		_, n := dl.layout.Part(i + 1)
		full = dl.sum.c.combine(full, s, n)
	}
	res.ChecksumAlgorithm, res.Checksum = string(dl.sum.c.name), dl.sum.c.encode(full)
	if res.Checksum != dl.sum.want {
		return Result{}, fmt.Errorf("checksum mismatch: S3 has %s %s, the download %s; the file was not kept",
			dl.sum.c.name, dl.sum.want, res.Checksum)
	}
	res.Verified = true
	return res, nil
}

// fetch downloads range n, retrying what can succeed on another attempt.
func (dl *downloader) fetch(ctx context.Context, n int, buf []byte) error {
	off, size := dl.layout.Part(n)
	for attempt, timeouts := 1, 0; ; attempt++ {
		timeout := dl.pace.timeout(size, timeouts)
		start := dl.o.now()
		sum, err := dl.get(ctx, n, off, size, timeout, buf)
		if err == nil {
			dl.pace.observe(size, dl.o.now().Sub(start))
			dl.sums[n-1] = sum
			dl.tr.partDone(n, size)
			return nil
		}
		dl.tr.drop(n)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isPreconditionFailed(err) {
			return fmt.Errorf("s3://%s/%s changed during the download", dl.d.Bucket, dl.d.Key)
		}
		if errors.Is(err, errAttemptTimeout) {
			timeouts++
			dl.pace.slower()
		}
		if !retryable(err) || attempt >= dl.o.MaxAttempts {
			return fmt.Errorf("range %d of %d, attempt %d: %w", n, dl.layout.Parts, attempt, err)
		}
		if err := dl.o.sleep(ctx, backoff(attempt, dl.o.rand)); err != nil {
			return err
		}
	}
}

// get is one attempt of a ranged GET, written at its offset of the file.
func (dl *downloader) get(ctx context.Context, n int, off, size int64, timeout time.Duration, buf []byte) (uint64, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in := &s3.GetObjectInput{
		Bucket: aws.String(dl.d.Bucket), Key: aws.String(dl.d.Key),
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", off, off+size-1)),
	}
	if dl.etag != "" {
		in.IfMatch = aws.String(dl.etag)
	}
	out, err := dl.c.GetObject(actx, in, noSDKRetry)
	if err != nil {
		return 0, attemptError(ctx, actx, timeout, err)
	}
	defer func() { _ = out.Body.Close() }()
	token := dl.tr.attempt(n)
	w := &rangeWriter{w: io.NewOffsetWriter(dl.f, off), c: dl.sum.c, report: func(pos int64) { dl.tr.set(n, token, pos) }}
	written, err := io.CopyBuffer(w, io.LimitReader(out.Body, size), buf)
	if err != nil {
		return 0, attemptError(ctx, actx, timeout, err)
	}
	if written != size {
		return 0, fmt.Errorf("short read: %d of %d bytes", written, size)
	}
	return w.sum, nil
}

// rangeWriter writes a range at its offset, summing it and reporting the
// bytes so far.
type rangeWriter struct {
	w      *io.OffsetWriter
	c      *crc
	sum    uint64
	n      int64
	report func(int64)
}

func (r *rangeWriter) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	if r.c != nil {
		r.sum = r.c.update(r.sum, p[:n])
	}
	r.n += int64(n)
	r.report(r.n)
	return n, err
}

// objectSum is the checksum S3 keeps for a whole object, when the download
// can verify it.
type objectSum struct {
	c    *crc   // nil: not verifiable, note says why
	want string // base64
	note string
}

func objectChecksum(h *s3.HeadObjectOutput) objectSum {
	whole := h.ChecksumType == "" || h.ChecksumType == types.ChecksumTypeFullObject
	for _, cand := range []struct {
		c     *crc
		value *string
	}{{&crc64NVME, h.ChecksumCRC64NVME}, {&crc32C, h.ChecksumCRC32C}, {&crc32IEEE, h.ChecksumCRC32}} {
		v := aws.ToString(cand.value)
		switch {
		case v == "":
			continue
		case !whole || strings.Contains(v, "-"):
			return objectSum{note: fmt.Sprintf("not verified: the %s checksum of the object is per part, not of the whole object", cand.c.name)}
		}
		return objectSum{c: cand.c, want: v}
	}
	if h.ChecksumSHA256 != nil || h.ChecksumSHA1 != nil {
		return objectSum{note: "not verified: only CRC checksums are checked, the object has a SHA one"}
	}
	return objectSum{note: "not verified: the object has no checksum"}
}

func headObject(ctx context.Context, c Client, bucket, key string) (*s3.HeadObjectOutput, error) {
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	switch {
	case isNotFound(err):
		return nil, fmt.Errorf("s3://%s/%s does not exist", bucket, key)
	case err != nil:
		return nil, fmt.Errorf("read s3://%s/%s: %w", bucket, key, err)
	}
	return head, nil
}

func mustNotExist(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%s already exists", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return err
}

// FreeBytes is the space the user can still write in dir;
// errors.ErrUnsupported where the OS cannot tell.
func FreeBytes(dir string) (uint64, error) { return freeBytes(dir) }

// enoughSpace checks that dir can take size more bytes. Where the free space
// cannot be known (an OS without the call) the check is skipped.
func enoughSpace(free func(string) (uint64, error), dir string, size int64) error {
	avail, err := free(dir)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return nil
	case err != nil:
		return fmt.Errorf("free space in %s: %w", dir, err)
	case avail < uint64(size):
		return fmt.Errorf("not enough free space in %s: %s needed, %s free", dir, FormatBytes(size), FormatBytes(int64(avail)))
	}
	return nil
}

// place moves the finished temporary file to dest. Without overwrite a hard
// link makes it atomic: it fails if dest appeared in the meantime.
func place(tmp, dest string, overwrite bool) error {
	if overwrite {
		return os.Rename(tmp, dest)
	}
	err := os.Link(tmp, dest)
	switch {
	case err == nil:
		_ = os.Remove(tmp) // dest is complete; at worst a hidden .part file stays
		return nil
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%s already exists (it appeared during the download): not replaced, use overwrite", dest)
	}
	// No hard links on this file system: check, then rename.
	if err := mustNotExist(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
