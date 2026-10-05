package s3gateway

import (
	"context"
	"crypto/md5" //nolint:gosec // ETag is an S3 content fingerprint, not a security primitive
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/versity/versitygw/backend"
	"github.com/versity/versitygw/s3err"
	"github.com/versity/versitygw/s3response"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// multipartIdleExpiry is how long an upload may go without a part before it counts as
// abandoned and its buffered parts are dropped: S3 keeps an incomplete upload until it is
// aborted, but a client that crashed never aborts, and here the parts are daemon RAM.
const multipartIdleExpiry = time.Hour

// multipartStore buffers in-flight multipart uploads in memory (ADR-0080 Temporary
// workarounds): the blob port has no streaming-multipart seam, so parts accumulate
// and CompleteMultipartUpload assembles them into a single Put, bounded by maxUpload.
type multipartStore struct {
	mu      sync.Mutex
	uploads map[string]*upload // uploadID → buffered parts
	clock   clock.Clock
}

// uploadTarget is what an upload is bound to: the creator's namespace and the bucket and key
// it was created for. S3 scopes an upload id to its bucket and key, and a bucket name resolves
// per namespace (ADR-0080 tenancy), so an id names no upload outside its own target.
type uploadTarget struct {
	ns          v1.NamespaceName
	bucket, key string
}

type upload struct {
	target  uploadTarget
	parts   map[int32][]byte
	size    int64     // the sum of the buffered parts' lengths
	touched time.Time // the last Create or UploadPart
}

func newMultipartStore() *multipartStore {
	return &multipartStore{uploads: map[string]*upload{}, clock: clock.System()}
}

// create starts an upload and drops the abandoned ones: a new upload is the only way the
// number of buffered uploads grows, so sweeping here keeps it to the recently active ones.
func (m *multipartStore) create(t uploadTarget) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	for id, u := range m.uploads {
		if now.Sub(u.touched) > multipartIdleExpiry {
			delete(m.uploads, id)
		}
	}
	id := "funcd-mpu-" + rand.Text()
	// The request's strings alias fiber's reused buffers; the binding outlives the request.
	t = uploadTarget{ns: v1.NamespaceName(strings.Clone(string(t.ns))), bucket: strings.Clone(t.bucket), key: strings.Clone(t.key)}
	m.uploads[id] = &upload{target: t, parts: map[int32][]byte{}, touched: now}
	return id
}

// get returns the upload id names when it is bound to t. The caller holds m.mu.
func (m *multipartStore) get(id string, t uploadTarget) (*upload, bool) {
	u, ok := m.uploads[id]
	if !ok || u.target != t {
		return nil, false
	}
	return u, true
}

// putPart buffers part num, replacing an earlier part of that number. It fails closed with
// EntityTooLarge when the upload's total would pass maxUpload (ADR-0080), so an upload never
// holds more than the cap.
func (m *multipartStore) putPart(id string, t uploadTarget, num int32, data []byte, maxUpload int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
	if !ok {
		return s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	size := u.size - int64(len(u.parts[num])) + int64(len(data))
	if size > maxUpload {
		return s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	u.parts[num] = data
	u.size = size
	u.touched = m.clock.Now()
	return nil
}

// assemble concatenates the parts the client listed, in its order (S3
// CompleteMultipartUpload): part numbers must ascend and each must be buffered with a
// matching ETag; unlisted parts are dropped. It does NOT delete the upload (Complete does).
func (m *multipartStore) assemble(id string, t uploadTarget, mpu *awstypes.CompletedMultipartUpload) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
	if !ok {
		return nil, s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	if mpu == nil || len(mpu.Parts) == 0 {
		return nil, s3err.GetAPIError(s3err.ErrMalformedXML)
	}
	var buf []byte
	var prev int32
	for _, p := range mpu.Parts {
		if p.PartNumber == nil || p.ETag == nil {
			return nil, s3err.GetAPIError(s3err.ErrMalformedXML)
		}
		num := *p.PartNumber
		if num <= prev {
			return nil, s3err.GetAPIError(s3err.ErrInvalidPartOrder)
		}
		prev = num
		data, ok := u.parts[num]
		if !ok || !backend.AreEtagsSame(etag(data), *p.ETag) {
			return nil, s3err.GetInvalidPartErr(id, num, *p.ETag)
		}
		buf = append(buf, data...)
	}
	return buf, nil
}

// abort drops the upload when it is bound to t and reports whether it did.
func (m *multipartStore) abort(id string, t uploadTarget) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.get(id, t); !ok {
		return false
	}
	delete(m.uploads, id)
	return true
}

func (m *multipartStore) parts(id string, t uploadTarget) ([]s3response.Part, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
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
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	key := deref(in.Key)
	prefix, _ := splitKey(key)
	_, pr, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return s3response.InitiateMultipartUploadResult{}, err
	}
	id := b.mp.create(uploadTarget{ns: pr.namespace, bucket: bucket, key: key})
	return s3response.InitiateMultipartUploadResult{Bucket: bucket, Key: key, UploadId: id}, nil
}

