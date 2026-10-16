package transfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/core"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// Item is one file a transfer plan moves.
type Item struct {
	// Step is the id of its plan action.
	Step     string `json:"step"`
	Path     string `json:"path"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	PartSize int64  `json:"partSize"`
	Parts    int    `json:"parts"`
	// Checksum is the algorithm verified at the end; absent when the object
	// to download has none that can be checked.
	Checksum string `json:"checksum,omitempty"`
	// Replaces: an existing object (upload) or file (download) is replaced.
	Replaces bool `json:"replaces"`
}

// Rejection is a file the plan refuses, with the reason: nothing is ever
// skipped silently.
type Rejection struct {
	Path   string `json:"path,omitempty"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// Batch is a transfer plan: the core.Plan to preview and execute (one action
// per file), the files it moves and the ones it refuses.
type Batch struct {
	Plan core.Plan `json:"plan"`
	// Confirmation is the text Execute needs retyped, "" for none.
	Confirmation string      `json:"confirmation"`
	Items        []Item      `json:"items"`
	Rejected     []Rejection `json:"rejected"`
	Warnings     []string    `json:"warnings,omitempty"`

	mu      sync.Mutex
	results []Result
}

func newBatch(operation, target string, t config.Target) *Batch {
	return &Batch{
		Plan:  core.Plan{Operation: operation, Target: target, Instance: t.Instance, Protected: t.Protected, Actions: []core.Action{}},
		Items: []Item{}, Rejected: []Rejection{},
	}
}

// Execute runs the plan (core.Plan.Execute): the rejected files are not in it.
func (b *Batch) Execute(ctx context.Context, confirmed string, emit func(core.Event)) error {
	return b.Plan.Execute(ctx, confirmed, emit)
}

// Results are the transfers completed so far, in plan order.
func (b *Batch) Results() []Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Result{}, b.results...)
}

// action adds the plan action of item; run does the transfer with the
// options of the request, whose progress then carries the step id.
func (b *Batch) action(item Item, title string, risk core.Risk, statement string, o Options,
	run func(context.Context, Options) (Result, error)) {
	if next := o.OnProgress; next != nil {
		o.OnProgress = func(p Progress) {
			p.Step = item.Step
			next(p)
		}
	}
	b.Items = append(b.Items, item)
	b.Plan.Actions = append(b.Plan.Actions, core.Action{
		ID: item.Step, Title: title, Kind: core.KindAWS, Risk: risk, Statement: statement,
		Run: func(ctx context.Context) (string, error) {
			res, err := run(ctx, o)
			if err != nil {
				return "", err
			}
			b.mu.Lock()
			b.results = append(b.results, res)
			b.mu.Unlock()
			return res.summary(), nil
		},
	})
}

func (r Result) summary() string {
	d := time.Duration(r.Seconds * float64(time.Second))
	if d >= time.Second {
		d = d.Round(time.Second)
	} else {
		d = d.Round(time.Millisecond)
	}
	s := fmt.Sprintf("%s in %s (%s/s)", FormatBytes(r.Size), d, FormatBytes(int64(r.BytesPerSecond)))
	switch {
	case r.Verified:
		s += ", " + r.ChecksumAlgorithm + " verified"
	case r.Note != "":
		s += ", " + r.Note
	}
	return s
}

// UploadRequest asks to upload local files to the bucket of a target.
type UploadRequest struct {
	Files []string
	// Key names the object; only with one file.
	Key string
	// Prefix replaces the import prefix of the target ("" is the root of
	// the bucket); nil keeps it. The key is the prefix and the file name.
	Prefix *string
	// Overwrite allows replacing the objects that exist now.
	Overwrite bool
	Options   Options
}

