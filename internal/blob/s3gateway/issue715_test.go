package s3gateway_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"unicode/utf8"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// countingBucket counts the listing entries storage returns to the gateway and records each ListAfter seek.
type countingBucket struct {
	blob.Bucket
	mu      sync.Mutex
	entries int
	afters  []string
}

func (c *countingBucket) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
	items, err := c.Bucket.List(ctx, prefix)
	c.record(len(items), "")
	return items, err
}

func (c *countingBucket) ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	items, more, err := c.Bucket.ListAfter(ctx, prefix, after, limit)
	c.record(len(items), after)
	return items, more, err
}

func (c *countingBucket) record(n int, after string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries += n
	c.afters = append(c.afters, after)
}

// counts returns the entries returned so far and the seek of every storage call.
func (c *countingBucket) counts() (entries int, afters []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries, append([]string(nil), c.afters...)
}

func fileBucket(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), gocloud.FileURL(t.TempDir()))
	require.NoError(t, err)
	return b
}

// eachSubstrate runs fn on the mem and file drivers, in parallel: seeding thousands of file keys is slow.
func eachSubstrate(t *testing.T, fn func(t *testing.T, open func(t *testing.T) blob.Bucket)) {
	t.Helper()
	t.Parallel()
	for name, open := range map[string]func(t *testing.T) blob.Bucket{"mem": memBucket, "file": fileBucket} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, open)
		})
	}
}

// countedGateway starts a gateway over meta on one substrate behind a fresh counting wrapper.
func countedGateway(t *testing.T, meta fakeMeta, open func(t *testing.T) blob.Bucket) (*gw, *countingBucket) {
	t.Helper()
	cb := &countingBucket{Bucket: open(t)}
	return newGateway(t, meta, fixedPolicies{rev: "0"}, nil, func(*testing.T) blob.Bucket { return cb }), cb
}

// listAllV2 pages ListObjectsV2 until IsTruncated is false and returns every key and common prefix in order.
// Each page's NextContinuationToken goes to onPage before the next request.
func listAllV2(t *testing.T, c *awss3.Client, in awss3.ListObjectsV2Input, onPage func(out *awss3.ListObjectsV2Output)) (keys, prefixes []string) {
	t.Helper()
	for pages := 1; ; pages++ {
		out, err := c.ListObjectsV2(context.Background(), &in)
		require.NoError(t, err, "page %d", pages)
		keys = append(keys, objectKeys(out.Contents)...)
		prefixes = append(prefixes, commonPrefixes(out.CommonPrefixes)...)
		if onPage != nil {
			onPage(out)
		}
		if !*out.IsTruncated {
			return keys, prefixes
		}
		require.NotNil(t, out.NextContinuationToken, "a truncated page carries a continuation token")
		require.Less(t, pages, 100_000, "pagination does not terminate")
		in.ContinuationToken = out.NextContinuationToken
	}
}

// Issue 715: a full paged listing makes storage return each key about once, not every key once per page.
func TestIssue715_PagedListingIsLinear(t *testing.T) {
	eachSubstrate(t, testPagedListingLinear)
}

// scenario: paged-listing-linear (ADR-0184) — the #715 reproduction.
func TestScenarioPagedListingLinear(t *testing.T) {
	eachSubstrate(t, testPagedListingLinear)
}

