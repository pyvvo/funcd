package s3gateway

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/versity/versitygw/backend"
	"github.com/versity/versitygw/s3err"
	"github.com/versity/versitygw/s3response"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
)

// region is the single S3 region funcd's gateway advertises (ADR-0085); DuckDB and
// the AWS SDK require one but funcd is single-region.
const region = "us-east-1"

// be is the funcd S3 backend over the blob.Bucket substrate (ADR-0080). It embeds
// BackendUnsupported (every non-overridden op ⇒ NotImplemented) and overrides only
// the DuckDB/DuckLake subset. EVERY override is a PEP: it resolves the principal from
// the request account, builds the (principal, s3::action, bucket/prefix) request, and
// calls the cedar PDP BEFORE touching blob — !Allowed ⇒ S3 AccessDenied (403).
type be struct {
	backend.BackendUnsupported
	bucketFor func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) // (ns, Bucket) → substrate bucket
	buckets   func(ctx context.Context, ns v1.NamespaceName) ([]v1.Bucket, error)
	pdp       authz.Authorizer
	external  ExternalKeys
	maxUpload int64
	log       *slog.Logger
	mp        *multipartStore
	life      context.Context // canceled when the gateway closes
}

var _ backend.Backend = (*be)(nil)

// opContext gives a backend op its own context with the request's values (issue 462).
// versitygw hands the backend fasthttp's pooled RequestCtx, whose Done reads server state that
// Close rewrites unsynchronized, so a context derived from it races the shutdown. The op's
// context ends when the op returns or the gateway closes.
func (b *be) opContext(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(b.life, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// accessDenied is the S3 403 the PEP returns on a deny (ADR-0080).
func accessDenied() error { return s3err.GetAPIError(s3err.ErrAccessDenied) }

// splitKey separates an S3 object key into its leading prefix sub-domain and the rest
// (ADR-0080): "gold/q.parquet" → ("gold", "q.parquet"). A key with no "/" is the
// prefix itself with an empty object path. The substrate key is the S3 key itself:
// rebuilding it from the split would store the folder marker "gold/" as "gold" (issue 709).
func splitKey(key string) (prefix, object string) {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// authorize is the PEP (ADR-0080): it resolves the principal, builds the Cedar
// request for (action, bucket, prefix), and consults the PDP. It returns the resolved
// (ns, bucket) substrate handle on Allow, or an S3 error on deny / missing bucket.
func (b *be) authorize(ctx context.Context, action authz.Action, bucket, prefix string) (blob.Bucket, principal, error) {
	pr, err := b.caller(ctx)
	if err != nil {
		return nil, principal{}, err
	}
	ok, err := b.allowed(ctx, pr, action, bucket, prefix)
	if err != nil {
		return nil, principal{}, err
	}
	if !ok {
		return nil, principal{}, accessDenied()
	}
	sub, ok := b.bucketFor(pr.namespace, bucket)
	if !ok {
		return nil, pr, s3err.GetAPIError(s3err.ErrNoSuchBucket)
	}
	return sub, pr, nil
}

// caller resolves the request's principal; an unauthenticated or unknown caller is denied.
func (b *be) caller(ctx context.Context) (principal, error) {
	acct, ok := accountFromCtx(ctx)
	if !ok {
		return principal{}, accessDenied()
	}
	pr, err := principalFor(acct, b.external)
	if err != nil {
		return principal{}, accessDenied()
	}
	return pr, nil
}

// allowed asks the PDP whether pr may perform action on bucket/prefix. The error is
// an S3 InternalError when the PDP itself fails.
func (b *be) allowed(ctx context.Context, pr principal, action authz.Action, bucket, prefix string) (bool, error) {
	req := authz.Request{
		Identity: authz.Identity{Principal: &pr.ref},
		Action:   action,
		Resource: &authz.EntityRef{
			Type:      v1.KindBucket,
			Namespace: pr.namespace,
			Name:      v1.ObjectName(bucket),
			Path:      prefix,
		},
	}
	dec, err := b.pdp.Authorize(ctx, req)
	if err != nil {
		b.log.Error("s3 PEP error", "action", action, "bucket", bucket, "prefix", prefix, "err", err)
		return false, s3err.GetAPIError(s3err.ErrInternalError)
	}
	if !dec.Allowed {
		b.log.Debug("s3 PEP denied", "action", action, "bucket", bucket, "prefix", prefix)
	}
	return dec.Allowed, nil
}

// mapBlobErr translates a blob fault to the closest S3 error (ADR-0080).
func mapBlobErr(err error) error {
	switch fault.KindOf(err) {
	case fault.NotFound:
		return s3err.GetAPIError(s3err.ErrNoSuchKey)
	case fault.Forbidden:
		return accessDenied()
	case fault.Invalid:
		return s3err.GetAPIError(s3err.ErrInvalidRequest)
	case fault.PayloadTooLarge:
		return s3err.GetAPIError(s3err.ErrEntityTooLarge)
	default:
		return s3err.GetAPIError(s3err.ErrInternalError)
	}
}

// --- Reads -----------------------------------------------------------------------

// GetObject serves a GET (ADR-0080), honoring a byte-range via GetObjectInput.Range.
// Last-Modified, the ETag, Content-Type and the user metadata come from blob.Stat, the values HEAD and the
// listing report (ADR-0159): the ETag is the stored digest, so a ranged GET carries the object's ETag and
// still reads only its range. A driver implementing blob.RangeReader serves the range directly; otherwise
// the gateway falls back to a full Get + slice (rangereader-fallback scenario).
func (b *be) GetObject(ctx context.Context, in *awss3.GetObjectInput) (*awss3.GetObjectOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket, key := deref(in.Bucket), deref(in.Key)
	prefix, _ := splitKey(key)
	sub, _, err := b.authorize(ctx, authz.ActionS3Read, bucket, prefix)
	if err != nil {
		return nil, err
	}
	attrs, found, err := blob.Stat(ctx, sub, key)
	if err != nil {
		return nil, mapBlobErr(err)
	}
	if !found {
		return nil, s3err.GetAPIError(s3err.ErrNoSuchKey)
	}
	if cerr := readPreconditions(attrs, backend.PreConditions{
		IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch, IfModSince: in.IfModifiedSince, IfUnmodeSince: in.IfUnmodifiedSince,
	}); cerr != nil {
		return nil, cerr
	}

	var data []byte
	var contentRange *string
	if rng := deref(in.Range); rng != "" {
		data, contentRange, err = getRange(ctx, sub, key, rng, attrs.Size)
	} else if data, err = sub.Get(ctx, key); err != nil {
		err = mapBlobErr(err)
	}
	if err != nil {
		return nil, err
	}

	return &awss3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: ptr(int64(len(data))),
		ContentRange:  contentRange,
		LastModified:  ptr(attrs.ModTime.UTC()),
		AcceptRanges:  ptr("bytes"),
		ETag:          backend.GetPtrFromString(objectETag(attrs.MD5)),
		ContentType:   backend.GetPtrFromString(attrs.ContentType),
		Metadata:      attrs.Metadata,
	}, nil
}

// getRange serves a Range header the way S3 does (RFC 9110 §14): the object size bounds
// the range, a range starting at or past the end is a 416, and Content-Range carries the
// complete length. It reads only the range through the optional blob.RangeReader when the
// driver has one, bounded by the listed size, else a full Get + slice (ADR-0080
// rangereader-fallback). Errors are S3 errors.
func getRange(ctx context.Context, sub blob.Bucket, key, header string, size int64) ([]byte, *string, error) {
	rr, ranger := sub.(blob.RangeReader)
	var full []byte
	var err error
	if !ranger {
		if full, err = sub.Get(ctx, key); err != nil {
			return nil, nil, mapBlobErr(err)
		}
		size = int64(len(full))
	}

	offset, length, valid, err := backend.ParseObjectRange(size, header)
	if err != nil {
		return nil, nil, err
	}
	var data []byte
	switch {
	case !ranger:
		data = full[offset : offset+length]
	case valid:
		data, err = rr.GetRange(ctx, key, offset, length)
	default:
		data, err = sub.Get(ctx, key)
	}
	if err != nil {
		return nil, nil, mapBlobErr(err)
	}
	if !valid {
		return data, nil, nil
	}
	return data, ptr(fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size)), nil
}

// HeadObject serves a HEAD (ADR-0080): a read-authorized metadata probe answered from the object's
// attributes, never its body. Its ETag is the stored digest a GET and the listing report (ADR-0159), so
// DuckDB's per-read ETag check holds from the first HEAD.
func (b *be) HeadObject(ctx context.Context, in *awss3.HeadObjectInput) (*awss3.HeadObjectOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket, key := deref(in.Bucket), deref(in.Key)
	prefix, _ := splitKey(key)
	sub, _, err := b.authorize(ctx, authz.ActionS3Read, bucket, prefix)
	if err != nil {
		return nil, err
	}
	attrs, found, serr := blob.Stat(ctx, sub, key)
	if serr != nil {
		return nil, mapBlobErr(serr)
	}
	if !found {
		return nil, s3err.GetAPIError(s3err.ErrNoSuchKey)
	}
	if cerr := readPreconditions(attrs, backend.PreConditions{
		IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch, IfModSince: in.IfModifiedSince, IfUnmodeSince: in.IfUnmodifiedSince,
	}); cerr != nil {
		return nil, cerr
	}
	return &awss3.HeadObjectOutput{
		ContentLength: ptr(attrs.Size),
		LastModified:  ptr(attrs.ModTime.UTC()),
		ETag:          backend.GetPtrFromString(objectETag(attrs.MD5)),
		ContentType:   backend.GetPtrFromString(attrs.ContentType),
		Metadata:      attrs.Metadata,
	}, nil
}

// maxListXML bounds the encoded entries of a listing page: versitygw answers a response body over
// 4 MiB with 500 InternalError (maxXMLBodyLen in s3api/controllers). The 64 KiB left holds the rest
// of the page, whose prefix, marker and delimiter echo a request that fits an 8 KiB header.
const maxListXML = 4<<20 - 64<<10

// listPage is one S3 listing page: the keys and common prefixes after a marker, at most
// limit entries and maxListXML encoded bytes in all, with the marker that resumes the
// listing when truncated.
type listPage struct {
	contents  []s3response.Object
	prefixes  []awstypes.CommonPrefix
	truncated bool
	next      string
}

// list serves one listing page and keeps no state between requests (ADR-0184): it authorizes the Prefix's
// leading segment once, then seeks storage from the marker with ListAfter, each call asking for the page's
// remaining slots. The next marker is the page's last entry, a key or a common prefix.
func (b *be) list(ctx context.Context, action authz.Action, bucket, prefix, delimiter, marker string, limit int32) (listPage, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	segment, _ := splitKey(prefix)
	sub, _, err := b.authorize(ctx, action, bucket, segment)
	if err != nil {
		return listPage{}, err
	}
	var p listPage
	if limit <= 0 {
		return p, nil
	}
	after := marker
	if cp := commonPrefix(marker, prefix, delimiter); cp != "" {
		// Every key under the marker's own common prefix rolls up into it, which a page already returned.
		after = cp + string(utf8.MaxRune)
	}
	var last, lastCP string
	size := 0
	for {
		items, more, lerr := sub.ListAfter(ctx, prefix, after, int(limit)-len(p.contents)-len(p.prefixes))
		if lerr != nil {
			return listPage{}, mapBlobErr(lerr)
		}
		for _, it := range items {
			key := it.Key
			// Only the leading segment is authorized: golden/… is not under a Prefix of gold.
			if seg, _ := splitKey(key); seg != segment {
				if key > segment+"/" {
					return p, nil
				}
				continue
			}
			cp := commonPrefix(key, prefix, delimiter)
			if cp != "" && (cp <= marker || cp == last) {
				continue
			}
			o := s3response.Object{
				Key:          ptr(key),
				Size:         ptr(it.Size),
				LastModified: ptr(it.ModTime),
				ETag:         backend.GetPtrFromString(objectETag(it.MD5)),
				StorageClass: awstypes.ObjectStorageClassStandard,
			}
			n, err := entryLen(o, cp)
			if err != nil {
				return listPage{}, err
			}
			// The entry must fit with its name again as the next marker; the first one always goes, so the listing advances.
			count := int32(len(p.contents) + len(p.prefixes))
			if count == limit || (count > 0 && size+2*n > maxListXML) {
				p.truncated = true
				p.next = last
				return p, nil
			}
			size += n
			if cp != "" {
				p.prefixes = append(p.prefixes, awstypes.CommonPrefix{Prefix: ptr(cp)})
				last, lastCP = cp, cp
			} else {
				p.contents = append(p.contents, o)
				last = key
			}
		}
		if !more {
			return p, nil
		}
		if int32(len(p.contents)+len(p.prefixes)) == limit {
			p.truncated = true
			p.next = last
			return p, nil
		}
		if len(items) > 0 {
			after = items[len(items)-1].Key
		}
		if lastCP != "" && strings.HasPrefix(after, lastCP) {
			after = lastCP + string(utf8.MaxRune)
		}
	}
}

// commonPrefix is the common prefix key rolls up into under prefix with delimiter, or "" when it rolls up
// into none: no delimiter, key outside prefix, or no delimiter in the rest.
func commonPrefix(key, prefix, delimiter string) string {
	if delimiter == "" || !strings.HasPrefix(key, prefix) {
		return ""
	}
	before, _, ok := strings.Cut(key[len(prefix):], delimiter)
	if !ok {
		return ""
	}
	return prefix + before + delimiter
}

// entryLen is the size of a listing entry as versitygw encodes the page with encoding/xml: the
// common prefix cp when set, else the object o.
func entryLen(o s3response.Object, cp string) (int, error) {
	var b bytes.Buffer
	enc := xml.NewEncoder(&b)
	var err error
	if cp != "" {
		err = enc.EncodeElement(awstypes.CommonPrefix{Prefix: &cp}, xml.StartElement{Name: xml.Name{Local: "CommonPrefixes"}})
	} else {
		err = enc.EncodeElement(o, xml.StartElement{Name: xml.Name{Local: "Contents"}})
	}
	return b.Len(), err
}

// pageSize is the request's MaxKeys, or the S3 default of 1000 when it names none.
func pageSize(maxKeys *int32) int32 {
	if maxKeys == nil {
		return 1000
	}
	return *maxKeys
}

// ListObjectsV2 lists objects under a bound prefix (ADR-0080 listobjects-glob). The
// S3 Prefix's leading segment selects the sub-domain; the rest filters within it.
func (b *be) ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input) (s3response.ListObjectsV2Result, error) {
	bucket := deref(in.Bucket)
	limit := pageSize(in.MaxKeys)
	marker := max(deref(in.StartAfter), deref(in.ContinuationToken))
	p, err := b.list(ctx, authz.ActionS3Read, bucket, deref(in.Prefix), deref(in.Delimiter), marker, limit)
	if err != nil {
		return s3response.ListObjectsV2Result{}, err
	}
	return s3response.ListObjectsV2Result{
		Name:                  ptr(bucket),
		Prefix:                in.Prefix,
		StartAfter:            backend.GetPtrFromString(deref(in.StartAfter)),
		ContinuationToken:     backend.GetPtrFromString(deref(in.ContinuationToken)),
		NextContinuationToken: backend.GetPtrFromString(p.next),
		Delimiter:             backend.GetPtrFromString(deref(in.Delimiter)),
		Contents:              p.contents,
		CommonPrefixes:        p.prefixes,
		KeyCount:              ptr(int32(len(p.contents) + len(p.prefixes))),
		MaxKeys:               ptr(limit),
		IsTruncated:           ptr(p.truncated),
	}, nil
}

