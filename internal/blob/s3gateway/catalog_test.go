package s3gateway_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

const goldCatalog = "lakehouse/gold/_ducklake/catalog.db"

// catalogMeta is lakehouseMeta with Lake analytics owning gold, and Lake other owning nothing.
func catalogMeta() fakeMeta {
	m := lakehouseMeta()
	m.buckets["default/lakehouse"].Spec.Prefixes[1].Owner = v1.LakeOwner("analytics")
	m.css = map[string]*v1.Lake{
		"default/analytics": {ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"}},
		"default/other":     {ObjectMeta: v1.ObjectMeta{Name: "other", Namespace: "default", ResourceGroup: "rg1"}},
	}
	return m
}

// withCatalogs serves the catalog store from store, one view per namespace, capped at maxBytes.
func withCatalogs(store blob.Bucket, maxBytes int64) func(*s3gateway.Deps) {
	return func(d *s3gateway.Deps) {
		d.Catalogs = func(ns v1.NamespaceName) blob.Bucket { return blob.Prefixed(store, "lake-catalogs/"+string(ns)+"/") }
		d.CatalogMaxBytes = maxBytes
	}
}

func putCatalog(c *awss3.Client, key, body string) error {
	_, err := c.PutObject(context.Background(), &awss3.PutObjectInput{Bucket: aws.String(v1.LakeCatalogBucket), Key: aws.String(key),
		Body: strings.NewReader(body)}, oneAttempt)
	return err
}

// scenario: catalog-only-its-lake — the Lake that owns lakehouse/gold puts, heads and gets gold's catalog in the
// catalog store; a Function bound to read gold, another Lake, and the owner itself for any other key or operation get
// AccessDenied, and the store is in no listing.
func TestScenarioCatalogOnlyItsLake(t *testing.T) {
	t.Parallel()
	store := memBucket(t)
	g := newGateway(t, catalogMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withCatalogs(store, 1<<20))
	ctx := context.Background()
	owner := g.clientAs(t, v1.KindLake, "default", "analytics")

	require.NoError(t, putCatalog(owner, goldCatalog, "DUCK-catalog"))
	stored, err := store.Get(ctx, "lake-catalogs/default/"+goldCatalog)
	require.NoError(t, err)
	require.Equal(t, "DUCK-catalog", string(stored), "the catalog lands in the store, outside the Bucket")
	found, err := g.buckets["default/lakehouse"].Exists(ctx, "gold/_ducklake/catalog.db")
	require.NoError(t, err)
	require.False(t, found)
	head, err := owner.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(v1.LakeCatalogBucket), Key: aws.String(goldCatalog)}, oneAttempt)
	require.NoError(t, err)
	require.Equal(t, int64(len("DUCK-catalog")), aws.ToInt64(head.ContentLength))
	got, err := owner.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(v1.LakeCatalogBucket), Key: aws.String(goldCatalog),
		Range: aws.String("bytes=0-3")}, oneAttempt)
	require.NoError(t, err)
	body, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	require.NoError(t, got.Body.Close())
	require.Equal(t, "DUCK", string(body))

	denied := func(err error, what string) {
		t.Helper()
		require.Error(t, err, what)
		require.Equal(t, http.StatusForbidden, statusCode(err), "%s: %v", what, err)
	}
	for who, c := range map[string]*awss3.Client{
		"a Function bound to read gold":  g.client(t, "default", "analytics"),
		"a Lake that owns no gold":       g.clientAs(t, v1.KindLake, "default", "other"),
		"the owner in another namespace": g.clientAs(t, v1.KindLake, "orders", "analytics"),
	} {
		_, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(v1.LakeCatalogBucket), Key: aws.String(goldCatalog)}, oneAttempt)
		denied(err, who+" reads")
		denied(putCatalog(c, goldCatalog, "x"), who+" writes")
	}
	denied(putCatalog(owner, "lakehouse/bronze/_ducklake/catalog.db", "x"), "a prefix the owner does not own")
	denied(putCatalog(owner, "lakehouse/gold/other.db", "x"), "a key that is no catalog")
	_, err = owner.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(v1.LakeCatalogBucket), Key: aws.String(goldCatalog)}, oneAttempt)
	denied(err, "a delete")
	_, err = owner.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(v1.LakeCatalogBucket)}, oneAttempt)
	denied(err, "a listing")
	_, err = owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: aws.String(v1.LakeCatalogBucket),
		Key: aws.String(goldCatalog)}, oneAttempt)
	denied(err, "a multipart upload")
	list, err := owner.ListBuckets(ctx, &awss3.ListBucketsInput{}, oneAttempt)
	require.NoError(t, err)
	require.NotContains(t, bucketNames(list.Buckets), v1.LakeCatalogBucket)
	still, err := store.Get(ctx, "lake-catalogs/default/"+goldCatalog)
	require.NoError(t, err)
	require.Equal(t, "DUCK-catalog", string(still), "no refused call changed the catalog")
}

func bucketNames(bs []awstypes.Bucket) []string {
	var out []string
	for _, b := range bs {
		out = append(out, aws.ToString(b.Name))
	}
	return out
}

// scenario: catalog-cap-refused — a catalog over lake.catalogMaxSize gets EntityTooLarge naming the setting, the stored
// catalog stays, and while the namespace is held for its data a save gets the hold's 503.
func TestScenarioCatalogCapRefused(t *testing.T) {
	t.Parallel()
	store := memBucket(t)
	h := heldIn(t, "orders")
	g := newGateway(t, catalogMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withCatalogs(store, 16), withHeld(h))
	owner := g.clientAs(t, v1.KindLake, "default", "analytics")

	require.NoError(t, putCatalog(owner, goldCatalog, strings.Repeat("c", 16)), "a catalog at the cap saves")
	err := putCatalog(owner, goldCatalog, strings.Repeat("c", 17))
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, statusCode(err))
	require.Equal(t, "EntityTooLarge", errorCode(err))
	require.ErrorContains(t, err, "the catalog is 17 bytes, over lake.catalogMaxSize (16 bytes)")
	stored, gerr := store.Get(context.Background(), "lake-catalogs/default/"+goldCatalog)
	require.NoError(t, gerr)
	require.Len(t, stored, 16)

	require.NoError(t, h.HoldNamespace("default", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	requireHeld(t, putCatalog(owner, goldCatalog, "c"), "namespace default is held for its data since ")
}
