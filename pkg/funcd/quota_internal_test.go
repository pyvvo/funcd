package funcd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// quotaGateway starts a platform with the S3 gateway and seeds objs; client returns an S3 client of the principal
// kind/name in namespace default.
func quotaGateway(t *testing.T, objs ...v1.Object) (*Platform, func(kind v1.Kind, name v1.ObjectName) *awss3.Client) {
	t.Helper()
	dataDir := t.TempDir()
	p, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), WithS3Gateway(addr, "", 0, "", dataDir))
	}, s3GatewayRun)
	for _, o := range objs {
		_, err := p.cfg.store.Create(context.Background(), o)
		require.NoError(t, err)
	}
	master, err := s3gateway.LoadOrCreateMaster("", dataDir)
	require.NoError(t, err)
	return p, func(kind v1.Kind, name v1.ObjectName) *awss3.Client {
		kp := s3gateway.DeriveKeypair(master, kind, "default", string(name))
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"),
			awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(kp.AccessKey, kp.SecretKey, "")))
		require.NoError(t, err)
		endpoint := "http://" + addr
		return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
			o.RetryMaxAttempts = 1
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		})
	}
}

func quotaBucket(spec v1.BucketSpec) *v1.Bucket {
	b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}, Spec: spec}
	b.Name, b.Namespace, b.ResourceGroup = "media", "default", "rg1"
	return b
}

func quotaFunction(name v1.ObjectName) *v1.Function {
	f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	f.Name, f.Namespace, f.ResourceGroup = name, "default", "rg1"
	f.Spec = v1.FunctionSpec{Runtime: "nodejs22"}
	return f
}

func put(ctx context.Context, c *awss3.Client, key string, n int) error {
	_, err := c.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("media"), Key: aws.String(key), Body: bytes.NewReader(make([]byte, n))})
	return err
}

// requireQuotaExceeded asserts the S3 answer of ADR-0292 Decision 5: 507 QuotaExceeded.
func requireQuotaExceeded(t *testing.T, err error) {
	t.Helper()
	var re interface{ HTTPStatusCode() int }
	require.True(t, errors.As(err, &re), "%v", err)
	require.Equal(t, http.StatusInsufficientStorage, re.HTTPStatusCode(), "%v", err)
	var coded interface{ ErrorCode() string }
	require.True(t, errors.As(err, &coded))
	require.Equal(t, "QuotaExceeded", coded.ErrorCode())
}

// scenario: bucket-total-refused — a Bucket at its quota refuses an S3 client's put and a context.blob put with 507,
// stores nothing, and still answers a read and a delete; after the delete a put lands again.
func TestScenarioBucketTotalRefused(t *testing.T) {
	ctx := context.Background()
	p, client := quotaGateway(t, quotaBucket(v1.BucketSpec{
		Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: v1.FunctionOwner("writer")}},
		Quota:    v1.BucketQuota{SizeQuota: v1.SizeQuota{MaxBytes: 64}},
	}), quotaFunction("writer"))
	c := client(v1.KindFunction, "writer")

	require.NoError(t, put(ctx, c, "raw/a", 40))
	requireQuotaExceeded(t, put(ctx, c, "raw/b", 40))
	view, ok := s3BucketFor(p.cfg.blob, p.cfg.store, p.quotas)("default", "media")
	require.True(t, ok)
	found, err := view.Exists(ctx, "raw/b")
	require.NoError(t, err)
	require.False(t, found, "a refused put stores nothing")
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(view.Put(ctx, "raw/c", make([]byte, 40), blob.PutOptions{})),
		"context.blob and a Site's build write through the same view")

	got, err := view.Get(ctx, "raw/a")
	require.NoError(t, err, "a read still works")
	require.Len(t, got, 40)
	_, err = c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String("media"), Key: aws.String("raw/a")})
	require.NoError(t, err, "a delete still works")
	require.NoError(t, put(ctx, c, "raw/b", 40), "the fresh measure before a refusal sees the delete")
}

