package s3gateway

import (
	"context"
	"crypto/md5" //nolint:gosec // ETag is an S3 content fingerprint, not a security primitive
	"crypto/rand"
	"encoding/hex"
	"math"
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
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// multipartIdleExpiry is how long an upload may go without a part that grows it past its peak
// before it counts as abandoned and its buffered parts are dropped: S3 keeps an incomplete upload
// until it is aborted, but a client that crashed never aborts, and here the parts are daemon RAM.
const multipartIdleExpiry = time.Hour

// multipartBudgetFactor sizes the daemon-wide budget on buffered multipart bytes (ADR-0188): budget =
// multipartBudgetFactor × maxUpload, and parts may use budget − maxUpload so one full-cap Complete always fits.
const multipartBudgetFactor = 3

// multipartStore buffers in-flight multipart uploads in memory (ADR-0080 Temporary
// workarounds): the blob port has no streaming-multipart seam, so parts accumulate
// and CompleteMultipartUpload assembles them into a single Put, bounded by maxUpload.
// The parts and the copies Completes assemble share one budget, and each owner a share of
// it (ADR-0188), so no number of uploads or principals can grow the buffers past it.
type multipartStore struct {
	mu         sync.Mutex
	uploads    map[string]*upload // uploadID → buffered parts
	clock      clock.Clock
	maxUpload  int64                     // s3gateway.maxUploadBytes: the per-upload cap and the per-principal share
	budget     int64                     // multipartBudgetFactor × maxUpload; 0 = off when that overflows
	buffered   int64                     // the sum of every upload's size
	assembling int64                     // the copies held by in-flight Completes
	owned      map[authz.EntityRef]int64 // each owner's buffered bytes; no entry at 0
}

// uploadTarget is what an upload is bound to: the creator's namespace and the bucket and key
// it was created for. S3 scopes an upload id to its bucket and key, and a bucket name resolves
// per namespace (ADR-0080 tenancy), so an id names no upload outside its own target.
type uploadTarget struct {
	ns          v1.NamespaceName
	bucket, key string
}

type upload struct {
	target     uploadTarget
	opts       blob.PutOptions // the Content-Type and user metadata of CreateMultipartUpload
	parts      map[int32][]byte
	size       int64           // the sum of the buffered parts' lengths
	touched    time.Time       // the last Create or part that took size past peak
	peak       int64           // the largest size the upload has held
	owner      authz.EntityRef // the principal that created the upload
	completing bool            // a Complete holds this upload's assembled copy; the sweep skips it
}

func newMultipartStore(maxUpload int64) *multipartStore {
	var budget int64
	if maxUpload <= math.MaxInt64/multipartBudgetFactor {
		budget = multipartBudgetFactor * maxUpload
	}
	return &multipartStore{
		uploads:   map[string]*upload{},
		clock:     clock.System(),
		maxUpload: maxUpload,
		budget:    budget,
		owned:     map[authz.EntityRef]int64{},
	}
}

// create starts an upload owned by owner and drops the abandoned ones: a new upload is the only way the
// number of buffered uploads grows, so sweeping here keeps it to the recently active ones.
func (m *multipartStore) create(t uploadTarget, owner authz.EntityRef, opts blob.PutOptions) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	m.sweep(now)
	id := "funcd-mpu-" + rand.Text()
	// The request's strings alias fiber's reused buffers; the binding and the owner outlive the request.
	t = uploadTarget{ns: v1.NamespaceName(strings.Clone(string(t.ns))), bucket: strings.Clone(t.bucket), key: strings.Clone(t.key)}
	owner = authz.EntityRef{
		Type:      v1.Kind(strings.Clone(string(owner.Type))),
		Namespace: v1.NamespaceName(strings.Clone(string(owner.Namespace))),
		Name:      v1.ObjectName(strings.Clone(string(owner.Name))),
		Path:      strings.Clone(owner.Path),
	}
	md := make(map[string]string, len(opts.Metadata))
	for k, v := range opts.Metadata {
		md[strings.Clone(k)] = strings.Clone(v)
	}
	opts = blob.PutOptions{ContentType: strings.Clone(opts.ContentType), Metadata: md}
	m.uploads[id] = &upload{target: t, opts: opts, parts: map[int32][]byte{}, touched: now, owner: owner}
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

// sweep drops the uploads idle past multipartIdleExpiry, except one a Complete holds. The caller holds m.mu.
func (m *multipartStore) sweep(now time.Time) {
	for id, u := range m.uploads {
		if !u.completing && now.Sub(u.touched) > multipartIdleExpiry {
			m.drop(id, u)
		}
	}
}

// drop removes the upload and returns its bytes to buffered and its owner. The caller holds m.mu.
func (m *multipartStore) drop(id string, u *upload) {
	delete(m.uploads, id)
	m.grow(u, -u.size)
}

// grow adds delta to the upload, buffered and the owner's bytes; an owner at 0 leaves owned. The caller holds m.mu.
func (m *multipartStore) grow(u *upload, delta int64) {
	u.size += delta
	m.buffered += delta
	if left := m.owned[u.owner] + delta; left != 0 {
		m.owned[u.owner] = left
	} else {
		delete(m.owned, u.owner)
	}
}

// partFits reports whether a part growing owner's upload by delta keeps the owner within its share, the parts
// within budget − maxUpload, and the parts plus the assembled copies within the budget. The caller holds m.mu.
func (m *multipartStore) partFits(owner authz.EntityRef, delta int64) bool {
	if delta > m.maxUpload-m.owned[owner] {
		return false
	}
	return m.budget == 0 || (delta <= m.budget-m.maxUpload-m.buffered && delta <= m.budget-m.buffered-m.assembling)
}

// copyFits reports whether a Complete's copy of n bytes fits beside the parts and the other copies. The caller
// holds m.mu.
func (m *multipartStore) copyFits(n int64) bool {
	return m.budget == 0 || n <= m.budget-m.buffered-m.assembling
}

// putPart buffers part num, replacing an earlier part of that number. It fails closed with
// EntityTooLarge when the upload's total would pass maxUpload (ADR-0080), so an upload never
// holds more than the cap, and with SlowDown when a part that grows the upload does not fit the
// owner's share or the budget even after a sweep (ADR-0188); a refused part changes nothing.
func (m *multipartStore) putPart(id string, t uploadTarget, num int32, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
	if !ok {
		return s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	delta := int64(len(data)) - int64(len(u.parts[num]))
	if delta > m.maxUpload-u.size {
		return s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	now := m.clock.Now()
	if delta > 0 && !m.partFits(u.owner, delta) {
		m.sweep(now)
		if _, ok := m.get(id, t); !ok {
			return s3err.GetAPIError(s3err.ErrNoSuchUpload)
		}
		if !m.partFits(u.owner, delta) {
			return s3err.GetAPIError(s3err.ErrSlowDown)
		}
	}
	u.parts[num] = data
	m.grow(u, delta)
	// Only growth past the peak keeps an upload alive, so re-sending parts cannot hold the budget forever.
	if u.size > u.peak {
		u.peak = u.size
		u.touched = now
	}
	return nil
}

// assemble concatenates the parts the client listed, in its order (S3
// CompleteMultipartUpload): part numbers must ascend and each must be buffered with a
// matching ETag; unlisted parts are dropped. It returns the upload's Put options with the bytes and
// does NOT delete the upload. The copy counts against the budget and marks the upload completing, so
// a second Complete of the id answers SlowDown; the caller hands both back with release (ADR-0188).
func (m *multipartStore) assemble(id string, t uploadTarget, mpu *awstypes.CompletedMultipartUpload) ([]byte, blob.PutOptions, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
	if !ok {
		return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrNoSuchUpload)
	}
	if mpu == nil || len(mpu.Parts) == 0 {
		return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrMalformedXML)
	}
	var n int64
	var prev int32
	for _, p := range mpu.Parts {
		if p.PartNumber == nil || p.ETag == nil {
			return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrMalformedXML)
		}
		num := *p.PartNumber
		if num <= prev {
			return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrInvalidPartOrder)
		}
		prev = num
		data, ok := u.parts[num]
		if !ok || !backend.AreEtagsSame(etag(data), *p.ETag) {
			return nil, blob.PutOptions{}, s3err.GetInvalidPartErr(id, num, *p.ETag)
		}
		n += int64(len(data))
	}
	if u.completing {
		return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrSlowDown)
	}
	if !m.copyFits(n) {
		m.sweep(m.clock.Now())
		if _, ok := m.get(id, t); !ok {
			return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrNoSuchUpload)
		}
		if !m.copyFits(n) {
			return nil, blob.PutOptions{}, s3err.GetAPIError(s3err.ErrSlowDown)
		}
	}
	u.completing = true
	m.assembling += n
	buf := make([]byte, 0, n)
	for _, p := range mpu.Parts {
		buf = append(buf, u.parts[*p.PartNumber]...)
	}
	return buf, u.opts, nil
}

