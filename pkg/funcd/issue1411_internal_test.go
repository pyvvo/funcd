package funcd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const issue1411Err = "metastore read failed (issue 1411)"

// bucketLookupFails fails each read of a Bucket made with context.Background(), the context s3BucketFor reads with,
// while on is set. The S3 policy check reads with the request's context, so its read still succeeds, as when a store
// error hits only the second of a request's two reads.
type bucketLookupFails struct {
	store.Store
	on *atomic.Bool
}

func (s bucketLookupFails) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if s.on.Load() && gvk.Kind == v1.KindBucket && ctx == context.Background() {
		return nil, fault.Unavailablef("issue1411.Get", issue1411Err)
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// seedLakeWriter creates Bucket lake, whose prefix raw Function writer owns and binds as raw.
func seedLakeWriter(t *testing.T, st store.Store) {
	t.Helper()
	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lake", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: v1.FunctionOwner("writer")}}}
	writer := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	writer.Name, writer.Namespace, writer.ResourceGroup = "writer", "default", "rg1"
	writer.Spec = v1.FunctionSpec{Runtime: "nodejs22", Bindings: v1.FunctionBindings{Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "lake", Prefix: "raw"}}}}
	for _, obj := range []v1.Object{bucket, writer} {
		_, err := st.Create(context.Background(), obj)
		require.NoError(t, err, "seed %s", obj.GroupVersionKind().Kind)
	}
}

// Through the real S3 gateway, a failed lookup read answers 500 InternalError, which S3 clients retry, and is logged.
func TestIssue1411_AFailedBucketLookupAnswersInternalErrorAndIsLogged(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	var on atomic.Bool
	logs := &syncBuffer{}
	st := bucketLookupFails{Store: store.New(memory.New()), on: &on}
	_, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), WithStore(st), WithLogger(slog.New(slog.NewTextHandler(logs, nil))), WithS3Gateway(addr, "", 0, "", dataDir))
	}, s3GatewayRun)
	seedLakeWriter(t, st)

	master, err := s3gateway.LoadOrCreateMaster("", dataDir)
	require.NoError(t, err)
	kp := s3gateway.DeriveKeypair(master, v1.KindFunction, "default", "writer")
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(kp.AccessKey, kp.SecretKey, "")))
	require.NoError(t, err)
	endpoint := "http://" + addr
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	put := func(key string) error {
		_, perr := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("lake"), Key: aws.String(key), Body: bytes.NewReader([]byte("row"))})
		return perr
	}

	require.NoError(t, put("raw/before.parquet"))
	on.Store(true)
	perr := put("raw/during.parquet")
	on.Store(false)
	var re interface{ HTTPStatusCode() int }
	var coded interface{ ErrorCode() string }
	require.True(t, errors.As(perr, &re) && errors.As(perr, &coded), "an S3 error, got %v", perr)
	require.Equal(t, http.StatusInternalServerError, re.HTTPStatusCode(), "a failed lookup is no NoSuchBucket: %v", perr)
	require.Equal(t, "InternalError", coded.ErrorCode())
	require.Contains(t, logs.String(), "s3 bucket lookup error")
	require.Contains(t, logs.String(), issue1411Err, "the log names the store error")
	require.NoError(t, put("raw/after.parquet"), "the next write lands")
}

// context.blob keeps the kind of a failed lookup read instead of reporting a missing Bucket.
func TestIssue1411_ContextBlobKeepsAFailedBucketLookup(t *testing.T) {
	ctx := t.Context()
	var on atomic.Bool
	st := bucketLookupFails{Store: store.New(memory.New()), on: &on}
	p, err := New(InMemory(), WithStore(st))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	seedLakeWriter(t, st)

	require.NoError(t, p.blob.facade.CheckRead(ctx, "default", "writer", "raw"))
	on.Store(true)
	cerr := p.blob.facade.CheckRead(ctx, "default", "writer", "raw")
	on.Store(false)
	require.Equal(t, fault.Unavailable, fault.KindOf(cerr), "a failed lookup is no missing Bucket: %v", cerr)
}

// The static handler answers a failed lookup read with its 5xx; a Bucket that does not exist stays a 404.
func TestIssue1411_StaticAnswersAFailedBucketLookupWith5xx(t *testing.T) {
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Close() })
	var on atomic.Bool
	st := bucketLookupFails{Store: store.New(memory.New()), on: &on}
	seedLakeWriter(t, st)
	require.NoError(t, shared.Put(ctx, bucketPrefix("default", "lake")+"raw/index.html", []byte("<title>lake</title>"), blob.PutOptions{}))
	h, err := static.New(static.Deps{Buckets: s3BucketFor(shared, st, quota.None)})
	require.NoError(t, err)
	serve := func(bucket v1.ObjectName) int {
		rec := httptest.NewRecorder()
		h.Serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "default", &v1.StaticBackend{Bucket: bucket, Prefix: "raw/"}, "/")
		return rec.Code
	}

	require.Equal(t, http.StatusOK, serve("lake"))
	on.Store(true)
	code := serve("lake")
	on.Store(false)
	require.Equal(t, http.StatusServiceUnavailable, code, "a failed lookup is no missing Bucket")
	require.Equal(t, http.StatusNotFound, serve("absent"), "a Bucket that does not exist stays a 404")
}
