package s3gateway_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
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

// Issue 159: ListObjectsV2 and ListObjects honour MaxKeys, Delimiter, StartAfter,
// ContinuationToken and Marker instead of returning every key under the prefix.
func TestIssue159_ListObjectsHonoursListingParams(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	all := []string{"gold/a.parquet", "gold/b.parquet", "gold/c.parquet", "gold/d/x.parquet", "gold/e/y.parquet"}
	for _, k := range all {
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	c := g.client(t, "default", "analytics")
	ctx := context.Background()
	two, three := int32(2), int32(3)

	var paged []string
	var token *string
	pages := 0
	for {
		out, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), MaxKeys: &two, ContinuationToken: token,
		})
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(out.Contents), 2, "page %d exceeds MaxKeys", pages)
		require.Equal(t, int32(2), *out.MaxKeys)
		require.Equal(t, int32(len(out.Contents)), *out.KeyCount)
		paged = append(paged, objectKeys(out.Contents)...)
		if !*out.IsTruncated {
			break
		}
		require.NotNil(t, out.NextContinuationToken, "a truncated page carries a continuation token")
		require.Less(t, pages, len(all), "pagination does not terminate")
		token = out.NextContinuationToken
	}
	require.Equal(t, 3, pages)
	require.Equal(t, all, paged, "pages cover every key once, in order")

	delim, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"),
	})
	require.NoError(t, err)
	require.Equal(t, all[:3], objectKeys(delim.Contents))
	require.Equal(t, []string{"gold/d/", "gold/e/"}, commonPrefixes(delim.CommonPrefixes))

	after, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), StartAfter: ptrS("gold/c.parquet"),
	})
	require.NoError(t, err)
	require.Equal(t, all[3:], objectKeys(after.Contents))

	v1page, err := c.ListObjects(ctx, &awss3.ListObjectsInput{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"), Marker: ptrS("gold/a.parquet"), MaxKeys: &three,
	})
	require.NoError(t, err)
	require.Equal(t, all[1:3], objectKeys(v1page.Contents))
	require.Equal(t, []string{"gold/d/"}, commonPrefixes(v1page.CommonPrefixes))
	require.True(t, *v1page.IsTruncated)
	require.Equal(t, "gold/d/", *v1page.NextMarker)

	v1rest, err := c.ListObjects(ctx, &awss3.ListObjectsInput{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"), Marker: v1page.NextMarker, MaxKeys: &three,
	})
	require.NoError(t, err)
	require.Empty(t, v1rest.Contents)
	require.Equal(t, []string{"gold/e/"}, commonPrefixes(v1rest.CommonPrefixes))
	require.False(t, *v1rest.IsTruncated)
}

// A listing authorizes the leading segment of the S3 Prefix, so it must return only keys in that
// sub-domain: a principal bound to gold sees nothing of golden, whose name merely starts with gold.
func TestListObjectsExcludeSiblingPrefixSharingName(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	for _, k := range []string{"gold/a.parquet", "golden/secret.parquet"} {
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	c := g.client(t, "default", "analytics")
	ctx := context.Background()

	for _, prefix := range []string{"gold", "gold/"} {
		v2, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS(prefix)})
		require.NoError(t, err)
		require.Equal(t, []string{"gold/a.parquet"}, objectKeys(v2.Contents), "V2 Prefix=%q", prefix)

		v1list, err := c.ListObjects(ctx, &awss3.ListObjectsInput{Bucket: ptrS("lakehouse"), Prefix: ptrS(prefix)})
		require.NoError(t, err)
		require.Equal(t, []string{"gold/a.parquet"}, objectKeys(v1list.Contents), "V1 Prefix=%q", prefix)
	}

	delim, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold"), Delimiter: ptrS("/")})
	require.NoError(t, err)
	require.Empty(t, delim.Contents)
	require.Equal(t, []string{"gold/"}, commonPrefixes(delim.CommonPrefixes))
}

// A listing page whose XML would pass versitygw's 4 MiB response cap pages on instead of answering 500:
// S3-length keys made of '&' encode five times longer, so a page of 1000 keys, or of their 1000 common
// prefixes, is over 5 MiB.
func TestListObjects_PageStaysUnderXMLBodyCap(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	const n = 1000
	var all, dirs []string
	for i := range n {
		dir := fmt.Sprintf("gold/%04d%s/", i, strings.Repeat("&", 1000))
		all = append(all, dir+"x")
		dirs = append(dirs, dir)
		g.seed(t, "default", "lakehouse", dir+"x", []byte("x"))
	}
	c := g.client(t, "default", "analytics")
	ctx := context.Background()

	var listed []string
	var token *string
	for pages := 1; ; pages++ {
		out, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), ContinuationToken: token,
		})
		require.NoError(t, err, "ListObjectsV2 page %d", pages)
		listed = append(listed, objectKeys(out.Contents)...)
		if !*out.IsTruncated {
			break
		}
		require.NotNil(t, out.NextContinuationToken, "a truncated page carries a continuation token")
		require.Less(t, pages, len(all), "pagination does not terminate")
		token = out.NextContinuationToken
	}
	require.Equal(t, all, listed, "pages cover every key once, in order")

	var prefixes []string
	var marker *string
	for pages := 1; ; pages++ {
		out, err := c.ListObjects(ctx, &awss3.ListObjectsInput{
			Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"), Marker: marker,
		})
		require.NoError(t, err, "ListObjects page %d", pages)
		require.Empty(t, out.Contents)
		prefixes = append(prefixes, commonPrefixes(out.CommonPrefixes)...)
		if !*out.IsTruncated {
			break
		}
		require.NotNil(t, out.NextMarker, "a truncated page carries a next marker")
		require.Less(t, pages, len(dirs), "pagination does not terminate")
		marker = out.NextMarker
	}
	require.Equal(t, dirs, prefixes, "pages cover every common prefix once, in order")
}