// ListObjects is the V1 listing (ADR-0080), same semantics as V2 with Marker.
func (b *be) ListObjects(ctx context.Context, in *awss3.ListObjectsInput) (s3response.ListObjectsResult, error) {
	bucket := deref(in.Bucket)
	limit := pageSize(in.MaxKeys)
	p, err := b.list(ctx, authz.ActionS3Read, bucket, deref(in.Prefix), deref(in.Delimiter), deref(in.Marker), limit)
	if err != nil {
		return s3response.ListObjectsResult{}, err
	}
	return s3response.ListObjectsResult{
		Name:           ptr(bucket),
		Prefix:         in.Prefix,
		Marker:         backend.GetPtrFromString(deref(in.Marker)),
		NextMarker:     backend.GetPtrFromString(p.next),
		Delimiter:      backend.GetPtrFromString(deref(in.Delimiter)),
		Contents:       p.contents,
		CommonPrefixes: p.prefixes,
		MaxKeys:        ptr(limit),
		IsTruncated:    ptr(p.truncated),
	}, nil
}

// --- Writes ----------------------------------------------------------------------

// PutObject writes an object (ADR-0080): s3::write, single-writer (owner). The body is
// buffered (bounded by maxUpload) then Put once with its Content-Type and user metadata — the blob
// port has no streaming seam. If-Match and If-None-Match are evaluated by writePreconditions after
// buffering, immediately before the Put (ADR-0159).
func (b *be) PutObject(ctx context.Context, in s3response.PutObjectInput) (s3response.PutObjectOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket, key := deref(in.Bucket), deref(in.Key)
	prefix, _ := splitKey(key)
	sub, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return s3response.PutObjectOutput{}, err
	}
	data, rerr := b.readCapped(in.Body)
	if rerr != nil {
		return s3response.PutObjectOutput{}, rerr
	}
	if cerr := writeConditions(ctx, sub, key, in.IfMatch, in.IfNoneMatch); cerr != nil {
		return s3response.PutObjectOutput{}, cerr
	}
	if perr := sub.Put(ctx, key, data, blob.PutOptions{ContentType: deref(in.ContentType), Metadata: in.Metadata}); perr != nil {
		return s3response.PutObjectOutput{}, mapBlobErr(perr)
	}
	return s3response.PutObjectOutput{ETag: etag(data)}, nil
}

