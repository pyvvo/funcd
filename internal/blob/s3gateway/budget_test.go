package s3gateway_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awstypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

const budgetMaxUpload = 64 << 10

func oneAttempt(o *awss3.Options) { o.RetryMaxAttempts = 1 }

// errorCode extracts the S3 error code from an aws-sdk error, or "".
func errorCode(err error) string {
	var ae interface{ ErrorCode() string }
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func requireSlowDown(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
	require.Equal(t, 503, statusCode(err), msgAndArgs...)
	require.Equal(t, "SlowDown", errorCode(err), msgAndArgs...)
}

// scenario: multipart-share-per-principal (ADR-0188, issue #731) — one principal buffers at most
// maxUploadBytes of parts across all its uploads; past that its parts answer 503 SlowDown and buffer nothing.
func TestScenarioMultipartSharePerPrincipal(t *testing.T) {
	meta := lakehouseMeta()
	meta.fns["default/etl-svc"].Spec.Blob = []v1.FunctionBlob{{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}}
	g := newGateway(t, meta, fixedPolicies{rev: "0"}, nil, memBucket, func(d *s3gateway.Deps) {
		d.MaxUploadBytes = budgetMaxUpload
	})
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	bucket := ptrS("lakehouse")
	key := func(i int) *string { return ptrS(fmt.Sprintf("bronze/part-%02d.parquet", i)) }
	body := bytes.Repeat([]byte("x"), budgetMaxUpload)
	one := int32(1)
	uploadPart := func(i int, id *string) error {
		_, err := owner.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: bucket, Key: key(i), UploadId: id, PartNumber: &one, Body: bytes.NewReader(body),
		}, oneAttempt)
		return err
	}

	const uploads = 20
	ids := make([]*string, uploads)
	for i := range uploads {
		c, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key(i)}, oneAttempt)
		require.NoError(t, err)
		ids[i] = c.UploadId
	}
	require.NoError(t, uploadPart(0, ids[0]), "the first full-share part is buffered")
	for i := 1; i < uploads; i++ {
		requireSlowDown(t, uploadPart(i, ids[i]), "part of upload %d passes the principal's share", i)
		lp, err := owner.ListParts(ctx, &awss3.ListPartsInput{Bucket: bucket, Key: key(i), UploadId: ids[i]}, oneAttempt)
		require.NoError(t, err)
		require.Empty(t, lp.Parts, "a refused part buffers nothing (upload %d)", i)
	}

	_, err := owner.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: bucket, Key: key(0), UploadId: ids[0]}, oneAttempt)
	require.NoError(t, err)
	require.NoError(t, uploadPart(1, ids[1]), "the abort returned the share, so the retried part is buffered")
}

// blockingPutBucket is a substrate bucket whose Put reports its entry and waits for release.
type blockingPutBucket struct {
	blob.Bucket
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPutBucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	b.entered <- struct{}{}
	<-b.release
	return b.Bucket.Put(ctx, key, data, opts)
}

// scenario: concurrent-complete-single-flight (ADR-0188) — a second Complete of an upload id whose Complete is
// in flight answers 503 SlowDown instead of building another full copy; the first answers 200 once its Put returns.
func TestScenarioConcurrentCompleteSingleFlight(t *testing.T) {
	sub := &blockingPutBucket{entered: make(chan struct{}, 1), release: make(chan struct{})}
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, func(t *testing.T) blob.Bucket {
		t.Helper()
		sub.Bucket = memBucket(t)
		return sub
	}, func(d *s3gateway.Deps) { d.MaxUploadBytes = budgetMaxUpload })
	ctx := context.Background()
	owner := g.client(t, "default", "etl-svc")
	bucket, key := ptrS("lakehouse"), ptrS("bronze/single.parquet")
	one := int32(1)

	c, err := owner.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: bucket, Key: key}, oneAttempt)
	require.NoError(t, err)
	part, err := owner.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: bucket, Key: key, UploadId: c.UploadId, PartNumber: &one,
		Body: bytes.NewReader(bytes.Repeat([]byte("y"), budgetMaxUpload)),
	}, oneAttempt)
	require.NoError(t, err)
	complete := func() error {
		_, cerr := owner.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
			Bucket: bucket, Key: key, UploadId: c.UploadId,
			MultipartUpload: &awstypes.CompletedMultipartUpload{Parts: []awstypes.CompletedPart{{ETag: part.ETag, PartNumber: &one}}},
		}, oneAttempt)
		return cerr
	}

	first := make(chan error, 1)
	go func() { first <- complete() }()
	<-sub.entered
	second := make(chan error, 1)
	go func() { second <- complete() }()
	select {
	case err := <-second:
		requireSlowDown(t, err, "a second Complete while the first holds its copy")
	case <-sub.entered:
		close(sub.release)
		t.Fatal("a second Complete of the same id built its own copy and reached Put")
	}
	close(sub.release)
	require.NoError(t, <-first, "the first Complete answers 200 once its Put returns")
	require.Len(t, mustGet(t, g, "default", "lakehouse", "bronze/single.parquet"), budgetMaxUpload)
}