func objectKeys(objs []awstypes.Object) []string {
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, *o.Key)
	}
	return keys
}

func commonPrefixes(cps []awstypes.CommonPrefix) []string {
	out := make([]string, 0, len(cps))
	for _, cp := range cps {
		out = append(out, *cp.Prefix)
	}
	return out
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
func (n noRangeBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	return n.inner.Put(ctx, key, data, opts)
}
func (n noRangeBucket) Delete(ctx context.Context, key string) error { return n.inner.Delete(ctx, key) }
func (n noRangeBucket) Exists(ctx context.Context, key string) (bool, error) {
	return n.inner.Exists(ctx, key)
}
func (n noRangeBucket) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
	return n.inner.List(ctx, prefix)
}
func (n noRangeBucket) Attributes(ctx context.Context, key string) (blob.Attributes, error) {
	return n.inner.Attributes(ctx, key)
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

// Issue #112: a ranged GET reports the complete object length in Content-Range, honours
// suffix ranges, and answers 416 for a range that starts at or past the end — on both the
// RangeReader path and the full-Get fallback.
func TestIssue112_RangedGetReportsSizeOr416(t *testing.T) {
	drivers := map[string]func(t *testing.T) blob.Bucket{
		"range-reader": memBucket,
		"fallback":     func(t *testing.T) blob.Bucket { return noRangeBucket{inner: memBucket(t)} },
	}
	cases := []struct {
		rng, contentRange, body string
		status                  int
	}{
		{rng: "bytes=2-5", contentRange: "bytes 2-5/10", body: "2345"},
		{rng: "bytes=5-100", contentRange: "bytes 5-9/10", body: "56789"},
		{rng: "bytes=7-", contentRange: "bytes 7-9/10", body: "789"},
		{rng: "bytes=-3", contentRange: "bytes 7-9/10", body: "789"},
		{rng: "bytes=20-30", status: 416},
		{rng: "bytes=10-", status: 416},
	}
	for name, makeBucket := range drivers {
		t.Run(name, func(t *testing.T) {
			g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, makeBucket)
			g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("0123456789"))
			c := g.client(t, "default", "analytics")
			for _, tc := range cases {
				out, err := c.GetObject(context.Background(), &awss3.GetObjectInput{
					Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet"), Range: ptrS(tc.rng),
				})
				if tc.status != 0 {
					require.Error(t, err, "Range %s", tc.rng)
					require.Equal(t, tc.status, statusCode(err), "Range %s", tc.rng)
					continue
				}
				require.NoError(t, err, "Range %s", tc.rng)
				body, rerr := io.ReadAll(out.Body)
				_ = out.Body.Close()
				require.NoError(t, rerr)
				require.Equal(t, tc.contentRange, aws.ToString(out.ContentRange), "Range %s", tc.rng)
				require.Equal(t, tc.body, string(body), "Range %s", tc.rng)
				require.Equal(t, int64(len(tc.body)), aws.ToInt64(out.ContentLength), "Range %s", tc.rng)
			}
		})
	}
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