// PlanUploads checks the files and the keys and builds the plan: one
// multipart upload per file. A file that cannot go (unreadable, empty, a
// key RDS cannot use, an object already there without Overwrite...) is
// rejected with its reason; the others stay in the plan.
func PlanUploads(ctx context.Context, c Client, target string, t config.Target, r UploadRequest) (*Batch, error) {
	if err := r.Options.validate(); err != nil {
		return nil, err
	}
	switch {
	case len(r.Files) == 0:
		return nil, invalidf("no files to upload")
	case r.Key != "" && r.Prefix != nil:
		return nil, invalidf("give a key or a prefix, not both")
	case r.Key != "" && len(r.Files) > 1:
		return nil, invalidf("a key names one file: use a prefix for %d files", len(r.Files))
	}
	prefix := t.Prefixes.Import
	if r.Prefix != nil {
		prefix = strings.TrimRight(*r.Prefix, "/")
	}
	if prefix != "" {
		if err := keyError(prefix); err != nil {
			return nil, invalidf("invalid prefix: %v", err)
		}
	}
	if r.Key != "" {
		if err := keyError(r.Key); err != nil {
			return nil, err
		}
	}
	b := newBatch("upload", target, t)
	seen := map[string]string{} // key -> file
	var replaced []string
	for _, file := range r.Files {
		abs, err := filepath.Abs(file)
		if err != nil {
			abs = file
		}
		key := r.Key
		if key == "" {
			key = joinKey(prefix, filepath.Base(abs))
		}
		reject := func(format string, args ...any) {
			b.Rejected = append(b.Rejected, Rejection{Path: file, Key: key, Reason: fmt.Sprintf(format, args...)})
		}
		st, err := os.Stat(abs)
		if err != nil {
			reject("cannot read it: %v", pathErr(err))
			continue
		}
		if !st.Mode().IsRegular() {
			reject("not a regular file")
			continue
		}
		f, err := os.Open(abs)
		if err != nil {
			reject("cannot read it: %v", pathErr(err))
			continue
		}
		_ = f.Close()
		layout, err := UploadLayout(st.Size(), r.Options.PartSize)
		if err != nil {
			reject("%v", err)
			continue
		}
		if err := keyError(key); err != nil {
			reject("%v: rename the file (letters, digits and ._-+=@ only)", err)
			continue
		}
		if other, dup := seen[key]; dup {
			reject("same key as %s", other)
			continue
		}
		head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(t.Bucket), Key: aws.String(key)})
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return nil, fmt.Errorf("read s3://%s/%s: %w", t.Bucket, key, err)
		}
		var existing int64
		if exists {
			existing = aws.ToInt64(head.ContentLength)
			if !r.Overwrite {
				same := "different size"
				if existing == st.Size() {
					same = "same size"
				}
				reject("already in s3://%s/%s (%s, %s): choose overwrite to replace it", t.Bucket, key, FormatBytes(existing), same)
				continue
			}
			replaced = append(replaced, key)
		}
		seen[key] = file
		item := Item{
			Step: fmt.Sprintf("upload-%d", len(b.Items)+1), Path: abs, Bucket: t.Bucket, Key: key,
			Size: st.Size(), PartSize: layout.PartSize, Parts: layout.Parts,
			Checksum: string(types.ChecksumAlgorithmCrc64nvme), Replaces: exists,
		}
		title, risk := fmt.Sprintf("Upload %s (%s)", filepath.Base(abs), FormatBytes(st.Size())), core.Change
		if exists {
			title, risk = title+", replacing the object in S3", core.Destructive
		}
		up := Upload{Path: abs, Bucket: t.Bucket, Key: key, Overwrite: exists, Size: st.Size(), ModTime: st.ModTime()}
		b.action(item, title, risk, uploadStatement(item, r.Options, existing), r.Options,
			func(ctx context.Context, o Options) (Result, error) { return up.Run(ctx, c, o) })
	}
	switch len(replaced) {
	case 0:
	case 1:
		b.Plan.Subject = replaced[0]
	default:
		b.Plan.Subject = fmt.Sprintf("replace %d objects", len(replaced))
	}
	if len(b.Items) > 0 {
		if w := encryptionWarning(ctx, c, t.Bucket); w != "" {
			b.Warnings = append(b.Warnings, w)
		}
	}
	b.Confirmation = b.Plan.Confirmation()
	return b, nil
}

func uploadStatement(it Item, o Options, existing int64) string {
	parts := "1 part"
	if it.Parts > 1 {
		parts = fmt.Sprintf("%d parts of %s, %d at a time", it.Parts, FormatBytes(it.PartSize),
			min(o.withDefaults().Concurrency, it.Parts))
	}
	guard := "If-None-Match: * (never replaces an object)"
	if it.Replaces {
		guard = fmt.Sprintf("replaces the object in S3 (%s)", FormatBytes(existing))
	}
	return fmt.Sprintf("PUT s3://%s/%s · multipart, %s, %s\nfrom %s (%s) · %s",
		it.Bucket, it.Key, parts, it.Checksum, it.Path, FormatBytes(it.Size), guard)
}

// encryptionWarning reads the default encryption of the bucket: with SSE-KMS,
// RDS reads the uploads only if the role of its option can use the key. The
// minimal IAM policy does not allow reading it, so a failure says nothing.
func encryptionWarning(ctx context.Context, c Client, bucket string) string {
	out, err := c.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	if err != nil || out.ServerSideEncryptionConfiguration == nil {
		return ""
	}
	for _, rule := range out.ServerSideEncryptionConfiguration.Rules {
		d := rule.ApplyServerSideEncryptionByDefault
		if d != nil && (d.SSEAlgorithm == types.ServerSideEncryptionAwsKms || d.SSEAlgorithm == types.ServerSideEncryptionAwsKmsDsse) {
			return fmt.Sprintf("bucket %s encrypts new objects with SSE-KMS: RDS can restore them only if the role of the %s option can use the key",
				bucket, "SQLSERVER_BACKUP_RESTORE")
		}
	}
	return ""
}

// DownloadRequest asks to download one object of the bucket of a target.
type DownloadRequest struct {
	// Source is a key, s3://<bucket of the target>/<key>, or a bare file
	// name, which is looked up under the export prefix.
	Source string
	// Dest is the local file, or an existing folder to put it in (""
	// is the current folder). The file name is the last part of the key.
	Dest string
	// Overwrite allows replacing the file if it exists now.
	Overwrite bool
	Options   Options
}

