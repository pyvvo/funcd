package s3gateway_test

import (
	"context"
	"io"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// scenario: deterministic-across-restart (ADR-0085) — DeriveKeypair is a pure function
// of (master, ns, fn): identical inputs yield an identical keypair, so a daemon restart
// re-derives the same credential (nothing stored, nothing rotated).
func TestScenarioDeterministicAcrossRestart(t *testing.T) {
	t.Parallel()
	a := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "analytics")
	b := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "analytics")
	require.Equal(t, a, b, "the same (master, ns, fn) must yield the same keypair across restarts")

	// A different function ⇒ a different secret (no collision).
	other := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "etl-svc")
	require.NotEqual(t, a.SecretKey, other.SecretKey)
	require.NotEqual(t, a.AccessKey, other.AccessKey)

	// A different master ⇒ a different secret (the master is load-bearing for isolation).
	diffMaster := s3gateway.DeriveKeypair([]byte("a-different-master-secret-value!"), v1.KindFunction, "default", "analytics")
	require.NotEqual(t, a.SecretKey, diffMaster.SecretKey)
}

// scenario: signs-as-self (ADR-0085) — a request SigV4-signed with function A's
// injected keypair authenticates as A; the backend principal is A's Ref and the Cedar
// PEP runs on it (A reads its bound prefix).
func TestScenarioSignsAsSelf(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("self-rows"))

	c := g.client(t, "default", "analytics") // analytics's own derived keypair
	out, err := c.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err, "A signs as itself and the PEP grants its bound read")
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	require.Equal(t, "self-rows", string(body))
}

// scenario: cannot-forge-peer (ADR-0085) — function A (holding only its own secret)
// tries to sign as function B's access key; SigV4 fails (403) because A cannot derive
// B's secret. Tenant isolation holds without the connection source.
func TestScenarioCannotForgePeer(t *testing.T) {
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	g.seed(t, "default", "lakehouse", "bronze/x.parquet", []byte("owned"))

	// B = etl-svc's access key, but signed with A's (analytics's) secret — a forgery.
	bAccess := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "etl-svc").AccessKey
	aSecret := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "analytics").SecretKey
	forged := g.clientWithKeys(t, bAccess, aSecret)

	_, err := forged.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("bronze/x.parquet")})
	require.Error(t, err, "signing B's access key with A's secret must fail SigV4")
	require.Equal(t, 403, statusCode(err))
}

// scenario: external-sigv4 / external-keypair (ADR-0080/0085) — a stored external
// keypair (an S3Identity) granted read by an admin Policy reads an object; a request
// with a bad signature for that access key is 403.
func TestScenarioExternalSigV4(t *testing.T) {
	meta := lakehouseMeta()
	// An admin Policy granting the external S3Identity s3::read on lakehouse/gold.
	pol := v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "ext-read", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `permit(principal == S3Identity::"default/EXTACCESS", action == Action::"s3::read", resource == BlobPrefix::"default/lakehouse/gold");`},
	}
	external := staticExternal{
		"EXTACCESS": {secret: "ext-secret-value", namespace: "default"},
	}
	g := newGateway(t, meta, fixedPolicies{policies: []v1.Policy{pol}, rev: "1"}, external, memBucket)
	g.seed(t, "default", "lakehouse", "gold/q.parquet", []byte("ext-rows"))

	ok := g.clientWithKeys(t, "EXTACCESS", "ext-secret-value")
	out, err := ok.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.NoError(t, err, "the external S3Identity reads gold via the admin Policy")
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	require.Equal(t, "ext-rows", string(body))

	// Bad signature: the right access key, the wrong secret ⇒ 403.
	bad := g.clientWithKeys(t, "EXTACCESS", "wrong-secret")
	_, err = bad.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("gold/q.parquet")})
	require.Error(t, err, "a bad signature for the external access key is rejected")
	require.Equal(t, 403, statusCode(err))
}