func testPagedListingLinear(t *testing.T, open func(t *testing.T) blob.Bucket) {
	const n = 10_000
	g, cb := countedGateway(t, lakehouseMeta(), open)
	want := make([]string, 0, n)
	for i := range n {
		k := fmt.Sprintf("gold/p%06d.parquet", i)
		want = append(want, k)
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	in := awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/")}
	keys, _ := listAllV2(t, g.client(t, "default", "analytics"), in, nil)
	require.Equal(t, want, keys, "every key once, in key order")
	entries, _ := cb.counts()
	require.LessOrEqual(t, entries, 2*n, "storage returned %d entries for %d keys", entries, n)
}

// scenario: continuation-is-stateless (ADR-0184) — a token is the page's last key or common prefix and resumes
// the listing on a new gateway over the same substrate, as StartAfter (V2) and Marker (V1) do.
func TestScenarioContinuationIsStateless(t *testing.T) {
	dir := t.TempDir()
	openDir := func(t *testing.T) blob.Bucket {
		b, err := gocloud.Open(context.Background(), gocloud.FileURL(dir))
		require.NoError(t, err)
		return b
	}
	ctx := context.Background()
	two := int32(2)
	first := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, openDir)
	for _, k := range []string{"gold/a", "gold/b", "gold/c", "gold/d", "gold/e/1", "gold/f/1", "gold/g/1"} {
		first.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	flat := &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), MaxKeys: &two}
	rolled := &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"), MaxKeys: &two, StartAfter: ptrS("gold/d")}
	c := first.client(t, "default", "analytics")
	flatPage, err := c.ListObjectsV2(ctx, flat)
	require.NoError(t, err)
	require.Equal(t, []string{"gold/a", "gold/b"}, objectKeys(flatPage.Contents))
	require.Equal(t, "gold/b", *flatPage.NextContinuationToken, "the token is the page's last key")
	rolledPage, err := c.ListObjectsV2(ctx, rolled)
	require.NoError(t, err)
	require.Equal(t, []string{"gold/e/", "gold/f/"}, commonPrefixes(rolledPage.CommonPrefixes))
	require.Equal(t, "gold/f/", *rolledPage.NextContinuationToken, "the token is the page's last common prefix")
	require.NoError(t, first.server.Close())

	c = newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, openDir).client(t, "default", "analytics")
	for _, tc := range []struct {
		in        *awss3.ListObjectsV2Input
		token     string
		delimiter *string
		keys      []string
		prefixes  []string
	}{
		{in: flat, token: "gold/b", keys: []string{"gold/c", "gold/d"}, prefixes: []string{}},
		{in: rolled, token: "gold/f/", delimiter: ptrS("/"), keys: []string{}, prefixes: []string{"gold/g/"}},
	} {
		next := *tc.in
		next.StartAfter = nil
		next.ContinuationToken = ptrS(tc.token)
		byToken, err := c.ListObjectsV2(ctx, &next)
		require.NoError(t, err)
		require.Equal(t, tc.keys, objectKeys(byToken.Contents), "token %q", tc.token)
		require.Equal(t, tc.prefixes, commonPrefixes(byToken.CommonPrefixes), "token %q", tc.token)

		next.ContinuationToken, next.StartAfter = nil, ptrS(tc.token)
		byStartAfter, err := c.ListObjectsV2(ctx, &next)
		require.NoError(t, err)
		require.Equal(t, objectKeys(byToken.Contents), objectKeys(byStartAfter.Contents), "StartAfter %q", tc.token)
		require.Equal(t, commonPrefixes(byToken.CommonPrefixes), commonPrefixes(byStartAfter.CommonPrefixes), "StartAfter %q", tc.token)

		byMarker, err := c.ListObjects(ctx, &awss3.ListObjectsInput{
			Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: tc.delimiter, MaxKeys: &two, Marker: ptrS(tc.token),
		})
		require.NoError(t, err)
		require.Equal(t, objectKeys(byToken.Contents), objectKeys(byMarker.Contents), "Marker %q", tc.token)
		require.Equal(t, commonPrefixes(byToken.CommonPrefixes), commonPrefixes(byMarker.CommonPrefixes), "Marker %q", tc.token)
	}
}

// scenario: rollup-skips-subtree (ADR-0184) — the seek past a common prefix skips the keys it rolls up.
func TestScenarioRollupSkipsSubtree(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, open func(t *testing.T) blob.Bucket) {
		g, cb := countedGateway(t, lakehouseMeta(), open)
		for i := range 5000 {
			g.seed(t, "default", "lakehouse", fmt.Sprintf("gold/a/k%04d", i), []byte("x"))
		}
		g.seed(t, "default", "lakehouse", "gold/b.parquet", []byte("x"))
		out, err := g.client(t, "default", "analytics").ListObjectsV2(context.Background(), &awss3.ListObjectsV2Input{
			Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/"),
		})
		require.NoError(t, err)
		require.Equal(t, []string{"gold/a/"}, commonPrefixes(out.CommonPrefixes))
		require.Equal(t, []string{"gold/b.parquet"}, objectKeys(out.Contents))
		require.False(t, *out.IsTruncated)
		entries, _ := cb.counts()
		require.LessOrEqual(t, entries, 2000)
	})
}