// UploadPart buffers one part (ADR-0080): s3::write PEP; the upload's total is capped by maxUpload.
func (b *be) UploadPart(ctx context.Context, in *awss3.UploadPartInput) (*awss3.UploadPartOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	key := deref(in.Key)
	prefix, _ := splitKey(key)
	_, pr, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
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
	if perr := b.mp.putPart(deref(in.UploadId), uploadTarget{ns: pr.namespace, bucket: bucket, key: key}, num, data, b.maxUpload); perr != nil {
		return nil, perr
	}
	return &awss3.UploadPartOutput{ETag: ptr(etag(data))}, nil
}

// CompleteMultipartUpload assembles the buffered parts and Puts the object once
// (ADR-0080): s3::write PEP, total bounded by maxUpload (fail-closed). If-None-Match: * makes
// it create-only (createOnly); a refused Complete leaves the upload in place.
func (b *be) CompleteMultipartUpload(ctx context.Context, in *awss3.CompleteMultipartUploadInput) (s3response.CompleteMultipartUploadResult, string, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	prefix, object := splitKey(deref(in.Key))
	sub, pr, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return s3response.CompleteMultipartUploadResult{}, "", err
	}
	target := uploadTarget{ns: pr.namespace, bucket: bucket, key: deref(in.Key)}
	key := blobKey(prefix, object)
	if cerr := createOnly(ctx, sub, key, in.IfNoneMatch); cerr != nil {
		return s3response.CompleteMultipartUploadResult{}, "", cerr
	}
	id := deref(in.UploadId)
	data, aerr := b.mp.assemble(id, target, in.MultipartUpload)
	if aerr != nil {
		return s3response.CompleteMultipartUploadResult{}, "", aerr
	}
	if int64(len(data)) > b.maxUpload {
		b.mp.abort(id, target)
		return s3response.CompleteMultipartUploadResult{}, "", s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	if perr := sub.Put(ctx, key, data); perr != nil {
		return s3response.CompleteMultipartUploadResult{}, "", mapBlobErr(perr)
	}
	b.mp.abort(id, target)
	return s3response.CompleteMultipartUploadResult{
		Bucket: in.Bucket,
		Key:    in.Key,
		ETag:   ptr(etag(data)),
	}, "", nil
}

// AbortMultipartUpload discards buffered parts (ADR-0080): s3::write PEP.
func (b *be) AbortMultipartUpload(ctx context.Context, in *awss3.AbortMultipartUploadInput) error {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	key := deref(in.Key)
	prefix, _ := splitKey(key)
	_, pr, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return err
	}
	if !b.mp.abort(deref(in.UploadId), uploadTarget{ns: pr.namespace, bucket: bucket, key: key}) {
		return s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	return nil
}

// ListParts lists buffered parts (ADR-0080): s3::read PEP (a metadata view of the upload).
func (b *be) ListParts(ctx context.Context, in *awss3.ListPartsInput) (s3response.ListPartsResult, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	key := deref(in.Key)
	prefix, _ := splitKey(key)
	_, pr, err := b.authorize(ctx, authz.ActionS3Read, bucket, prefix)
	if err != nil {
		return s3response.ListPartsResult{}, err
	}
	parts, ok := b.mp.parts(deref(in.UploadId), uploadTarget{ns: pr.namespace, bucket: bucket, key: key})
	if !ok {
		return s3response.ListPartsResult{}, s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	return s3response.ListPartsResult{Bucket: bucket, Key: key, UploadID: deref(in.UploadId), Parts: parts}, nil
}

// etag is the S3 ETag of data in its wire form: the MD5 hex in double quotes (an RFC 9110
// entity-tag). Every response that carries an ETag uses it, so the gateway has one form.
func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // content fingerprint, not security
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
