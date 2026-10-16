// Package transfertest is an in-memory S3 for the tests of package transfer
// and of the commands and endpoints built on it. It checks what S3 checks
// (part checksums, the whole-object checksum on completion, the minimum
// part size, If-None-Match and If-Match) and lets a test fail or slow down
// any part or range.
package transfertest

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Object is a stored object.
type Object struct {
	Data         []byte
	ETag         string
	Checksums    map[types.ChecksumAlgorithm]string
	ChecksumType types.ChecksumType
}

type part struct {
	data     []byte
	etag     string
	checksum string
}

type upload struct {
	bucket, key string
	parts       map[int32]part
}

// S3 is the in-memory server. The zero value is not usable: use New.
type S3 struct {
	mu      sync.Mutex
	objects map[string]*Object // bucket/key
	uploads map[string]*upload
	nextID  int
	calls   map[string]int
	tries   map[string]int

	// OnUploadPart runs before a part is stored, for every attempt (from
	// 1): an error fails the attempt. It may block on ctx.
	OnUploadPart func(ctx context.Context, part int32, attempt int) error
	// OnGetObject runs before a GET is answered, likewise; rng is the
	// Range header.
	OnGetObject func(ctx context.Context, rng string, attempt int) error
	// Encryption is the default encryption of every bucket; "" answers
	// that there is none.
	Encryption types.ServerSideEncryption
}

// New returns an empty S3.
func New() *S3 {
	return &S3{objects: map[string]*Object{}, uploads: map[string]*upload{}, calls: map[string]int{}, tries: map[string]int{}}
}

// Calls is how many times an operation ran ("AbortMultipartUpload"...).
func (s *S3) Calls(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[op]
}

// Uploads is how many multipart uploads are open.
func (s *S3) Uploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

// UploadIDs are the open multipart uploads.
func (s *S3) UploadIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.uploads {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Get returns a stored object, nil if missing.
func (s *S3) Get(bucket, key string) *Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[bucket+"/"+key]
}

// Put stores an object as a single PUT would, with a whole-object checksum
// of the given algorithm ("" for none).
func (s *S3) Put(bucket, key string, data []byte, algo types.ChecksumAlgorithm) {
	o := &Object{Data: slices.Clone(data), ETag: etag(data), Checksums: map[types.ChecksumAlgorithm]string{}}
	if algo != "" {
		o.Checksums[algo] = Checksum(algo, data)
		o.ChecksumType = types.ChecksumTypeFullObject
	}
	s.PutObject(bucket, key, o)
}

// PutObject stores o as it is.
func (s *S3) PutObject(bucket, key string, o *Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = o
}

// Checksum is the base64 checksum of data, as S3 shows it.
func Checksum(algo types.ChecksumAlgorithm, data []byte) string {
	var sum []byte
	switch algo {
	case types.ChecksumAlgorithmCrc64nvme:
		sum = binary.BigEndian.AppendUint64(nil, crc64.Checksum(data, crc64.MakeTable(0x9a6c9329ac4bc9b5)))
	case types.ChecksumAlgorithmCrc32:
		sum = binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(data))
	case types.ChecksumAlgorithmCrc32c:
		sum = binary.BigEndian.AppendUint32(nil, crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
	default:
		panic("transfertest: no checksum " + string(algo))
	}
	return base64.StdEncoding.EncodeToString(sum)
}

// Error is an S3 error answer as the SDK returns it.
func Error(op string, status int, code string) error {
	var api error = &smithy.GenericAPIError{Code: code, Message: code}
	switch code {
	case "NotFound":
		api = &types.NotFound{}
	case "NoSuchKey":
		api = &types.NoSuchKey{}
	case "NoSuchUpload":
		api = &types.NoSuchUpload{}
	}
	return &smithy.OperationError{ServiceID: "S3", OperationName: op, Err: &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}}, Err: api,
		},
	}}
}

func etag(data []byte) string {
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *S3) count(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[op]++
}

func (s *S3) attempt(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tries[what]++
	return s.tries[what]
}

