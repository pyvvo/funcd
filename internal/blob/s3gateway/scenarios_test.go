package s3gateway_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// lakehouseMeta seeds the canonical fixture (the cedar s3_test mirror): bucket
// "lakehouse" in "default" with prefix "bronze" owned by "etl-svc" and prefix "gold"
// with NO owner (read-only). Functions:
//   - etl-svc owns lakehouse/bronze (no binding — isolates the owner-write path).
//   - analytics BINDS bronze + gold (spec.blob) but owns neither.
//   - reporting binds nothing (unbound default-deny).
func lakehouseMeta() fakeMeta {
	return fakeMeta{
		fns: map[string]*v1.Function{
			"default/reporting": {ObjectMeta: v1.ObjectMeta{Name: "reporting", Namespace: "default", ResourceGroup: "rg1"}},
			"default/etl-svc":   {ObjectMeta: v1.ObjectMeta{Name: "etl-svc", Namespace: "default", ResourceGroup: "rg1"}},
			"default/analytics": {
				ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.FunctionSpec{Blob: []v1.FunctionBlob{
					{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"},
					{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"},
				}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "bronze", Owner: "etl-svc"},
					{Name: "gold"},
				}},
			},
		},
	}
}

// scenario: binding-grants-read (ADR-0080) — analytics, bound to lakehouse/gold via
// spec.blob with NO Policy, reads an object there (ranged GET).
func TestScenarioBindingGrantsRead(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("PAR1-gold-rows"))
	c := g.client(t, "default", "analytics")

	out, err := c.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err, "analytics reads lakehouse/gold via its binding, no Policy")
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	require.Equal(t, "PAR1-gold-rows", string(body))
}

// scenario: owner-writes + non-owner denied (ADR-0080) — the owner etl-svc writes
// lakehouse/bronze; a bound non-owner (analytics) writing the same prefix is 403.
func TestScenarioOwnerWrites(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()

	owner := g.client(t, "default", "etl-svc")
	_, err := owner.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("bronze/x.parquet"), Body: bytes.NewReader([]byte("rows")),
	})
	require.NoError(t, err, "the owner etl-svc writes lakehouse/bronze")
	require.Equal(t, []byte("rows"), mustGet(t, g, "default", "lakehouse", "bronze/x.parquet"))

	nonOwner := g.client(t, "default", "analytics")
	_, err = nonOwner.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("bronze/y.parquet"), Body: bytes.NewReader([]byte("nope")),
	})
	require.Error(t, err, "a bound non-owner write is denied")
	require.Equal(t, 403, statusCode(err))
}

// scenario: unbound-denied (ADR-0080) — reporting has no spec.blob binding ⇒ s3::read
// default-deny (403); no blob is touched.
func TestScenarioUnboundDenied(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("rows"))
	c := g.client(t, "default", "reporting")

	_, err := c.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.Error(t, err, "reporting is unbound ⇒ default-deny")
	require.Equal(t, 403, statusCode(err))
}

// scenario: listobjects-glob (ADR-0080) — several Parquet under a bound silver-like
// prefix; ListObjectsV2 returns all matching keys.
func TestScenarioListObjectsGlob(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	for _, k := range []string{"gold/a.parquet", "gold/b.parquet", "gold/c.parquet"} {
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	c := g.client(t, "default", "analytics")

	out, err := c.ListObjectsV2(context.Background(), &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/")})
	require.NoError(t, err)
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, *o.Key)
	}
	require.ElementsMatch(t, []string{"gold/a.parquet", "gold/b.parquet", "gold/c.parquet"}, keys)
}

// scenario: cross-namespace-rejected (ADR-0080) — a principal scoped to namespace
// "default" requesting a bucket in namespace "other" is denied (tenancy default-deny):
// the bucket name resolves under the CALLER's namespace, where it does not exist.
func TestScenarioCrossNamespaceRejected(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	c := g.client(t, "default", "analytics")

	// "warehouse" is not a Bucket in the caller's namespace; the PEP denies (no binding).
	_, err := c.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("warehouse"), Key: ptrS("gold/q.parquet")})
	require.Error(t, err, "a cross-namespace / unknown bucket is denied")
	require.Equal(t, 403, statusCode(err))
}

// noRangeBucket wraps a blob.Bucket but deliberately does NOT expose blob.RangeReader,
// forcing the s3gateway's full-Get+slice fallback.
type noRangeBucket struct{ inner blob.Bucket }

func (n noRangeBucket) Get(ctx context.Context, key string) ([]byte, error) {
	return n.inner.Get(ctx, key)
}
func (n noRangeBucket) Put(ctx context.Context, key string, data []byte) error {
	return n.inner.Put(ctx, key, data)
}
func (n noRangeBucket) Delete(ctx context.Context, key string) error { return n.inner.Delete(ctx, key) }
func (n noRangeBucket) Exists(ctx context.Context, key string) (bool, error) {
	return n.inner.Exists(ctx, key)
}
func (n noRangeBucket) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
	return n.inner.List(ctx, prefix)
}
func (n noRangeBucket) SignedURL(ctx context.Context, key string, opts blob.SignOptions) (string, error) {
	return n.inner.SignedURL(ctx, key, opts)
}
func (n noRangeBucket) Close() error { return n.inner.Close() }

// scenario: rangereader-fallback (ADR-0080) — a bucket whose driver lacks RangeReader
// still serves a ranged GET (via full Get + slice).
func TestScenarioRangeReaderFallback(t *testing.T) {
	makeNoRange := func(t *testing.T) blob.Bucket { return noRangeBucket{inner: memBucket(t)} }
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, makeNoRange)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("0123456789"))
	c := g.client(t, "default", "analytics")

	// Range bytes=2-5 → "2345"; the driver has no RangeReader so the gateway slices.
	out, err := c.GetObject(context.Background(), &awss3.GetObjectInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet"), Range: ptrS("bytes=2-5"),
	})
	require.NoError(t, err)
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	require.Equal(t, "2345", string(body))
}