// scenario: lake-total-refused — a Lake whose prefix holds its quota gets 507 for its engine's next file, while the
// Bucket's other prefixes, which set no quota, take writes.
func TestScenarioLakeTotalRefused(t *testing.T) {
	ctx := context.Background()
	lake := &v1.Lake{TypeMeta: v1.TypeMeta{APIVersion: v1.KindLake.GVK().APIVersion(), Kind: v1.KindLake}}
	lake.Name, lake.Namespace, lake.ResourceGroup = "analytics", "default", "rg1"
	lake.Spec.Location = v1.LakeLocation{Bucket: "media", Prefix: "gold"}
	_, client := quotaGateway(t, quotaBucket(v1.BucketSpec{Prefixes: []v1.BucketPrefix{
		{Name: "gold", Owner: v1.LakeOwner("analytics"), Quota: v1.SizeQuota{MaxSize: "1Ki"}},
		{Name: "raw", Owner: v1.FunctionOwner("writer")},
	}}), quotaFunction("writer"))
	engine, writer := client(v1.KindLake, "analytics"), client(v1.KindFunction, "writer")

	require.NoError(t, put(ctx, engine, "gold/main/t/f1.parquet", 1000))
	err := put(ctx, engine, "gold/main/t/f2.parquet", 100)
	requireQuotaExceeded(t, err)
	require.ErrorContains(t, err, "prefix gold of bucket default/media holds 1000 of its 1024 bytes")
	require.NoError(t, put(ctx, writer, "raw/big.bin", 4096), "a prefix without a quota is not capped by another's")
}

// scenario: catalog-outside-bucket — a Lake's engine saves its catalog to the catalog store (ADR-0293): the Bucket's
// listing does not show it, its prefix's quota does not count it, and a Lake whose prefix holds its quota still saves.
func TestScenarioCatalogOutsideBucket(t *testing.T) {
	ctx := context.Background()
	p, client := quotaGateway(t, quotaBucket(v1.BucketSpec{Prefixes: []v1.BucketPrefix{
		{Name: "gold", Owner: v1.LakeOwner("analytics"), Quota: v1.SizeQuota{MaxBytes: 1000}},
	}}))
	engine := client(v1.KindLake, "analytics")
	catalog := func(n int) error {
		_, err := engine.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(v1.LakeCatalogBucket),
			Key: aws.String("media/gold/_ducklake/catalog.db"), Body: bytes.NewReader(make([]byte, n))})
		return err
	}

	require.NoError(t, put(ctx, engine, "gold/main/t/f1.parquet", 1000), "the files fill the prefix's quota")
	requireQuotaExceeded(t, put(ctx, engine, "gold/main/t/f2.parquet", 1))
	require.NoError(t, catalog(4096), "the catalog saves outside the prefix's quota")
	require.NoError(t, catalog(2048), "and again")
	stored, err := p.cfg.blob.Get(ctx, "lake-catalogs/default/media/gold/_ducklake/catalog.db")
	require.NoError(t, err)
	require.Len(t, stored, 2048)
	view, ok := s3BucketFor(p.cfg.blob, p.cfg.store, p.quotas)("default", "media")
	require.True(t, ok)
	items, err := view.List(ctx, "gold/")
	require.NoError(t, err)
	require.Len(t, items, 1, "the Bucket lists the Lake's file, not its catalog")
}

// scenario: purge-removes-catalogs — purging a Bucket deletes the catalogs of the Lakes on its prefixes, and no other.
func TestScenarioPurgeRemovesCatalogs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })
	for _, k := range []string{
		"s3/default/media/gold/main/f1.parquet",
		"lake-catalogs/default/media/gold/_ducklake/catalog.db",
		"lake-catalogs/default/media-two/gold/_ducklake/catalog.db",
		"lake-catalogs/orders/media/gold/_ducklake/catalog.db",
	} {
		require.NoError(t, shared.Put(ctx, k, []byte("x"), blob.PutOptions{}))
	}
	require.NoError(t, bucketPurger{shared: shared}.Purge(ctx, "default", "media"))
	for k, want := range map[string]bool{
		"s3/default/media/gold/main/f1.parquet":                     false,
		"lake-catalogs/default/media/gold/_ducklake/catalog.db":     false,
		"lake-catalogs/default/media-two/gold/_ducklake/catalog.db": true,
		"lake-catalogs/orders/media/gold/_ducklake/catalog.db":      true,
	} {
		found, err := shared.Exists(ctx, k)
		require.NoError(t, err)
		require.Equal(t, want, found, k)
	}
}

func TestLakeData(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"lake-catalogs/default/media/gold/_ducklake/catalog.db": "s3/default/media/gold/",
		"lake-catalogs/default/media/_ducklake/catalog.db":      "",
		"lake-catalogs/default/media/gold/catalog.db":           "",
		"s3/default/media/gold/_ducklake/catalog.db":            "",
	} {
		got, ok := LakeData(key)
		require.Equal(t, want, got, key)
		require.Equal(t, want != "", ok, key)
	}
}