// TestMultipartUploadIsBoundToItsNamespaceBucketAndKey: an upload id works only for the
// namespace, bucket and key it was created for (ADR-0080 tenancy default-deny; S3 scopes an
// upload id to its bucket and key). A principal in another namespace that is authorized on
// its own bucket, even one with the same name and key, gets NoSuchUpload for every multipart
// op on the victim's id, and so does the creator on another key; the upload itself stays intact.
func TestMultipartUploadIsBoundToItsNamespaceBucketAndKey(t *testing.T) {
	meta := lakehouseMeta()
	meta.fns["default/etl-svc"].Spec.Blob = []v1.FunctionBlob{{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}}
	meta.fns["evil/attacker"] = &v1.Function{
		ObjectMeta: v1.ObjectMeta{Name: "attacker", Namespace: "evil", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Blob: []v1.FunctionBlob{{Alias: "loot", Bucket: "lakehouse", Prefix: "bronze"}}},
	}
	meta.buckets["evil/lakehouse"] = &v1.Bucket{
		ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "evil", ResourceGroup: "rg1"},
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "bronze", Owner: "attacker"}}},
	}
	g := newGateway(t, meta, fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	victim := g.client(t, "default", "etl-svc")
	attacker := g.client(t, "evil", "attacker")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/payroll.parquet")

	create, err := victim.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	one := int32(1)
	part, err := victim.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: bucket, Key: key, UploadId: create.UploadId, PartNumber: &one, Body: strings.NewReader("SECRET-ROWS"),
	})
	require.NoError(t, err)
	done := &awstypes.CompletedMultipartUpload{Parts: []awstypes.CompletedPart{{ETag: part.ETag, PartNumber: &one}}}

	noSuchUpload := func(op string, err error) {
		t.Helper()
		if assert.Error(t, err, "%s on another target's upload must fail", op) {
			assert.Equal(t, 404, statusCode(err), "%s: %v", op, err)
			assert.ErrorContains(t, err, "NoSuchUpload", op)
		}
	}
	for _, tc := range []struct {
		name   string
		c      *awss3.Client
		bucket *string
		key    *string
	}{
		{"another namespace, same bucket and key names", attacker, bucket, key},
		{"another namespace, another key", attacker, bucket, ptrS("bronze/loot.parquet")},
		{"the creator, another key", victim, bucket, ptrS("bronze/other.parquet")},
	} {
		_, err = tc.c.ListParts(ctx, &awss3.ListPartsInput{Bucket: tc.bucket, Key: tc.key, UploadId: create.UploadId})
		noSuchUpload(tc.name+": ListParts", err)
		two := int32(2)
		_, err = tc.c.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: tc.bucket, Key: tc.key, UploadId: create.UploadId, PartNumber: &two, Body: strings.NewReader("INJECTED"),
		})
		noSuchUpload(tc.name+": UploadPart", err)
		_, err = tc.c.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket: tc.bucket, Key: tc.key, UploadId: create.UploadId, MultipartUpload: done,
		})
		noSuchUpload(tc.name+": CompleteMultipartUpload", err)
		_, err = tc.c.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: tc.bucket, Key: tc.key, UploadId: create.UploadId})
		noSuchUpload(tc.name+": AbortMultipartUpload", err)
	}

	_, err = victim.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: bucket, Key: key, UploadId: create.UploadId, MultipartUpload: done,
	})
	require.NoError(t, err, "the victim's upload survives the other targets' attempts")
	require.Equal(t, "SECRET-ROWS", string(mustGet(t, g, "default", "lakehouse", "bronze/payroll.parquet")))
	for _, k := range []string{"bronze/payroll.parquet", "bronze/loot.parquet"} {
		ok, xerr := g.buckets["evil/lakehouse"].Exists(ctx, k)
		require.NoError(t, xerr)
		require.False(t, ok, "no victim bytes land in the other namespace's bucket at %s", k)
	}
	require.Regexp(t, `^funcd-mpu-[A-Z2-7]{26}$`, *create.UploadId, "an upload id is random, not a counter another tenant can guess")
}

// TestIssue157_BucketOpsHonorBindings: HeadBucket succeeds only for a caller bound to the
// Bucket, and ListBuckets returns exactly the Buckets the caller is bound to (ADR-0080).
// An unbound caller gets the same 403 for an existing and a missing bucket.
func TestIssue157_BucketOpsHonorBindings(t *testing.T) {
	meta := lakehouseMeta()
	meta.buckets["default/scratch"] = &v1.Bucket{
		ObjectMeta: v1.ObjectMeta{Name: "scratch", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "tmp"}}},
	}
	g := newGateway(t, meta, fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	bound := g.client(t, "default", "analytics")
	unbound := g.client(t, "default", "reporting")

	_, err := bound.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: ptrS("lakehouse")})
	require.NoError(t, err, "analytics is bound to lakehouse")

	_, err = unbound.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: ptrS("lakehouse")})
	require.Error(t, err, "reporting has no spec.blob binding to lakehouse")
	require.Equal(t, 403, statusCode(err))

	_, err = unbound.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: ptrS("nosuch")})
	require.Equal(t, 403, statusCode(err), "a missing bucket answers like an unbound one")

	_, err = bound.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: ptrS("scratch")})
	require.Equal(t, 403, statusCode(err), "analytics is not bound to scratch")

	out, err := bound.ListBuckets(ctx, &awss3.ListBucketsInput{})
	require.NoError(t, err)
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		names = append(names, *b.Name)
	}
	require.Equal(t, []string{"lakehouse"}, names)

	out, err = unbound.ListBuckets(ctx, &awss3.ListBucketsInput{})
	require.NoError(t, err)
	require.Empty(t, out.Buckets)
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

// Issue 158: Complete assembles exactly the listed parts, in order, and rejects a listed
// part whose ETag does not match or that was never uploaded (S3 InvalidPart); a rejected
// Complete leaves the upload in place.
func TestIssue158_CompleteHonorsPartList(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/mpu.bin")

	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	etags := map[int32]*string{}
	for i, body := range []string{"AAA", "BBB", "CCC"} {
		num := int32(i + 1)
		uo, perr := owner.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: bucket, Key: key, UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader([]byte(body)),
		})
		require.NoError(t, perr)
		etags[num] = uo.ETag
	}
	complete := func(parts ...awstypes.CompletedPart) error {
		_, cerr := owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket: bucket, Key: key, UploadId: create.UploadId,
			MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: parts},
		})
		return cerr
	}
	part := func(num int32, etag *string) awstypes.CompletedPart {
		return awstypes.CompletedPart{PartNumber: &num, ETag: etag}
	}

	err = complete(part(1, ptrS(`"bogus"`)), part(2, etags[2]))
	require.ErrorContains(t, err, "InvalidPart", "a listed ETag that does not match the stored part")
	require.Equal(t, 400, statusCode(err))

	err = complete(part(1, etags[1]), part(9, etags[2]))
	require.ErrorContains(t, err, "InvalidPart", "a listed part that was never uploaded")
	require.Equal(t, 400, statusCode(err))

	err = complete(part(2, etags[2]), part(1, etags[1]))
	require.ErrorContains(t, err, "InvalidPartOrder", "parts listed out of ascending order")
	require.Equal(t, 400, statusCode(err))

	require.NoError(t, complete(part(1, etags[1]), part(2, etags[2])))
	require.Equal(t, "AAABBB", string(mustGet(t, g, "default", "lakehouse", "bronze/mpu.bin")),
		"the object is exactly the listed parts; the unlisted part 3 is dropped")
}