// getCountingBucket counts the whole-object Gets made on a blob.Bucket.
type getCountingBucket struct {
	blob.Bucket
	gets atomic.Int32
}

func (c *getCountingBucket) Get(ctx context.Context, key string) ([]byte, error) {
	c.gets.Add(1)
	return c.Bucket.Get(ctx, key)
}

// TestIssue110_HeadObjectDoesNotReadObject: a HEAD answers from the object's metadata (size and
// modification time) on a file substrate, without reading the object; a missing key is still a 404.
func TestIssue110_HeadObjectDoesNotReadObject(t *testing.T) {
	dir := t.TempDir()
	var sub *getCountingBucket
	makeFile := func(t *testing.T) blob.Bucket {
		t.Helper()
		b, err := gocloud.Open(context.Background(), "file://"+dir)
		require.NoError(t, err)
		sub = &getCountingBucket{Bucket: b}
		return sub
	}
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, makeFile)
	data := bytes.Repeat([]byte("x"), 1<<20)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", data)
	modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "gold", "q.parquet"), modTime, modTime))
	c := g.client(t, "default", "analytics")
	ctx := context.Background()

	out, err := c.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err)
	require.Zero(t, sub.gets.Load(), "HEAD must not read the whole object")
	require.Equal(t, int64(len(data)), aws.ToInt64(out.ContentLength))
	require.Equal(t, modTime, aws.ToTime(out.LastModified).UTC())

	_, err = c.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/missing.parquet")})
	require.Error(t, err)
	require.Equal(t, 404, statusCode(err))
}

// scenario: owner multipart write (ADR-0080) — the owner drives an explicit multipart
// upload (Create → UploadPart×2 → Complete) under the s3::write PEP; the buffered parts
// assemble into a single blob.Put. A non-owner's Create is denied (403).
func TestScenarioOwnerMultipartWrite(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")

	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("bronze/big.parquet"),
	})
	require.NoError(t, err, "the owner starts a multipart upload")

	part1 := bytes.Repeat([]byte("a"), 6<<20) // 6 MiB
	part2 := bytes.Repeat([]byte("b"), 4<<20) // 4 MiB
	parts := make([]awstypes.CompletedPart, 0, 2)
	for i, body := range [][]byte{part1, part2} {
		num := int32(i + 1)
		uo, perr := owner.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: ptrS("lakehouse"), Key: ptrS("bronze/big.parquet"),
			UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader(body),
		})
		require.NoError(t, perr, "upload part %d", num)
		parts = append(parts, awstypes.CompletedPart{ETag: uo.ETag, PartNumber: &num})
	}
	_, err = owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("bronze/big.parquet"), UploadId: create.UploadId,
		MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: parts},
	})
	require.NoError(t, err, "owner completes the multipart upload → a single blob.Put")
	require.Len(t, mustGet(t, g, "default", "lakehouse", "bronze/big.parquet"), len(part1)+len(part2))

	// A non-owner cannot even start a multipart upload on the owned prefix.
	nonOwner := g.client(t, "default", "analytics")
	_, err = nonOwner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: ptrS("lakehouse"), Key: ptrS("bronze/forbidden.parquet"),
	})
	require.Error(t, err, "a bound non-owner cannot start a multipart write")
	require.Equal(t, 403, statusCode(err))
}

// TestIssue30_MultipartTotalCappedAtUploadPart: maxUploadBytes bounds the bytes a multipart
// upload buffers (ADR-0080), so the part that would take the upload past the cap is
// rejected when it arrives, not at Complete after every part is held in memory.
func TestIssue30_MultipartTotalCappedAtUploadPart(t *testing.T) {
	const maxUpload = 1024
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, func(d *s3gateway.Deps) {
		d.MaxUploadBytes = maxUpload
	})
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	key := ptrS("bronze/capped.parquet")

	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: ptrS("lakehouse"), Key: key})
	require.NoError(t, err)
	upload := func(num int32, size int) (*awss3.UploadPartOutput, error) {
		return owner.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: ptrS("lakehouse"), Key: key, UploadId: create.UploadId,
			PartNumber: &num, Body: bytes.NewReader(bytes.Repeat([]byte("x"), size)),
		})
	}

	_, err = upload(1, maxUpload)
	require.NoError(t, err, "a part up to the cap is accepted")
	first, err := upload(1, maxUpload)
	require.NoError(t, err, "re-sending a part replaces it, it does not add to the total")
	_, err = upload(2, 1)
	require.Error(t, err, "a part past the upload's cap must be rejected")
	require.Equal(t, 400, statusCode(err), "EntityTooLarge")

	num := int32(1)
	_, err = owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: ptrS("lakehouse"), Key: key, UploadId: create.UploadId,
		MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: []awstypes.CompletedPart{{ETag: first.ETag, PartNumber: &num}}},
	})
	require.NoError(t, err, "the upload holds only the accepted part, within the cap")
	require.Len(t, mustGet(t, g, "default", "lakehouse", "bronze/capped.parquet"), maxUpload)
}

// mustGet reads a substrate key directly (bypassing the gateway) for assertions.
func mustGet(t *testing.T, g *gw, ns, bucket, key string) []byte {
	t.Helper()
	b, ok := g.buckets[ns+"/"+bucket]
	require.True(t, ok)
	data, err := b.Get(context.Background(), key)
	require.NoError(t, err)
	return data
}