// PlanDownload checks the object and the destination and builds the plan.
// An object that does not exist, or a file that exists without Overwrite, is
// a rejection.
func PlanDownload(ctx context.Context, c Client, target string, t config.Target, r DownloadRequest) (*Batch, error) {
	if err := r.Options.validate(); err != nil {
		return nil, err
	}
	key, err := sourceKey(r.Source, t)
	if err != nil {
		return nil, err
	}
	dest, err := destPath(r.Dest, key)
	if err != nil {
		return nil, err
	}
	b := newBatch("download", target, t)
	reject := func(format string, args ...any) (*Batch, error) {
		b.Rejected = append(b.Rejected, Rejection{Path: dest, Key: key, Reason: fmt.Sprintf(format, args...)})
		return b, nil
	}
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(t.Bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if isNotFound(err) {
		return reject("s3://%s/%s does not exist", t.Bucket, key)
	}
	if err != nil {
		return nil, fmt.Errorf("read s3://%s/%s: %w", t.Bucket, key, err)
	}
	st, err := os.Lstat(dest)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return reject("cannot check the destination: %v", pathErr(err))
	case exists && !st.Mode().IsRegular():
		return reject("%s exists and is not a regular file", dest)
	case exists && !r.Overwrite:
		return reject("%s already exists (%s): choose overwrite to replace it", dest, FormatBytes(st.Size()))
	}
	size := aws.ToInt64(head.ContentLength)
	o := r.Options.withDefaults()
	if err := enoughSpace(o.free, filepath.Dir(dest), size); err != nil {
		return reject("%v", err)
	}
	layout, sum := rangeLayout(size, r.Options.PartSize), objectChecksum(head)
	item := Item{
		Step: "download", Path: dest, Bucket: t.Bucket, Key: key,
		Size: size, PartSize: layout.PartSize, Parts: layout.Parts, Replaces: exists,
	}
	verify := sum.note
	if sum.c != nil {
		item.Checksum = string(sum.c.name)
		verify = item.Checksum + " verified"
	}
	title, risk, guard := fmt.Sprintf("Download %s (%s)", path.Base(key), FormatBytes(size)), core.Change,
		"never replaces a file"
	if exists {
		title, risk, guard = title+", replacing the local file", core.Destructive, "replaces the file"
		b.Plan.Subject = filepath.Base(dest)
	}
	ranges := fmt.Sprintf("%d ranges of %s, %d at a time", layout.Parts, FormatBytes(layout.PartSize), min(o.Concurrency, max(layout.Parts, 1)))
	if layout.Parts <= 1 {
		ranges = "1 range"
	}
	statement := fmt.Sprintf("GET s3://%s/%s · %s, %s\nto %s through a temporary file · %s",
		t.Bucket, key, ranges, verify, dest, guard)
	dl := Download{Bucket: t.Bucket, Key: key, Path: dest, Overwrite: exists, ETag: aws.ToString(head.ETag)}
	b.action(item, title, risk, statement, r.Options,
		func(ctx context.Context, o Options) (Result, error) { return dl.Run(ctx, c, o) })
	b.Confirmation = b.Plan.Confirmation()
	return b, nil
}

// sourceKey turns the source of a download into a key of the target bucket.
func sourceKey(src string, t config.Target) (string, error) {
	key := src
	if rest, ok := strings.CutPrefix(src, "s3://"); ok {
		bucket, k, _ := strings.Cut(rest, "/")
		if bucket != t.Bucket {
			return "", invalidf("the bucket must be %s, the bucket of the target", t.Bucket)
		}
		key = k
	} else if !strings.Contains(src, "/") {
		key = joinKey(t.Prefixes.Export, src)
	}
	return key, keyError(key)
}

// destPath is the local file of a download: dest itself, or the last part
// of the key inside dest when dest is a folder.
func destPath(dest, key string) (string, error) {
	if dest == "" {
		dest = "."
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", invalidf("invalid destination %q: %v", dest, err)
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return filepath.Join(abs, path.Base(key)), nil
	}
	if st, err := os.Stat(filepath.Dir(abs)); err != nil || !st.IsDir() {
		return "", invalidf("folder %s does not exist", filepath.Dir(abs))
	}
	return abs, nil
}

// keyError checks the key of one object: the rules of validate.S3Key (it
// must fit in the ARN that RDS reads), no "*" (restore reads it as a
// wildcard) and no "/" at the end.
func keyError(key string) error {
	switch {
	case key == "":
		return invalidf("empty S3 key")
	case strings.HasSuffix(key, "/"):
		return invalidf("S3 key %q names a folder, not an object", key)
	case strings.Contains(key, "*"):
		return invalidf("S3 key %q contains *, which restore reads as a wildcard", key)
	}
	if err := validate.S3Key(key); err != nil {
		return requestError{err.Error()}
	}
	return nil
}

func joinKey(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

// pathErr drops the path an *fs.PathError repeats: the rejection names it.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