// TestIssue380_ETagsAreQuoted: every ETag the gateway sends is the MD5 hex in double quotes, the
// RFC 9110 entity-tag form S3 uses — PutObject, GetObject and ListParts as well as UploadPart.
func TestIssue380_ETagsAreQuoted(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	reader := g.client(t, "default", "analytics")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/hello.txt")
	const want = `"5d41402abc4b2a76b9719d911017c592"` // MD5 of "hello"

	put, err := owner.PutObject(ctx, &awss3.PutObjectInput{Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("hello"))})
	require.NoError(t, err)
	require.Equal(t, want, aws.ToString(put.ETag), "PutObject")

	got, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	require.NoError(t, got.Body.Close())
	require.Equal(t, want, aws.ToString(got.ETag), "GetObject")

	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	num := int32(1)
	up, err := owner.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: bucket, Key: key, UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader([]byte("hello")),
	})
	require.NoError(t, err)
	require.Equal(t, want, aws.ToString(up.ETag), "UploadPart")

	parts, err := reader.ListParts(ctx, &awss3.ListPartsInput{Bucket: bucket, Key: key, UploadId: create.UploadId})
	require.NoError(t, err)
	require.Len(t, parts.Parts, 1)
	require.Equal(t, want, aws.ToString(parts.Parts[0].ETag), "ListParts")
}

// TestIssue425_RangedGetKeepsObjectETag: a ranged GET never sends an ETag computed over the range.
// It carries the object's ETag (the one PutObject and a full GET return), on both the RangeReader
// path and the full-Get fallback (ADR-0159).
func TestIssue425_RangedGetKeepsObjectETag(t *testing.T) {
	drivers := map[string]func(t *testing.T) blob.Bucket{
		"range-reader": memBucket,
		"fallback":     func(t *testing.T) blob.Bucket { return noRangeBucket{inner: memBucket(t)} },
	}
	for name, makeBucket := range drivers {
		t.Run(name, func(t *testing.T) {
			g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, makeBucket)
			ctx := context.Background()
			reader := g.client(t, "default", "analytics")
			bucket, key := ptrS("lakehouse"), ptrS("bronze/abc.bin")

			put, err := g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
				Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("abcdefgh")),
			})
			require.NoError(t, err)
			want := aws.ToString(put.ETag)

			full, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key})
			require.NoError(t, err)
			require.NoError(t, full.Body.Close())
			require.Equal(t, want, aws.ToString(full.ETag), "a full GET carries the PutObject ETag")

			for _, rng := range []string{"bytes=0-3", "bytes=4-7"} {
				out, gerr := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, Range: ptrS(rng)})
				require.NoError(t, gerr, "Range %s", rng)
				require.NoError(t, out.Body.Close())
				require.NotEmpty(t, aws.ToString(out.ContentRange), "Range %s is a 206", rng)
				require.Equal(t, want, aws.ToString(out.ETag), "Range %s must carry the object's ETag", rng)
			}
		})
	}
}

// ctxBucket hands the test the context of each Put; with block set, the Put waits for that
// context to end instead of writing.
type ctxBucket struct {
	blob.Bucket
	puts  chan context.Context
	block atomic.Bool
}

func (c *ctxBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	c.puts <- ctx
	if c.block.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.Bucket.Put(ctx, key, data, opts)
}

// Issue #462: the substrate never gets fasthttp's pooled RequestCtx, whose Done reads server
// state that Close rewrites. Its context ends with the request, so nothing derived from it
// (gocloud's NewWriter) outlives the request, and Close still cancels an op in flight.
func TestIssue462_BlobContextEndsWithRequest(t *testing.T) {
	sub := &ctxBucket{puts: make(chan context.Context, 1)}
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, func(t *testing.T) blob.Bucket {
		t.Helper()
		sub.Bucket = memBucket(t)
		return sub
	})
	owner := g.client(t, "default", "etl-svc")
	put := func() error {
		_, err := owner.PutObject(context.Background(), &awss3.PutObjectInput{
			Bucket: ptrS("lakehouse"), Key: ptrS("bronze/x.parquet"), Body: bytes.NewReader([]byte("rows")),
		}, func(o *awss3.Options) { o.RetryMaxAttempts = 1 })
		return err
	}

	require.NoError(t, put())
	require.ErrorIs(t, (<-sub.puts).Err(), context.Canceled, "the blob context ends with the request")

	sub.block.Store(true)
	errc := make(chan error, 1)
	go func() { errc <- put() }()
	inFlight := <-sub.puts
	require.NoError(t, g.server.Close())
	require.ErrorIs(t, inFlight.Err(), context.Canceled, "Close cancels a blob op in flight")
	require.Error(t, <-errc)
}