// release hands back the n-byte copy an admitted assemble counted and clears completing; with drop it also
// drops the upload and returns its parts. An upload aborted meanwhile returns only n.
func (m *multipartStore) release(id string, t uploadTarget, n int64, drop bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assembling -= n
	u, ok := m.get(id, t)
	if !ok {
		return
	}
	u.completing = false
	if drop {
		m.drop(id, u)
	}
}

// abort drops the upload when it is bound to t, returning its bytes, and reports whether it did.
func (m *multipartStore) abort(id string, t uploadTarget) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.get(id, t)
	if !ok {
		return false
	}
	m.drop(id, u)
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

// CreateMultipartUpload starts a buffered multipart upload (ADR-0080): s3::write PEP. The upload keeps
// the request's Content-Type and user metadata for the Put at Complete (ADR-0159).
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
	id := b.mp.create(uploadTarget{ns: pr.namespace, bucket: bucket, key: key}, pr.ref, blob.PutOptions{ContentType: deref(in.ContentType), Metadata: in.Metadata})
	return s3response.InitiateMultipartUploadResult{Bucket: bucket, Key: key, UploadId: id}, nil
}

// UploadPart buffers one part (ADR-0080): s3::write PEP; the upload's total is capped by maxUpload, and its
// owner's parts and the daemon's by the multipart budget (ADR-0188).
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
	if perr := b.mp.putPart(deref(in.UploadId), uploadTarget{ns: pr.namespace, bucket: bucket, key: key}, num, data); perr != nil {
		return nil, perr
	}
	return &awss3.UploadPartOutput{ETag: ptr(etag(data))}, nil
}

