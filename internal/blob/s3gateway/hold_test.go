package s3gateway_test

import (
	"context"
	"errors"
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
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// heldIn is a hold with ns held for its data, as restore run leaves it.
func heldIn(t *testing.T, ns v1.NamespaceName) *hold.Hold {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{ns}, hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	return h
}

func withHeld(g hold.CallGate) func(*s3gateway.Deps) { return func(d *s3gateway.Deps) { d.Held = g } }

// requireHeld asserts ADR-0290 Decision 6's refusal: a 503 ServiceUnavailable whose message contains want.
func requireHeld(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, statusCode(err), err.Error())
	require.Equal(t, "ServiceUnavailable", errorCode(err))
	var msg interface{ ErrorMessage() string }
	require.True(t, errors.As(err, &msg))
	require.Contains(t, msg.ErrorMessage(), want)
}

// scenario: gateway-write-refused — while restore run holds default for its data, the owner's put, delete, batch
// delete and multipart upload through the S3 gateway get 503 and change nothing; after the release a put lands.
func TestScenarioGatewayWriteRefused(t *testing.T) {
	t.Parallel()
	h := heldIn(t, "default")
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withHeld(h))
	g.seed(t, "default", "lakehouse", "bronze/old.parquet", []byte("old"))
	c := g.client(t, "default", "etl-svc")
	ctx := context.Background()
	const want = "namespace default is held for its data since "

	_, err := c.PutObject(ctx, &awss3.PutObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/new.parquet"),
		Body: strings.NewReader("new")}, oneAttempt)
	requireHeld(t, err, want)
	_, err = c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/old.parquet")}, oneAttempt)
	requireHeld(t, err, want)
	_, err = c.DeleteObjects(ctx, &awss3.DeleteObjectsInput{Bucket: ptrS("lakehouse"), Delete: &awstypes.Delete{
		Objects: []awstypes.ObjectIdentifier{{Key: ptrS("bronze/old.parquet")}}}}, oneAttempt)
	requireHeld(t, err, want)
	_, err = c.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: ptrS("lakehouse"),
		Key: ptrS("bronze/big.parquet")}, oneAttempt)
	requireHeld(t, err, "writes resume after funcdctl hold release --namespace default")
	sub := g.buckets["default/lakehouse"]
	for key, want := range map[string]bool{"bronze/old.parquet": true, "bronze/new.parquet": false, "bronze/big.parquet": false} {
		got, err := sub.Exists(ctx, key)
		require.NoError(t, err)
		require.Equal(t, want, got, "a refused write changes nothing: %s", key)
	}

	require.NoError(t, h.ReleaseNamespace("default", hold.ReasonData))
	_, err = c.PutObject(ctx, &awss3.PutObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/new.parquet"),
		Body: strings.NewReader("new")}, oneAttempt)
	require.NoError(t, err, "after the release the put lands")
}

// scenario: gateway-read-served — a held namespace's Buckets still answer reads through the S3 gateway.
func TestScenarioGatewayReadServed(t *testing.T) {
	t.Parallel()
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withHeld(heldIn(t, "default")))
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("PAR1-gold-rows"))
	c := g.client(t, "default", "analytics")
	ctx := context.Background()

	out, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")}, oneAttempt)
	require.NoError(t, err)
	body, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	require.NoError(t, out.Body.Close())
	require.Equal(t, "PAR1-gold-rows", string(body))
	list, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: ptrS("lakehouse"), Prefix: ptrS("gold/")}, oneAttempt)
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
}

// scenario: gateway-restore-load-refused — while an in-place Restore loads the namespace, a part upload gets the 503,
// which names the Restore, and the upload begun before the hold can still be aborted.
func TestScenarioGatewayRestoreLoadRefused(t *testing.T) {
	t.Parallel()
	h, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withHeld(h))
	c := g.client(t, "default", "etl-svc")
	ctx := context.Background()
	up, err := c.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: ptrS("lakehouse"),
		Key: ptrS("bronze/big.parquet")}, oneAttempt)
	require.NoError(t, err)

	require.NoError(t, h.HoldNamespace("default", hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(time.Now()),
		Restore: "nightly-1"}))
	_, err = c.UploadPart(ctx, &awss3.UploadPartInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/big.parquet"),
		UploadId: up.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("part")}, oneAttempt)
	requireHeld(t, err, "namespace default is held while Restore nightly-1 loads it since ")
	_, err = c.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: ptrS("lakehouse"),
		Key: ptrS("bronze/big.parquet"), UploadId: up.UploadId}, oneAttempt)
	require.NoError(t, err, "an abort writes nothing, so the hold lets it through")
}

// scenario: gateway-auth-before-hold — a caller the PEP denies gets the 403 it gets today, which says nothing of the
// hold.
func TestScenarioGatewayAuthBeforeHold(t *testing.T) {
	t.Parallel()
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withHeld(heldIn(t, "default")))
	c := g.client(t, "default", "reporting")

	_, err := c.PutObject(context.Background(), &awss3.PutObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/x"),
		Body: strings.NewReader("x")}, oneAttempt)
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, statusCode(err))
	require.NotContains(t, err.Error(), "held")
}

// scenario: gateway-unheld-namespace-served — a hold on another namespace leaves this one's writes alone.
func TestScenarioGatewayUnheldNamespaceServed(t *testing.T) {
	t.Parallel()
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket, withHeld(heldIn(t, "orders")))
	c := g.client(t, "default", "etl-svc")

	_, err := c.PutObject(context.Background(), &awss3.PutObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/x"),
		Body: strings.NewReader("x")}, oneAttempt)
	require.NoError(t, err)
}