// HeadObject answers the size, the ETag and, with ChecksumMode ENABLED, the
// checksums.
func (s *S3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	s.count("HeadObject")
	o := s.Get(aws.ToString(in.Bucket), aws.ToString(in.Key))
	if o == nil {
		return nil, Error("HeadObject", 404, "NotFound")
	}
	out := &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(o.Data))), ETag: aws.String(o.ETag)}
	if in.ChecksumMode == types.ChecksumModeEnabled {
		out.ChecksumType = o.ChecksumType
		for algo, v := range o.Checksums {
			switch algo {
			case types.ChecksumAlgorithmCrc64nvme:
				out.ChecksumCRC64NVME = aws.String(v)
			case types.ChecksumAlgorithmCrc32:
				out.ChecksumCRC32 = aws.String(v)
			case types.ChecksumAlgorithmCrc32c:
				out.ChecksumCRC32C = aws.String(v)
			case types.ChecksumAlgorithmSha256:
				out.ChecksumSHA256 = aws.String(v)
			}
		}
	}
	return out, nil
}

// GetObject answers the object, or the "bytes=a-b" range of it.
func (s *S3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	s.count("GetObject")
	rng := aws.ToString(in.Range)
	if s.OnGetObject != nil {
		if err := s.OnGetObject(ctx, rng, s.attempt("get "+rng)); err != nil {
			return nil, err
		}
	}
	o := s.Get(aws.ToString(in.Bucket), aws.ToString(in.Key))
	if o == nil {
		return nil, Error("GetObject", 404, "NoSuchKey")
	}
	if m := aws.ToString(in.IfMatch); m != "" && m != o.ETag {
		return nil, Error("GetObject", 412, "PreconditionFailed")
	}
	data := o.Data
	if rng != "" {
		a, b, ok := parseRange(rng, int64(len(data)))
		if !ok {
			return nil, Error("GetObject", 416, "InvalidRange")
		}
		data = data[a : b+1]
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(data)), ContentLength: aws.Int64(int64(len(data))), ETag: aws.String(o.ETag),
	}, nil
}

func parseRange(rng string, size int64) (int64, int64, bool) {
	spec, ok := strings.CutPrefix(rng, "bytes=")
	if !ok {
		return 0, 0, false
	}
	from, to, _ := strings.Cut(spec, "-")
	a, err1 := strconv.ParseInt(from, 10, 64)
	b, err2 := strconv.ParseInt(to, 10, 64)
	if err1 != nil || err2 != nil || a < 0 || a > b || a >= size {
		return 0, 0, false
	}
	return a, min(b, size-1), true
}

// CreateMultipartUpload opens an upload.
func (s *S3) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	s.count("CreateMultipartUpload")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := fmt.Sprintf("upload-%d", s.nextID)
	s.uploads[id] = &upload{bucket: aws.ToString(in.Bucket), key: aws.ToString(in.Key), parts: map[int32]part{}}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id), Bucket: in.Bucket, Key: in.Key}, nil
}