// CompleteMultipartUpload assembles the buffered parts and Puts the object once with the upload's
// Content-Type and user metadata (ADR-0080): s3::write PEP, total bounded by maxUpload (fail-closed).
// If-Match and If-None-Match are evaluated by writePreconditions after assembly, immediately before the
// Put (ADR-0159); a refused Complete leaves the upload in place for a retry.
func (b *be) CompleteMultipartUpload(ctx context.Context, in *awss3.CompleteMultipartUploadInput) (s3response.CompleteMultipartUploadResult, string, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket, key := deref(in.Bucket), deref(in.Key)
	prefix, _ := splitKey(key)
	sub, pr, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return s3response.CompleteMultipartUploadResult{}, "", err
	}
	target := uploadTarget{ns: pr.namespace, bucket: bucket, key: key}
	id := deref(in.UploadId)
	data, opts, aerr := b.mp.assemble(id, target, in.MultipartUpload)
	if aerr != nil {
		return s3response.CompleteMultipartUploadResult{}, "", aerr
	}
	n := int64(len(data))
	if n > b.maxUpload {
		b.mp.release(id, target, n, true)
		return s3response.CompleteMultipartUploadResult{}, "", s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	if cerr := writeConditions(ctx, sub, key, in.IfMatch, in.IfNoneMatch); cerr != nil {
		b.mp.release(id, target, n, false)
		return s3response.CompleteMultipartUploadResult{}, "", cerr
	}
	if perr := sub.Put(ctx, key, data, opts); perr != nil {
		b.mp.release(id, target, n, false)
		return s3response.CompleteMultipartUploadResult{}, "", mapBlobErr(perr)
	}
	b.mp.release(id, target, n, true)
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
// entity-tag). It equals the objectETag of the digest the driver stores for data, so the gateway has one form.
func etag(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // content fingerprint, not security
	return objectETag(sum[:])
}

// objectETag is the quoted lowercase hex ETag of digest, or "" when digest is nil or empty; the caller
// then sets a nil ETag, so no header and no listing element (ADR-0159).
func objectETag(digest []byte) string {
	if len(digest) == 0 {
		return ""
	}
	return `"` + hex.EncodeToString(digest) + `"`
}

// readPreconditions evaluates a GET or HEAD's conditions in RFC 9110 §13.2.2 order (ADR-0159): If-Match,
// then the dates (datePreconditions, which skips a date condition its ETag condition decides), then
// If-None-Match. Without a digest the ETag is "", so only If-Match: * matches it and an entity-tag
// If-None-Match matches nothing. The error is NotModified (304) or PreconditionFailed (412); the caller
// returns it with a nil output, so no output header (a non-nil one adds x-amz-delete-marker).
func readPreconditions(attrs blob.Attributes, c backend.PreConditions) error {
	tag := objectETag(attrs.MD5)
	if err := backend.EvaluatePreconditions(tag, attrs.ModTime, backend.PreConditions{IfMatch: c.IfMatch}); err != nil {
		return err
	}
	if err := datePreconditions(attrs.ModTime, c); err != nil {
		return err
	}
	return backend.EvaluatePreconditions(tag, attrs.ModTime, backend.PreConditions{IfNoneMatch: c.IfNoneMatch})
}

// writePreconditions answers a PUT or CompleteMultipartUpload's If-Match and If-None-Match (ADR-0159):
// nil, PreconditionFailed (412), NoSuchKey (404) or NotImplemented (501). versitygw's evaluator compares
// If-Match: * as an entity tag, so * alone is answered here: the write proceeds when the object exists.
func writePreconditions(attrs blob.Attributes, found bool, ifMatch, ifNoneMatch *string) error {
	if ifNoneMatch == nil && deref(ifMatch) == "*" {
		if !found {
			return s3err.GetAPIError(s3err.ErrNoSuchKey)
		}
		return nil
	}
	return backend.EvaluateObjectPutPreconditions(objectETag(attrs.MD5), ifMatch, ifNoneMatch, found)
}
