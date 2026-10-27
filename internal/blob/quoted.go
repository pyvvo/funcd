package blob

import (
	"context"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/quota"
)

// Quoted returns a view of inner whose Put asks chk before it stores (ADR-0292): the key's prefix, then the Bucket,
// each a subject of its own, and a refusal by the Bucket releases the prefix's reservation. A delete reports itself to
// both; reads pass through. capped reports whether the Bucket (prefix "") or a prefix sets a size; a presigned PUT under one is
// fault.Forbidden, since its upload bypasses Put, as Capped refuses one. It forwards RangeReader.
func Quoted(inner Bucket, chk quota.Checker, bucket quota.Subject, capped func(prefix string) bool) Bucket {
	q := &quotedBucket{Bucket: inner, chk: chk, subject: bucket, capped: capped}
	if rr, ok := inner.(RangeReader); ok {
		return &quotedRangeBucket{quotedBucket: q, rr: rr}
	}
	return q
}

type quotedBucket struct {
	Bucket
	chk     quota.Checker
	subject quota.Subject
	capped  func(prefix string) bool
}

// prefixOf is a key's leading segment, the Bucket prefix it lies in (the S3 gateway's splitKey).
func prefixOf(key string) string {
	p, _, _ := strings.Cut(key, "/")
	return p
}

func (q *quotedBucket) Put(ctx context.Context, key string, data []byte, opts PutOptions) error {
	replaces := func(ctx context.Context) (int64, error) {
		a, err := q.Attributes(ctx, key)
		if fault.KindOf(err) == fault.NotFound {
			return 0, nil
		}
		return a.Size, err
	}
	n := int64(len(data))
	pfx := q.subject
	pfx.Prefix = prefixOf(key)
	settlePrefix, err := q.chk.Admit(ctx, quota.Request{Subject: pfx, Bytes: n, Replaces: replaces})
	if err != nil {
		return err
	}
	settleBucket, err := q.chk.Admit(ctx, quota.Request{Subject: q.subject, Bytes: n, Replaces: replaces})
	if err != nil {
		settlePrefix(false)
		return err
	}
	err = q.Bucket.Put(ctx, key, data, opts)
	settleBucket(err == nil)
	settlePrefix(err == nil)
	return err
}

// Delete reports a stored delete to the key's prefix and the Bucket, so their next refusal measures again.
func (q *quotedBucket) Delete(ctx context.Context, key string) error {
	pfx := q.subject
	pfx.Prefix = prefixOf(key)
	settlePrefix, perr := q.chk.Admit(ctx, quota.Request{Subject: pfx, Deletes: true})
	settleBucket, berr := q.chk.Admit(ctx, quota.Request{Subject: q.subject, Deletes: true})
	err := q.Bucket.Delete(ctx, key)
	if perr == nil {
		settlePrefix(err == nil)
	}
	if berr == nil {
		settleBucket(err == nil)
	}
	return err
}

func (q *quotedBucket) SignedURL(ctx context.Context, key string, opts SignOptions) (string, error) {
	if opts.Method == SignPut && (q.capped("") || q.capped(prefixOf(key))) {
		return "", fault.Forbiddenf("blob.Quoted.SignedURL", "a presigned PUT for %q cannot enforce the quota of %s", key, q.subject)
	}
	return q.Bucket.SignedURL(ctx, key, opts)
}

type quotedRangeBucket struct {
	*quotedBucket
	rr RangeReader
}

func (q *quotedRangeBucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return q.rr.GetRange(ctx, key, offset, length)
}
