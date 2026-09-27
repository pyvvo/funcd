package s3gateway

import (
	"context"
	"crypto/md5" //nolint:gosec // ETag is an S3 content fingerprint, not a security primitive
	"encoding/hex"
	"io"
	"sort"
	"strconv"
	"sync"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/versity/versitygw/s3err"
	"github.com/versity/versitygw/s3response"

	authz "github.com/pyvvo/funcd/internal/auth"
)

// multipartStore buffers in-flight multipart uploads in memory (ADR-0080 Temporary
// workarounds): the blob port has no streaming-multipart seam, so parts accumulate
// and CompleteMultipartUpload assembles them into a single Put, bounded by maxUpload.
type multipartStore struct {
	mu      sync.Mutex
	uploads map[string]*upload // uploadID → buffered parts
	next    uint64
}

type upload struct {
	bucket, key string
	parts       map[int32][]byte
}

func newMultipartStore() *multipartStore {
	return &multipartStore{uploads: map[string]*upload{}}
}

func (m *multipartStore) create(bucket, key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	id := "funcd-mpu-" + strconv.FormatUint(m.next, 10)
	m.uploads[id] = &upload{bucket: bucket, key: key, parts: map[int32][]byte{}}
	return id
}

func (m *multipartStore) putPart(id string, num int32, data []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return false
	}
	u.parts[num] = data
	return true
}

// assemble concatenates the parts in ascending part-number order and returns the
// object bytes plus its total size; it does NOT delete the upload (Complete does).
func (m *multipartStore) assemble(id string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return nil, false
	}
	nums := make([]int, 0, len(u.parts))
	for n := range u.parts {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	var buf []byte
	for _, n := range nums {
		buf = append(buf, u.parts[int32(n)]...)
	}
	return buf, true
}

func (m *multipartStore) abort(id string) { m.mu.Lock(); delete(m.uploads, id); m.mu.Unlock() }

func (m *multipartStore) parts(id string) ([]s3response.Part, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return nil, false
	}
	nums := make([]int, 0, len(u.parts))
	for n := range u.parts {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	out := make([]s3response.Part, 0, len(nums))
	for _, n := range nums {
		out = append(out, s3response.Part{PartNumber: n, Size: int64(len(u.parts[int32(n)])), ETag: etag(u.parts[int32(n)])})
	}
	return out, true
}

// --- backend multipart methods (PEP-guarded) -------------------------------------

// CreateMultipartUpload starts a buffered multipart upload (ADR-0080): s3::write PEP.
func (b *be) CreateMultipartUpload(ctx context.Context, in s3response.CreateMultipartUploadInput) (s3response.InitiateMultipartUploadResult, error) {
	bucket := deref(in.Bucket)
	key := deref(in.Key)
	prefix, _ := splitKey(key)
	if _, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix); err != nil {
		return s3response.InitiateMultipartUploadResult{}, err
	}
	id := b.mp.create(bucket, key)
	return s3response.InitiateMultipartUploadResult{Bucket: bucket, Key: key, UploadId: id}, nil
}

// UploadPart buffers one part (ADR-0080): s3::write PEP, capped per part by maxUpload.
func (b *be) UploadPart(ctx context.Context, in *awss3.UploadPartInput) (*awss3.UploadPartOutput, error) {
	bucket := deref(in.Bucket)
	prefix, _ := splitKey(deref(in.Key))
	if _, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix); err != nil {
		return nil, err
	}
	data, rerr := b.readCapped(in.Body)
	if rerr != nil {
		return nil, rerr
	}
	num := int32(1)
	if in.PartNumber != nil {
		num = *in.PartNumber
	}
	if !b.mp.putPart(deref(in.UploadId), num, data) {
		return nil, s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	return &awss3.UploadPartOutput{ETag: ptr(quotedETag(data))}, nil
}

// CompleteMultipartUpload assembles the buffered parts and Puts the object once
// (ADR-0080): s3::write PEP, total bounded by maxUpload (fail-closed).
func (b *be) CompleteMultipartUpload(ctx context.Context, in *awss3.CompleteMultipartUploadInput) (s3response.CompleteMultipartUploadResult, string, error) {
	bucket := deref(in.Bucket)
	prefix, object := splitKey(deref(in.Key))
	sub, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return s3response.CompleteMultipartUploadResult{}, "", err
	}
	id := deref(in.UploadId)
	data, ok := b.mp.assemble(id)
	if !ok {
		return s3response.CompleteMultipartUploadResult{}, "", s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	if int64(len(data)) > b.maxUpload {
		b.mp.abort(id)
		return s3response.CompleteMultipartUploadResult{}, "", s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	if perr := sub.Put(ctx, blobKey(prefix, object), data); perr != nil {
		return s3response.CompleteMultipartUploadResult{}, "", mapBlobErr(perr)
	}
	b.mp.abort(id)
	return s3response.CompleteMultipartUploadResult{
		Bucket: in.Bucket,
		Key:    in.Key,
		ETag:   ptr(quotedETag(data)),
	}, "", nil
}

// AbortMultipartUpload discards buffered parts (ADR-0080): s3::write PEP.
func (b *be) AbortMultipartUpload(ctx context.Context, in *awss3.AbortMultipartUploadInput) error {
	prefix, _ := splitKey(deref(in.Key))
	if _, _, err := b.authorize(ctx, authz.ActionS3Write, deref(in.Bucket), prefix); err != nil {
		return err
	}
	b.mp.abort(deref(in.UploadId))
	return nil
}

// ListParts lists buffered parts (ADR-0080): s3::read PEP (a metadata view of the upload).
func (b *be) ListParts(ctx context.Context, in *awss3.ListPartsInput) (s3response.ListPartsResult, error) {
	bucket := deref(in.Bucket)
	prefix, _ := splitKey(deref(in.Key))
	if _, _, err := b.authorize(ctx, authz.ActionS3Read, bucket, prefix); err != nil {
		return s3response.ListPartsResult{}, err
	}
	parts, ok := b.mp.parts(deref(in.UploadId))
	if !ok {
		return s3response.ListPartsResult{}, s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	return s3response.ListPartsResult{Bucket: bucket, Key: deref(in.Key), UploadID: deref(in.UploadId), Parts: parts}, nil
}

// etag is the unquoted MD5 content fingerprint S3 uses for an ETag.
func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // content fingerprint, not security
	return hex.EncodeToString(sum[:])
}

// quotedETag is the S3 wire form: the MD5 hex wrapped in double quotes.
func quotedETag(data []byte) string { return `"` + etag(data) + `"` }

var _ = io.EOF