// scenario: delimiter-pages-many-partitions (ADR-0184) — a delimiter listing over many partitions is linear,
// and each page after a common-prefix token T seeks storage past T+U+10FFFF.
func TestScenarioDelimiterPagesManyPartitions(t *testing.T) {
	const parts, perPart = 3000, 10
	eachSubstrate(t, func(t *testing.T, open func(t *testing.T) blob.Bucket) {
		g, cb := countedGateway(t, lakehouseMeta(), open)
		want := make([]string, 0, parts)
		for p := range parts {
			cp := fmt.Sprintf("gold/d=%04d/", p)
			want = append(want, cp)
			for k := range perPart {
				g.seed(t, "default", "lakehouse", fmt.Sprintf("%sk%02d", cp, k), []byte("x"))
			}
		}
		calls := 0
		var token string
		onPage := func(out *awss3.ListObjectsV2Output) {
			_, afters := cb.counts()
			page := afters[calls:]
			calls = len(afters)
			require.LessOrEqual(t, len(page), 50, "storage calls for the page after %q", token)
			if token != "" {
				require.Equal(t, token+string(utf8.MaxRune), page[0], "the first seek after token %q", token)
			}
			if *out.IsTruncated {
				token = *out.NextContinuationToken
				cps := commonPrefixes(out.CommonPrefixes)
				require.Equal(t, cps[len(cps)-1], token, "the token is the page's last common prefix")
			}
		}
		in := awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), Delimiter: ptrS("/")}
		keys, prefixes := listAllV2(t, g.client(t, "default", "analytics"), in, onPage)
		require.Empty(t, keys)
		require.Equal(t, want, prefixes, "every common prefix once, in order")
		entries, _ := cb.counts()
		require.LessOrEqual(t, entries, 2*parts*perPart)
	})
}

// scenario: maxkeys-zero (ADR-0184) — MaxKeys 0 answers an empty, untruncated page without a storage call.
func TestScenarioMaxkeysZero(t *testing.T) {
	g, cb := countedGateway(t, lakehouseMeta(), memBucket)
	g.seed(t, "default", "lakehouse", "gold/a.parquet", []byte("x"))
	zero := int32(0)
	out, err := g.client(t, "default", "analytics").ListObjectsV2(context.Background(), &awss3.ListObjectsV2Input{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), MaxKeys: &zero,
	})
	require.NoError(t, err)
	require.Empty(t, out.Contents)
	require.Empty(t, out.CommonPrefixes)
	require.False(t, *out.IsTruncated)
	_, afters := cb.counts()
	require.Empty(t, afters, "no storage call")
}

// scenario: pages-are-not-a-snapshot (ADR-0184) — each page reflects storage at its request.
func TestScenarioPagesAreNotASnapshot(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	for _, k := range []string{"gold/a", "gold/b", "gold/c", "gold/d"} {
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	c := g.client(t, "default", "analytics")
	ctx := context.Background()
	two := int32(2)
	page1, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), MaxKeys: &two})
	require.NoError(t, err)
	require.Equal(t, []string{"gold/a", "gold/b"}, objectKeys(page1.Contents))

	g.seed(t, "default", "lakehouse", "gold/bb", []byte("x"))
	require.NoError(t, g.buckets["default/lakehouse"].Delete(ctx, "gold/c"))
	page2, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), MaxKeys: &two, ContinuationToken: page1.NextContinuationToken,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gold/bb", "gold/d"}, objectKeys(page2.Contents))
}

// scenario: forged-token-bounded (ADR-0184) — a token grants nothing: it only seeks within the authorized Prefix.
func TestScenarioForgedTokenBounded(t *testing.T) {
	meta := lakehouseMeta()
	meta.buckets["default/lakehouse"].Spec.Prefixes = append(meta.buckets["default/lakehouse"].Spec.Prefixes, v1.BucketPrefix{Name: "silver"})
	meta.fns["default/gold-reader"] = &v1.Function{
		ObjectMeta: v1.ObjectMeta{Name: "gold-reader", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Blob: []v1.FunctionBlob{{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}}},
	}
	g, cb := countedGateway(t, meta, memBucket)
	for _, k := range []string{"gold/a", "gold/b", "silver/s"} {
		g.seed(t, "default", "lakehouse", k, []byte("x"))
	}
	c := g.client(t, "default", "gold-reader")
	ctx := context.Background()

	before, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), ContinuationToken: ptrS("a")})
	require.NoError(t, err)
	require.Equal(t, []string{"gold/a", "gold/b"}, objectKeys(before.Contents), "a token before the prefix lists its first page")

	past, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/"), ContinuationToken: ptrS("zzz")})
	require.NoError(t, err)
	require.Empty(t, past.Contents)
	require.False(t, *past.IsTruncated)

	_, calls := cb.counts()
	for _, token := range []string{"a", "gold/a", "silver/", "zzz"} {
		_, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("silver/"), ContinuationToken: ptrS(token)})
		require.Error(t, err, "token %q", token)
		require.Equal(t, 403, statusCode(err), "token %q", token)
	}
	_, after := cb.counts()
	require.Len(t, after, len(calls), "a denied listing makes no storage call")
}
