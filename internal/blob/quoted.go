package blob

import (
	"context"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/quota"
)

// Quoted returns a view of inner whose Put asks chk before it stores (ADR-0292): the key's prefix, then the Bucket,
// each a subject of its own and asked only when capped reports a size for it, so a key's leading segment that no quota
// names is never tracked; a refusal by the Bucket releases the prefix's reservation. A delete reports itself to the
// same subjects; reads pass through. capped reports whether the Bucket (prefix "") or a declared prefix sets a size; a
// presigned PUT under one is fault.Forbidden, since its upload bypasses Put, as Capped refuses one. A nil chk is
// quota.None. It forwards RangeReader.
func Quoted(inner Bucket, chk quota.Checker, bucket quota.Subject, capped func(prefix string) bool) Bucket {
	if chk == nil {
		chk = quota.None
	}
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
	settle, err := q.admit(ctx, key, quota.Request{Bytes: int64(len(data)), Replaces: replaces})
	if err != nil {
		return err
	}
	err = q.Bucket.Put(ctx, key, data, opts)
	settle(err == nil)
	return err
}

// Delete reports a stored delete to the capped subjects of key, so their next refusal measures again.
func (q *quotedBucket) Delete(ctx context.Context, key string) error {
	settle, aerr := q.admit(ctx, key, quota.Request{Deletes: true})
	err := q.Bucket.Delete(ctx, key)
	if aerr == nil {
		settle(err == nil)
	}
	return err
}

// admit asks chk for r on each capped subject of key, its prefix first, then the Bucket; a refusal releases what the
// subjects before it admitted.
func (q *quotedBucket) admit(ctx context.Context, key string, r quota.Request) (quota.Settle, error) {
	var settles []quota.Settle
	for _, s := range q.subjects(key) {
		r.Subject = s
		settle, err := q.chk.Admit(ctx, r)
		if err != nil {
			for _, done := range settles {
				done(false)
			}
			return nil, err
		}
		settles = append(settles, settle)
	}
	return func(stored bool) {
		for _, settle := range settles {
			settle(stored)
		}
	}, nil
}

// subjects are the capped subjects a write to key charges: the Bucket prefix it lies in, then the Bucket.
func (q *quotedBucket) subjects(key string) []quota.Subject {
	var out []quota.Subject
	if p := prefixOf(key); p != "" && q.capped(p) {
		s := q.subject
		s.Prefix = p
		out = append(out, s)
	}
	if q.capped("") {
		out = append(out, q.subject)
	}
	return out
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