// UploadPart stores a part; a CRC64NVME in the request must match the bytes.
func (s *S3) UploadPart(ctx context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	s.count("UploadPart")
	n := aws.ToInt32(in.PartNumber)
	if s.OnUploadPart != nil {
		if err := s.OnUploadPart(ctx, n, s.attempt(fmt.Sprintf("part %d", n))); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sum := Checksum(types.ChecksumAlgorithmCrc64nvme, data)
	if want := aws.ToString(in.ChecksumCRC64NVME); want != "" && want != sum {
		return nil, Error("UploadPart", 400, "BadDigest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.uploads[aws.ToString(in.UploadId)]
	if u == nil {
		return nil, Error("UploadPart", 404, "NoSuchUpload")
	}
	p := part{data: data, etag: etag(data), checksum: sum}
	u.parts[n] = p
	return &s3.UploadPartOutput{ETag: aws.String(p.etag), ChecksumCRC64NVME: aws.String(sum)}, nil
}

// CompleteMultipartUpload assembles the listed parts, as S3 does.
func (s *S3) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	s.count("CompleteMultipartUpload")
	s.mu.Lock()
	defer s.mu.Unlock()
	id := aws.ToString(in.UploadId)
	u := s.uploads[id]
	if u == nil {
		return nil, Error("CompleteMultipartUpload", 404, "NoSuchUpload")
	}
	name := u.bucket + "/" + u.key
	if aws.ToString(in.IfNoneMatch) == "*" && s.objects[name] != nil {
		return nil, Error("CompleteMultipartUpload", 412, "PreconditionFailed")
	}
	var data []byte
	var md5s []byte
	listed := in.MultipartUpload.Parts
	for i, cp := range listed {
		n := aws.ToInt32(cp.PartNumber)
		p, ok := u.parts[n]
		if !ok || p.etag != aws.ToString(cp.ETag) || (i > 0 && n <= aws.ToInt32(listed[i-1].PartNumber)) {
			return nil, Error("CompleteMultipartUpload", 400, "InvalidPart")
		}
		if i < len(listed)-1 && len(p.data) < 5<<20 {
			return nil, Error("CompleteMultipartUpload", 400, "EntityTooSmall")
		}
		data = append(data, p.data...)
		raw, _ := hex.DecodeString(strings.Trim(p.etag, `"`))
		md5s = append(md5s, raw...)
	}
	sum := Checksum(types.ChecksumAlgorithmCrc64nvme, data)
	if want := aws.ToString(in.ChecksumCRC64NVME); want != "" && want != sum {
		return nil, Error("CompleteMultipartUpload", 400, "BadDigest")
	}
	if size := in.MpuObjectSize; size != nil && *size != int64(len(data)) {
		return nil, Error("CompleteMultipartUpload", 400, "InvalidRequest")
	}
	all := md5.Sum(md5s)
	o := &Object{
		Data: data, ETag: fmt.Sprintf(`"%s-%d"`, hex.EncodeToString(all[:]), len(listed)),
		Checksums:    map[types.ChecksumAlgorithm]string{types.ChecksumAlgorithmCrc64nvme: sum},
		ChecksumType: types.ChecksumTypeFullObject,
	}
	s.objects[name] = o
	delete(s.uploads, id)
	return &s3.CompleteMultipartUploadOutput{
		Bucket: aws.String(u.bucket), Key: aws.String(u.key), ETag: aws.String(o.ETag),
		ChecksumCRC64NVME: aws.String(sum), ChecksumType: types.ChecksumTypeFullObject,
	}, nil
}

// AbortMultipartUpload drops an upload and its parts.
func (s *S3) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	s.count("AbortMultipartUpload")
	s.mu.Lock()
	defer s.mu.Unlock()
	id := aws.ToString(in.UploadId)
	if s.uploads[id] == nil {
		return nil, Error("AbortMultipartUpload", 404, "NoSuchUpload")
	}
	delete(s.uploads, id)
	return &s3.AbortMultipartUploadOutput{}, nil
}

// ListParts lists the parts of an upload, 2 per page so tests see paging.
func (s *S3) ListParts(_ context.Context, in *s3.ListPartsInput, _ ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	s.count("ListParts")
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.uploads[aws.ToString(in.UploadId)]
	if u == nil {
		return nil, Error("ListParts", 404, "NoSuchUpload")
	}
	marker := int32(0)
	if m := aws.ToString(in.PartNumberMarker); m != "" {
		v, _ := strconv.Atoi(m)
		marker = int32(v)
	}
	var numbers []int32
	for n := range u.parts {
		if n > marker {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	out := &s3.ListPartsOutput{IsTruncated: aws.Bool(len(numbers) > 2)}
	if len(numbers) > 2 {
		numbers = numbers[:2]
		out.NextPartNumberMarker = aws.String(strconv.Itoa(int(numbers[1])))
	}
	for _, n := range numbers {
		p := u.parts[n]
		out.Parts = append(out.Parts, types.Part{
			PartNumber: aws.Int32(n), Size: aws.Int64(int64(len(p.data))), ETag: aws.String(p.etag),
			ChecksumCRC64NVME: aws.String(p.checksum),
		})
	}
	return out, nil
}

// GetBucketEncryption answers Encryption.
func (s *S3) GetBucketEncryption(_ context.Context, _ *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	s.count("GetBucketEncryption")
	if s.Encryption == "" {
		return nil, Error("GetBucketEncryption", 404, "ServerSideEncryptionConfigurationNotFoundError")
	}
	return &s3.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
		Rules: []types.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{SSEAlgorithm: s.Encryption}}},
	}}, nil
}
