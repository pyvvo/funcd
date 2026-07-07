package s3gateway_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	authz "github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/cedar"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/blob/s3gateway"
)

// testMaster is a fixed node master secret for the gateway tests (deterministic keypairs).
//
//nolint:gochecknoglobals // a fixed test fixture shared across the suite's table tests
var testMaster = []byte("test-node-master-secret-0123456789")

// fakeMeta is an in-memory cedar MetaReader over seeded Functions + Buckets (the harness
// mirror of the cedar package's s3Meta).
type fakeMeta struct {
	fns     map[string]*v1.Function
	buckets map[string]*v1.Bucket
}

func (m fakeMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	k := string(ns) + "/" + string(name)
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[k]; ok {
			return f, nil
		}
	case v1.KindBucket:
		if b, ok := m.buckets[k]; ok {
			return b, nil
		}
	}
	return nil, fault.NotFoundf("fakeMeta.Get", "%s %s not found", gvk.Kind, k)
}

// fixedPolicies is a PolicySource with no user policies (the binding alone grants read).
type fixedPolicies struct {
	policies []v1.Policy
	rev      string
}

func (f fixedPolicies) Policies(_ context.Context) ([]v1.Policy, string, error) {
	return f.policies, f.rev, nil
}

// staticExternal is an in-memory ExternalKeys store for the external-sigv4 scenarios.
type staticExternal map[string]struct {
	secret    string
	namespace string
}

func (s staticExternal) Lookup(access string) (string, string, bool) {
	e, ok := s[access]
	return e.secret, e.namespace, ok
}

// gw is a running in-process gateway plus the substrate it serves, for one test.
type gw struct {
	endpoint string
	buckets  map[string]blob.Bucket // keyed "<ns>/<bucket>"
	server   *s3gateway.Server
}

// newGateway builds the full in-process stack (ADR-0080/0085): a real gocloud mem://
// bucket per seeded Bucket, a real cedar PDP over the seeded resources, and the gateway
// on an ephemeral loopback port. policies/external may be nil. It registers cleanup.
func newGateway(t *testing.T, meta fakeMeta, policies fixedPolicies, external s3gateway.ExternalKeys, makeBucket func(t *testing.T) blob.Bucket) *gw {
	t.Helper()
	ctx := context.Background()

	ep, err := cedar.NewEntityProvider(meta)
	require.NoError(t, err)
	pdp, err := cedar.New(cedar.Deps{Entities: ep, Policies: policies})
	require.NoError(t, err)

	buckets := map[string]blob.Bucket{}
	for k := range meta.buckets {
		b := makeBucket(t)
		buckets[k] = b
		t.Cleanup(func() { _ = b.Close() })
	}

	bucketFor := func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		b, ok := buckets[string(ns)+"/"+bucket]
		return b, ok
	}

	srv, err := s3gateway.New(s3gateway.Deps{
		BucketFor: bucketFor,
		PDP:       pdp,
		Master:    testMaster,
		External:  external,
		Listen:    freeAddr(t),
		Logger:    nil,
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = srv.Run(runCtx) }()
	t.Cleanup(func() { cancel(); _ = srv.Close() })

	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not become ready")
	}

	return &gw{endpoint: "http://" + srv.Addr(), buckets: buckets, server: srv}
}

func memBucket(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	return b
}

// client builds a real aws-sdk-go-v2 s3 client (path-style, derived keypair, BaseEndpoint
// = the gateway) for an in-platform function (ns, fn).
func (g *gw) client(t *testing.T, ns, fn string) *awss3.Client {
	t.Helper()
	kp := s3gateway.DeriveKeypair(testMaster, ns, fn)
	return g.clientWithKeys(t, kp.AccessKey, kp.SecretKey)
}

// clientWithKeys builds a client with explicit keys (for forge / external tests).
func (g *gw) clientWithKeys(t *testing.T, access, secret string) *awss3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(access, secret, "")),
	)
	require.NoError(t, err)
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &g.endpoint
		o.UsePathStyle = true
		// Don't emit the aws-sdk-go-v2 default streaming CRC32 trailer: versitygw
		// rejects the unsupported trailer with 501. A plain signed payload (what
		// DuckDB/httpfs sends against a non-AWS endpoint) is what funcd serves.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// seed writes bytes directly into the substrate for a (ns, bucket, key), bypassing the
// gateway (used to set up reads).
func (g *gw) seed(t *testing.T, ns, bucket, key string, data []byte) {
	t.Helper()
	b, ok := g.buckets[ns+"/"+bucket]
	require.True(t, ok, "no substrate bucket %s/%s", ns, bucket)
	require.NoError(t, b.Put(context.Background(), key, data))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// statusCode extracts an HTTP status from an aws-sdk error (403 etc.), or 0.
func statusCode(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

// ensure authz import is used (the EntityRef type underlies the PEP).
var _ = authz.ActionS3Read