// TestIssue463_GatewayStartsWhenItsReservedPortIsTaken: freeAddr releases the port it reserves, so another test
// process can bind it before the gateway does. newGateway must still bring the gateway up, on another port,
// instead of waiting out the readiness timeout.
func TestIssue463_GatewayStartsWhenItsReservedPortIsTaken(t *testing.T) {
	var taken net.Listener
	takeReservedPort := func(d *s3gateway.Deps) {
		if taken != nil {
			return
		}
		l, err := net.Listen("tcp", d.Listen)
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })
		taken = l
	}
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, takeReservedPort)
	require.NotEqual(t, taken.Addr().String(), g.server.Addr(), "the gateway reports the port another listener holds")

	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("rows"))
	out, err := g.client(t, "default", "analytics").GetObject(context.Background(),
		&awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err)
	require.NoError(t, out.Body.Close())
}

// TestIssue496_GetObjectReportsModTime: a full or ranged GET reports the object's modification time as
// Last-Modified, the value HEAD returns, never the time of the request, on both the RangeReader path and
// the full-Get fallback.
func TestIssue496_GetObjectReportsModTime(t *testing.T) {
	fileBucket := func(dir string) func(t *testing.T) blob.Bucket {
		return func(t *testing.T) blob.Bucket {
			t.Helper()
			b, err := gocloud.Open(context.Background(), "file://"+dir)
			require.NoError(t, err)
			return b
		}
	}
	for name, wrap := range map[string]func(blob.Bucket) blob.Bucket{
		"range-reader": func(b blob.Bucket) blob.Bucket { return b },
		"fallback":     func(b blob.Bucket) blob.Bucket { return noRangeBucket{inner: b} },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			open := fileBucket(dir)
			g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, func(t *testing.T) blob.Bucket { return wrap(open(t)) })
			g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("0123456789"))
			modTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			require.NoError(t, os.Chtimes(filepath.Join(dir, "gold", "q.parquet"), modTime, modTime))
			c := g.client(t, "default", "analytics")
			ctx := context.Background()
			bucket, key := ptrS("lakehouse"), ptrS("gold/q.parquet")

			head, err := c.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
			require.NoError(t, err)
			require.Equal(t, modTime, aws.ToTime(head.LastModified).UTC())

			for _, rng := range []string{"", "bytes=2-5"} {
				in := &awss3.GetObjectInput{Bucket: bucket, Key: key}
				if rng != "" {
					in.Range = ptrS(rng)
				}
				out, gerr := c.GetObject(ctx, in)
				require.NoError(t, gerr, "Range %q", rng)
				require.NoError(t, out.Body.Close())
				require.Equal(t, modTime, aws.ToTime(out.LastModified).UTC(), "Range %q", rng)
			}
		})
	}
}

// Issue #111: GET and HEAD answer If-Modified-Since with 304 and If-Unmodified-Since with 412,
// comparing at the one-second precision of Last-Modified, and leave a date condition to the ETag
// condition that takes precedence over it. A PUT with If-None-Match: * never overwrites an existing
// object.
func TestIssue111_DatePreconditionsAndCreateOnlyPut(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner, reader := g.client(t, "default", "etl-svc"), g.client(t, "default", "analytics")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/x.parquet")
	put := func(k *string, body string, ifNoneMatch *string) (*awss3.PutObjectOutput, error) {
		return owner.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: bucket, Key: k, Body: bytes.NewReader([]byte(body)), IfNoneMatch: ifNoneMatch,
		})
	}
	created, err := put(key, "rows", nil)
	require.NoError(t, err)
	head, err := reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	lastModified := aws.ToTime(head.LastModified)
	before := lastModified.Add(-time.Second)

	status := func(err error) int {
		if err == nil {
			return 200
		}
		return statusCode(err)
	}
	cases := []struct {
		name                 string
		modSince, unmodSince *time.Time
		ifMatch, ifNoneMatch *string
		want                 int
	}{
		{name: "if-modified-since last-modified", modSince: &lastModified, want: 304},
		{name: "if-modified-since before", modSince: &before, want: 200},
		{name: "if-unmodified-since last-modified", unmodSince: &lastModified, want: 200},
		{name: "if-unmodified-since before", unmodSince: &before, want: 412},
		{name: "not modified and modified after", modSince: &lastModified, unmodSince: &before, want: 412},
		{name: "if-match decides over if-unmodified-since", unmodSince: &before, ifMatch: created.ETag, want: 200},
		{name: "stale if-match with if-unmodified-since last-modified", unmodSince: &lastModified, ifMatch: ptrS(`"stale"`), want: 412},
		{name: "if-none-match decides over if-modified-since", modSince: &lastModified, ifNoneMatch: ptrS(`"other"`), want: 200},
	}
	for _, tc := range cases {
		out, gerr := reader.GetObject(ctx, &awss3.GetObjectInput{
			Bucket: bucket, Key: key, IfModifiedSince: tc.modSince, IfUnmodifiedSince: tc.unmodSince,
			IfMatch: tc.ifMatch, IfNoneMatch: tc.ifNoneMatch,
		})
		if gerr == nil {
			require.NoError(t, out.Body.Close())
		}
		require.Equal(t, tc.want, status(gerr), "GET %s", tc.name)
		_, herr := reader.HeadObject(ctx, &awss3.HeadObjectInput{
			Bucket: bucket, Key: key, IfModifiedSince: tc.modSince, IfUnmodifiedSince: tc.unmodSince,
			IfMatch: tc.ifMatch, IfNoneMatch: tc.ifNoneMatch,
		})
		require.Equal(t, tc.want, status(herr), "HEAD %s", tc.name)
	}

	_, err = put(key, "CLOBBERED", ptrS("*"))
	var coded interface{ ErrorCode() string }
	require.ErrorAs(t, err, &coded, "If-None-Match: * over an existing object")
	require.Equal(t, 412, statusCode(err))
	require.Equal(t, "PreconditionFailed", coded.ErrorCode())
	require.Equal(t, "rows", string(mustGet(t, g, "default", "lakehouse", "bronze/x.parquet")))
	_, err = put(ptrS("bronze/new.parquet"), "fresh", ptrS("*"))
	require.NoError(t, err, "If-None-Match: * creates a missing object")
	require.Equal(t, "fresh", string(mustGet(t, g, "default", "lakehouse", "bronze/new.parquet")))
}