// DeleteObject removes an object (ADR-0080): s3::write.
func (b *be) DeleteObject(ctx context.Context, in *awss3.DeleteObjectInput) (*awss3.DeleteObjectOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket, key := deref(in.Bucket), deref(in.Key)
	prefix, _ := splitKey(key)
	sub, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
	if err != nil {
		return nil, err
	}
	if derr := sub.Delete(ctx, key); derr != nil && fault.KindOf(derr) != fault.NotFound {
		return nil, mapBlobErr(derr)
	}
	return &awss3.DeleteObjectOutput{}, nil
}

// DeleteObjects is the batch delete (ADR-0080): each key is a per-object s3::write PEP.
func (b *be) DeleteObjects(ctx context.Context, in *awss3.DeleteObjectsInput) (s3response.DeleteResult, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	bucket := deref(in.Bucket)
	var res s3response.DeleteResult
	if in.Delete == nil {
		return res, nil
	}
	for _, obj := range in.Delete.Objects {
		key := deref(obj.Key)
		prefix, _ := splitKey(key)
		sub, _, err := b.authorize(ctx, authz.ActionS3Write, bucket, prefix)
		if err != nil {
			res.Error = append(res.Error, awstypes.Error{Key: ptr(key), Code: ptr("AccessDenied"), Message: ptr("access denied")})
			continue
		}
		if derr := sub.Delete(ctx, key); derr != nil && fault.KindOf(derr) != fault.NotFound {
			res.Error = append(res.Error, awstypes.Error{Key: ptr(key), Code: ptr("InternalError"), Message: ptr("delete failed")})
			continue
		}
		res.Deleted = append(res.Deleted, awstypes.DeletedObject{Key: ptr(key)})
	}
	return res, nil
}

