package s3gateway_test

import (
	"context"
	"io"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// spineMeta seeds one bucket "lakehouse" with prefix "gold" (no owner, read-only) and a
// function "analytics" bound to it via spec.blob.
func spineMeta() fakeMeta {
	return fakeMeta{
		fns: map[string]*v1.Function{
			"default/analytics": {
				ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"},
				Spec: v1.FunctionSpec{Blob: []v1.FunctionBlob{
					{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"},
				}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold"}}},
			},
		},
	}
}

// TestSpine retires the integration risk (ADR-0085): the real aws-sdk-go-v2 s3 client,
// SigV4-signing with the DERIVED keypair, makes a request that authenticates through the
// in-process IAM and reaches the funcd backend. HeadBucket proves the chain; a bound
// GetObject proves the PEP + blob read end-to-end.
func TestSpine(t *testing.T) {
	g := newGateway(t, spineMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	c := g.client(t, "default", "analytics")
	ctx := context.Background()

	// HeadBucket: SigV4 → iam.GetUserAccount derives the secret → signature verifies →
	// backend.HeadBucket resolves the namespace bucket.
	_, err := c.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: ptrS("lakehouse")})
	require.NoError(t, err, "derived-keypair HeadBucket must authenticate and reach the backend")

	// Seed an object and read it through the gateway (read PEP grants via the binding).
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("PAR1rows"))
	out, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err, "bound read must pass the PEP and serve the bytes")
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	require.Equal(t, "PAR1rows", string(body))
}

func ptrS(s string) *string { return &s }