// Issue #111: a CompleteMultipartUpload with If-None-Match: * never overwrites an existing object,
// and still creates a missing one.
func TestIssue111_CreateOnlyMultipartComplete(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	bucket := ptrS("lakehouse")
	_, err := owner.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: ptrS("bronze/x.parquet"), Body: bytes.NewReader([]byte("rows")),
	})
	require.NoError(t, err)
	completeCreateOnly := func(key, body string) error {
		create, cerr := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: &key})
		require.NoError(t, cerr)
		num := int32(1)
		part, perr := owner.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: bucket, Key: &key, UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader([]byte(body)),
		})
		require.NoError(t, perr)
		_, cerr = owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket: bucket, Key: &key, UploadId: create.UploadId, IfNoneMatch: ptrS("*"),
			MultipartUpload: &awstypes.CompletedMultipartUpload{
				Parts: []awstypes.CompletedPart{{PartNumber: &num, ETag: part.ETag}},
			},
		})
		return cerr
	}

	err = completeCreateOnly("bronze/x.parquet", "CLOBBERED")
	var coded interface{ ErrorCode() string }
	require.ErrorAs(t, err, &coded, "If-None-Match: * over an existing object")
	require.Equal(t, 412, statusCode(err))
	require.Equal(t, "PreconditionFailed", coded.ErrorCode())
	require.Equal(t, "rows", string(mustGet(t, g, "default", "lakehouse", "bronze/x.parquet")))
	require.NoError(t, completeCreateOnly("bronze/new.parquet", "fresh"), "If-None-Match: * creates a missing object")
	require.Equal(t, "fresh", string(mustGet(t, g, "default", "lakehouse", "bronze/new.parquet")))
}

func fileBucketIn(dir string) func(t *testing.T) blob.Bucket {
	return func(t *testing.T) blob.Bucket {
		t.Helper()
		b, err := gocloud.Open(context.Background(), "file://"+dir)
		require.NoError(t, err)
		return b
	}
}

// scenario: etag-on-every-read (ADR-0159) — the ETag a PUT answered comes back on HEAD, a ranged GET and
// the listing, on the memory and the file substrate.
func TestScenarioETagOnEveryRead(t *testing.T) {
	for name, makeBucket := range map[string]func(t *testing.T) blob.Bucket{
		"memory": memBucket,
		"file":   func(t *testing.T) blob.Bucket { return fileBucketIn(t.TempDir())(t) },
	} {
		t.Run(name, func(t *testing.T) {
			g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, makeBucket)
			ctx := context.Background()
			reader := g.client(t, "default", "analytics")
			bucket, key := ptrS("lakehouse"), ptrS("bronze/e.bin")
			put, err := g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
				Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("0123456789")),
			})
			require.NoError(t, err)
			want := aws.ToString(put.ETag)
			require.NotEmpty(t, want)

			head, err := reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
			require.NoError(t, err)
			require.Equal(t, want, aws.ToString(head.ETag), "HEAD")

			ranged, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, Range: ptrS("bytes=0-3")})
			require.NoError(t, err)
			require.NoError(t, ranged.Body.Close())
			require.Equal(t, "bytes 0-3/10", aws.ToString(ranged.ContentRange))
			require.Equal(t, want, aws.ToString(ranged.ETag), "ranged GET")

			list, err := reader.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: bucket, Prefix: ptrS("bronze/")})
			require.NoError(t, err)
			require.Len(t, list.Contents, 1)
			require.Equal(t, want, aws.ToString(list.Contents[0].ETag), "LIST")
		})
	}
}