// --- Buckets ---------------------------------------------------------------------

// namespaceBuckets lists the Bucket resources of the caller's namespace.
func (b *be) namespaceBuckets(ctx context.Context, pr principal) ([]v1.Bucket, error) {
	all, err := b.buckets(ctx, pr.namespace)
	if err != nil {
		b.log.Error("s3 list buckets", "namespace", pr.namespace, "err", err)
		return nil, s3err.GetAPIError(s3err.ErrInternalError)
	}
	return all, nil
}

// bound reports whether pr is bound to bkt (ADR-0080): the PDP lets it s3::read at
// least one of the Bucket's prefixes.
func (b *be) bound(ctx context.Context, pr principal, bkt v1.Bucket) (bool, error) {
	for _, p := range bkt.Spec.Prefixes {
		ok, err := b.allowed(ctx, pr, authz.ActionS3Read, string(bkt.Name), p.Name)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// HeadBucket succeeds iff a Bucket of that name exists in the caller's namespace and
// the caller is bound to it (ADR-0080). Like the object PEP, any other case is 403, so
// an unbound caller cannot tell an existing bucket from a missing one.
func (b *be) HeadBucket(ctx context.Context, in *awss3.HeadBucketInput) (*awss3.HeadBucketOutput, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	pr, err := b.caller(ctx)
	if err != nil {
		return nil, err
	}
	all, err := b.namespaceBuckets(ctx, pr)
	if err != nil {
		return nil, err
	}
	for _, bkt := range all {
		if string(bkt.Name) != deref(in.Bucket) {
			continue
		}
		ok, berr := b.bound(ctx, pr, bkt)
		if berr != nil {
			return nil, berr
		}
		if ok {
			return &awss3.HeadBucketOutput{}, nil
		}
	}
	return nil, accessDenied()
}

// ListBuckets returns the Buckets the caller is bound to (ADR-0080), filtered by the
// request prefix.
func (b *be) ListBuckets(ctx context.Context, in s3response.ListBucketsInput) (s3response.ListAllMyBucketsResult, error) {
	ctx, end := b.opContext(ctx)
	defer end()
	pr, err := b.caller(ctx)
	if err != nil {
		return s3response.ListAllMyBucketsResult{}, err
	}
	all, err := b.namespaceBuckets(ctx, pr)
	if err != nil {
		return s3response.ListAllMyBucketsResult{}, err
	}
	res := s3response.ListAllMyBucketsResult{Prefix: in.Prefix}
	for _, bkt := range all {
		if !strings.HasPrefix(string(bkt.Name), in.Prefix) {
			continue
		}
		ok, berr := b.bound(ctx, pr, bkt)
		if berr != nil {
			return s3response.ListAllMyBucketsResult{}, berr
		}
		if ok {
			res.Buckets.Bucket = append(res.Buckets.Bucket, s3response.ListAllMyBucketsEntry{
				Name:         string(bkt.Name),
				CreationDate: bkt.CreationTime,
			})
		}
	}
	return res, nil
}

// CreateBucket is Forbidden over S3 (ADR-0080): Buckets are managed via the control plane.
func (b *be) CreateBucket(context.Context, *awss3.CreateBucketInput, []byte) error {
	return accessDenied()
}

// DeleteBucket is Forbidden over S3 (ADR-0080): Buckets are managed via the control plane.
func (b *be) DeleteBucket(context.Context, string) error {
	return accessDenied()
}

// --- ACL/policy pass-through -----------------------------------------------------
//
// funcd's authorization is ENTIRELY the Cedar PEP in the object methods above
// (ADR-0080). versitygw runs its own bucket-ACL + bucket-policy gate in front of the
// controllers, calling these two backend methods; funcd makes that built-in gate a
// permissive pass-through so it never shadows the Cedar decision:
//   - GetBucketPolicy returns NoSuchBucketPolicy (no S3 bucket policy on any bucket).
//   - GetBucketAcl returns an ACL whose Owner is the CALLER, so versitygw's
//     DisableACL ownership check (acl.Owner == access) always passes — every real
//     authz decision is then made by the Cedar PEP.

// GetBucketPolicy reports no bucket policy (ADR-0080): authz is the Cedar PEP, not S3
// bucket policies. Returning ErrNoSuchBucketPolicy lets versitygw's VerifyAccess fall
// through to the (pass-through) ACL check.
func (b *be) GetBucketPolicy(context.Context, string) ([]byte, error) {
	return nil, s3err.GetAPIError(s3err.ErrNoSuchBucketPolicy)
}

// GetBucketAcl returns a caller-owned ACL (ADR-0080): with WithDisableACL the gateway
// only checks acl.Owner == caller access, so a caller-owned ACL always passes and the
// Cedar PEP becomes the sole authorization gate. The caller's access is read from the
// authenticated account on the request context.
func (b *be) GetBucketAcl(ctx context.Context, _ *awss3.GetBucketAclInput) ([]byte, error) {
	owner := ""
	if acct, ok := accountFromCtx(ctx); ok {
		owner = acct.Access
	}
	return []byte(`{"Owner":` + strconv.Quote(owner) + `}`), nil
}

// GetObjectLockConfiguration reports no object-lock config (ADR-0080): funcd does not
// implement S3 object-lock, and versitygw's write path (CheckObjectAccess) treats
// ErrObjectLockConfigurationNotFound as "no lock" and proceeds. Without this override
// the BackendUnsupported default returns NotImplemented (501) and blocks every write.
func (b *be) GetObjectLockConfiguration(context.Context, string) ([]byte, error) {
	return nil, s3err.GetAPIError(s3err.ErrObjectLockConfigurationNotFound)
}

// GetBucketVersioning reports versioning unset (ADR-0080): funcd does not implement
// object versioning. An empty output (no Status) makes versitygw's overwrite path treat
// the bucket as unversioned, so writes proceed to the Cedar PEP + blob.Put.
func (b *be) GetBucketVersioning(context.Context, string) (s3response.GetBucketVersioningOutput, error) {
	return s3response.GetBucketVersioningOutput{}, nil
}

// --- helpers ---------------------------------------------------------------------

// datePreconditions evaluates If-Unmodified-Since (412) and If-Modified-Since (304) against modTime
// at the one-second precision of the Last-Modified header, in RFC 9110 §13.2.2 order. A date condition
// is skipped when the ETag condition that takes precedence over it under §13.2.2 is present
// (If-Match over If-Unmodified-Since, If-None-Match over If-Modified-Since); readPreconditions evaluates
// that one. versitygw's EvaluatePreconditions is not used because it fails an If-Unmodified-Since equal
// to Last-Modified.
func datePreconditions(modTime time.Time, pc backend.PreConditions) error {
	modTime = modTime.Truncate(time.Second)
	if pc.IfMatch == nil && pc.IfUnmodeSince != nil && modTime.After(*pc.IfUnmodeSince) {
		return s3err.GetPreconditionFailedErr(s3err.ConditionIfUnmodifiedSince)
	}
	if pc.IfNoneMatch == nil && pc.IfModSince != nil && !modTime.After(*pc.IfModSince) {
		return s3err.GetAPIError(s3err.ErrNotModified)
	}
	return nil
}

// writeConditions answers a write's If-Match and If-None-Match from the attributes of the object at key
// (writePreconditions), reading them only when one is set. The check and the Put are not atomic, as the
// blob port has no conditional Put (ADR-0159).
func writeConditions(ctx context.Context, sub blob.Bucket, key string, ifMatch, ifNoneMatch *string) error {
	if ifMatch == nil && ifNoneMatch == nil {
		return nil
	}
	attrs, found, err := blob.Stat(ctx, sub, key)
	if err != nil {
		return mapBlobErr(err)
	}
	return writePreconditions(attrs, found, ifMatch, ifNoneMatch)
}

// readCapped reads r into memory bounded by maxUpload (fail-closed: a body past the
// cap is rejected, never OOMs the daemon — ADR-0080 Temporary workarounds).
func (b *be) readCapped(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	limit := b.maxUpload
	lr := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, s3err.GetAPIError(s3err.ErrInternalError)
	}
	if int64(len(data)) > limit {
		return nil, s3err.GetAPIError(s3err.ErrEntityTooLarge)
	}
	return data, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ptr returns a pointer to v — a local helper for the many *string/*int64 AWS SDK
// fields. The `any` constraint is the idiomatic generic-pointer form and is not an
// exported/port signature.
//
//nolint:forbidigo // generic pointer helper; `any` here is a type parameter, not a port type
func ptr[T any](v T) *T { return &v }
