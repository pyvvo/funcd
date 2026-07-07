package blob

import (
	"context"
	"strings"
)

// Prefixed returns a Bucket view of inner whose keys are all transparently prefixed
// with keyPrefix (ADR-0080): it scopes a single shared substrate bucket into many
// logical buckets (e.g. one per (namespace, Bucket) S3 domain) without a separate
// backend per bucket. A trailing "/" on keyPrefix is recommended for a clean boundary.
// It forwards the optional RangeReader capability when inner implements it.
func Prefixed(inner Bucket, keyPrefix string) Bucket {
	p := &prefixedBucket{inner: inner, prefix: keyPrefix}
	if _, ok := inner.(RangeReader); ok {
		return &prefixedRangeBucket{prefixedBucket: p}
	}
	return p
}

type prefixedBucket struct {
	inner  Bucket
	prefix string
}

func (p *prefixedBucket) k(key string) string { return p.prefix + key }

func (p *prefixedBucket) Get(ctx context.Context, key string) ([]byte, error) {
	return p.inner.Get(ctx, p.k(key))
}

func (p *prefixedBucket) Put(ctx context.Context, key string, data []byte) error {
	return p.inner.Put(ctx, p.k(key), data)
}

func (p *prefixedBucket) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, p.k(key))
}

func (p *prefixedBucket) Exists(ctx context.Context, key string) (bool, error) {
	return p.inner.Exists(ctx, p.k(key))
}

func (p *prefixedBucket) List(ctx context.Context, prefix string) ([]Attributes, error) {
	items, err := p.inner.List(ctx, p.k(prefix))
	if err != nil {
		return nil, err
	}
	// Strip the view prefix back off so callers see their own key space.
	out := make([]Attributes, 0, len(items))
	for _, it := range items {
		it.Key = strings.TrimPrefix(it.Key, p.prefix)
		out = append(out, it)
	}
	return out, nil
}

func (p *prefixedBucket) SignedURL(ctx context.Context, key string, opts SignOptions) (string, error) {
	return p.inner.SignedURL(ctx, p.k(key), opts)
}

// Close is a no-op for a view: the underlying shared bucket is owned by the daemon and
// closed once, not per logical view.
func (p *prefixedBucket) Close() error { return nil }

// prefixedRangeBucket additionally forwards RangeReader (ADR-0080) when the inner
// bucket implements it, so the s3gateway's ranged-read fast path survives the view.
type prefixedRangeBucket struct{ *prefixedBucket }

func (p *prefixedRangeBucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return p.inner.(RangeReader).GetRange(ctx, p.k(key), offset, length)
}