// scenario: stale-if-match-put-rejected (ADR-0159) — a PUT or CompleteMultipartUpload whose If-Match names a
// replaced ETag is 412 and leaves the object as it was; the current ETag succeeds. The rest of the write
// rows: If-Match: * proceeds on an existing object, If-Match on an absent one is 404, an entity-tag
// If-None-Match and If-Match with If-None-Match: * are 501.
func TestScenarioStaleIfMatchPutRejected(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/x.bin")
	put := func(k *string, body string, ifMatch, ifNoneMatch *string) (*awss3.PutObjectOutput, error) {
		return owner.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: bucket, Key: k, Body: bytes.NewReader([]byte(body)), IfMatch: ifMatch, IfNoneMatch: ifNoneMatch,
		})
	}
	v0, err := put(key, "v0", nil, nil)
	require.NoError(t, err)
	v1, err := put(key, "v1", nil, nil)
	require.NoError(t, err)
	e0, e1 := v0.ETag, v1.ETag
	require.NotEqual(t, aws.ToString(e0), aws.ToString(e1))

	_, err = put(key, "STALE", e0, nil)
	require.Equal(t, 412, statusCode(err), "If-Match of the replaced ETag: %v", err)
	require.Equal(t, "v1", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))
	v2, err := put(key, "v2", e1, nil)
	require.NoError(t, err, "If-Match of the current ETag")
	require.Equal(t, "v2", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))

	_, err = put(key, "v3", ptrS("*"), nil)
	require.NoError(t, err, "If-Match: * on an existing object")
	require.Equal(t, "v3", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))
	_, err = put(ptrS("bronze/absent.bin"), "a", ptrS("*"), nil)
	require.Equal(t, 404, statusCode(err), "If-Match: * on an absent object")
	_, err = put(ptrS("bronze/absent.bin"), "a", v2.ETag, nil)
	require.Equal(t, 404, statusCode(err), "If-Match on an absent object")
	_, err = put(key, "x", nil, e1)
	require.Equal(t, 501, statusCode(err), "an entity-tag If-None-Match")
	_, err = put(key, "x", ptrS("*"), ptrS("*"))
	require.Equal(t, 501, statusCode(err), "If-Match with If-None-Match: *")
	require.Equal(t, "v3", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))

	current, err := g.client(t, "default", "analytics").HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key})
	require.NoError(t, err)
	num := int32(1)
	part, err := owner.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: bucket, Key: key, UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader([]byte("multipart")),
	})
	require.NoError(t, err)
	complete := func(ifMatch *string) error {
		_, cerr := owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket: bucket, Key: key, UploadId: create.UploadId, IfMatch: ifMatch,
			MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: []awstypes.CompletedPart{{PartNumber: &num, ETag: part.ETag}}},
		})
		return cerr
	}
	require.Equal(t, 412, statusCode(complete(e0)), "a stale If-Match on Complete")
	require.Equal(t, "v3", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))
	require.NoError(t, complete(current.ETag), "the same upload completes with the current ETag")
	require.Equal(t, "multipart", string(mustGet(t, g, "default", "lakehouse", "bronze/x.bin")))
}

// scenario: if-none-match-not-modified (ADR-0159) — a GET or HEAD whose If-None-Match names the object's
// ETag (or *) is 304 with no body; another ETag gets 200 and the object.
func TestScenarioIfNoneMatchNotModified(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	reader := g.client(t, "default", "analytics")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/n.bin")
	put, err := g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("body")),
	})
	require.NoError(t, err)

	for _, inm := range []*string{put.ETag, ptrS("*")} {
		_, gerr := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, IfNoneMatch: inm})
		require.Equal(t, 304, statusCode(gerr), "GET If-None-Match %s", aws.ToString(inm))
		_, herr := reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key, IfNoneMatch: inm})
		require.Equal(t, 304, statusCode(herr), "HEAD If-None-Match %s", aws.ToString(inm))
	}

	other := ptrS(`"0123456789abcdef0123456789abcdef"`)
	out, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, IfNoneMatch: other})
	require.NoError(t, err)
	body, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	require.NoError(t, out.Body.Close())
	require.Equal(t, "body", string(body))
	require.Equal(t, aws.ToString(put.ETag), aws.ToString(out.ETag))
	head, err := reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key, IfNoneMatch: other})
	require.NoError(t, err)
	require.Equal(t, int64(4), aws.ToInt64(head.ContentLength))
}

// scenario: content-type-and-metadata-roundtrip (ADR-0159) — the Content-Type and x-amz-meta-* of a PUT, and
// of a multipart upload's Create, come back on HEAD and GET; a type that does not parse is 400.
func TestScenarioContentTypeAndMetadataRoundTrip(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	ctx := context.Background()
	owner, reader := g.client(t, "default", "etl-svc"), g.client(t, "default", "analytics")
	bucket := ptrS("lakehouse")
	meta := map[string]string{"owner": "etl"}
	check := func(key string) {
		t.Helper()
		head, err := reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: &key})
		require.NoError(t, err)
		require.Equal(t, "application/json", aws.ToString(head.ContentType), "HEAD %s", key)
		require.Equal(t, meta, head.Metadata, "HEAD %s", key)
		out, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: &key})
		require.NoError(t, err)
		require.NoError(t, out.Body.Close())
		require.Equal(t, "application/json", aws.ToString(out.ContentType), "GET %s", key)
		require.Equal(t, meta, out.Metadata, "GET %s", key)
	}

	_, err := owner.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: ptrS("bronze/put.json"), Body: bytes.NewReader([]byte(`{"a":1}`)),
		ContentType: ptrS("application/json"), Metadata: meta,
	})
	require.NoError(t, err)
	check("bronze/put.json")

	key := "bronze/mpu.json"
	create, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: bucket, Key: &key, ContentType: ptrS("application/json"), Metadata: meta,
	})
	require.NoError(t, err)
	num := int32(1)
	part, err := owner.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: bucket, Key: &key, UploadId: create.UploadId, PartNumber: &num, Body: bytes.NewReader([]byte(`{"b":2}`)),
	})
	require.NoError(t, err)
	_, err = owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: bucket, Key: &key, UploadId: create.UploadId,
		MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: []awstypes.CompletedPart{{PartNumber: &num, ETag: part.ETag}}},
	})
	require.NoError(t, err)
	check(key)

	_, err = owner.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: ptrS("bronze/bad.json"), Body: bytes.NewReader([]byte("x")), ContentType: ptrS("not a type;;"),
	})
	require.Equal(t, 400, statusCode(err), "an unparsable Content-Type: %v", err)
}

// bodyRecorder is an S3 client HTTP client that keeps the raw body of the last response.
type bodyRecorder struct{ last []byte }

func (r *bodyRecorder) Do(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	r.last = body
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// scenario: no-digest-no-etag (ADR-0159) — an object in the file substrate without gocloud's sidecar has no
// ETag on HEAD, GET or the listing; an If-Match other than * is 412 on GET, HEAD and PUT and leaves the
// object unchanged; an entity-tag If-None-Match proceeds; and no whole-object read computes an ETag.
func TestScenarioNoDigestNoETag(t *testing.T) {
	dir := t.TempDir()
	var sub *getCountingBucket
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, func(t *testing.T) blob.Bucket {
		t.Helper()
		sub = &getCountingBucket{Bucket: fileBucketIn(dir)(t)}
		return sub
	})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bronze"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bronze", "raw.bin"), []byte("raw bytes"), 0o600))
	ctx := context.Background()
	reader := g.client(t, "default", "analytics")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/raw.bin")
	anyTag := ptrS(`"0123456789abcdef0123456789abcdef"`)
	okGets := int32(0)
	get := func(ifMatch, ifNoneMatch *string) (*awss3.GetObjectOutput, error) {
		out, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
		if err == nil {
			okGets++
			body, rerr := io.ReadAll(out.Body)
			require.NoError(t, rerr)
			require.NoError(t, out.Body.Close())
			require.Equal(t, "raw bytes", string(body))
		}
		return out, err
	}
	head := func(ifMatch, ifNoneMatch *string) (*awss3.HeadObjectOutput, error) {
		return reader.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
	}

	h, err := head(nil, nil)
	require.NoError(t, err)
	require.Nil(t, h.ETag, "HEAD")
	out, err := get(nil, nil)
	require.NoError(t, err)
	require.Nil(t, out.ETag, "GET")
	rec := &bodyRecorder{}
	list, err := reader.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: bucket, Prefix: ptrS("bronze/")},
		func(o *awss3.Options) { o.HTTPClient = rec })
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
	require.Nil(t, list.Contents[0].ETag, "LIST")
	require.Contains(t, string(rec.last), "<Key>bronze/raw.bin</Key>")
	require.NotContains(t, string(rec.last), "<ETag>", "the raw ListObjectsV2 XML")

	_, err = get(anyTag, nil)
	require.Equal(t, 412, statusCode(err), "GET If-Match")
	_, err = head(anyTag, nil)
	require.Equal(t, 412, statusCode(err), "HEAD If-Match")
	_, err = g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("CLOBBERED")), IfMatch: anyTag,
	})
	require.Equal(t, 412, statusCode(err), "PUT If-Match")
	raw, err := os.ReadFile(filepath.Join(dir, "bronze", "raw.bin"))
	require.NoError(t, err)
	require.Equal(t, "raw bytes", string(raw))

	_, err = get(nil, anyTag)
	require.NoError(t, err, "GET If-None-Match matches nothing")
	h, err = head(nil, anyTag)
	require.NoError(t, err, "HEAD If-None-Match matches nothing")
	require.Equal(t, int64(len("raw bytes")), aws.ToInt64(h.ContentLength))
	_, err = get(ptrS("*"), nil)
	require.NoError(t, err, "If-Match: * is an existence test")

	require.Equal(t, okGets, sub.gets.Load(), "one whole-object read per 200 GET, none for an ETag")
}

// rangeCountingBucket counts the whole-object Gets and the range reads made on a bucket with RangeReader.
type rangeCountingBucket struct {
	blob.Bucket
	gets, ranges atomic.Int32
}

func (c *rangeCountingBucket) Get(ctx context.Context, key string) ([]byte, error) {
	c.gets.Add(1)
	return c.Bucket.Get(ctx, key)
}

func (c *rangeCountingBucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	c.ranges.Add(1)
	return c.Bucket.(blob.RangeReader).GetRange(ctx, key, offset, length)
}

// scenario: conditional-ranged-read-stays-ranged (ADR-0159) — a ranged GET under a matching If-Match is
// a 206 with the range and the object's ETag, served by one range read and no whole-object read.
func TestScenarioConditionalRangedReadStaysRanged(t *testing.T) {
	var sub *rangeCountingBucket
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, func(t *testing.T) blob.Bucket {
		t.Helper()
		sub = &rangeCountingBucket{Bucket: memBucket(t)}
		return sub
	})
	ctx := context.Background()
	bucket, key := ptrS("lakehouse"), ptrS("bronze/big.parquet")
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16)
	put, err := g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: key, Body: bytes.NewReader(data),
	})
	require.NoError(t, err)

	out, err := g.client(t, "default", "analytics").GetObject(ctx, &awss3.GetObjectInput{
		Bucket: bucket, Key: key, Range: ptrS("bytes=0-1023"), IfMatch: put.ETag,
	})
	require.NoError(t, err)
	body, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	require.NoError(t, out.Body.Close())
	require.Equal(t, data[:1024], body)
	require.Equal(t, fmt.Sprintf("bytes 0-1023/%d", len(data)), aws.ToString(out.ContentRange), "a 206")
	require.Equal(t, aws.ToString(put.ETag), aws.ToString(out.ETag))
	require.Equal(t, int32(1), sub.ranges.Load(), "one range read")
	require.Zero(t, sub.gets.Load(), "no whole-object read")
}
