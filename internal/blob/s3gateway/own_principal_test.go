package s3gateway_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// lakeMeta seeds Function "lake" (no spec.blob) and CatalogService "lake" bound to lakehouse/raw, in one
// namespace: the shared name the engine and the Function must not merge.
func lakeMeta() fakeMeta {
	return fakeMeta{
		fns: map[string]*v1.Function{
			"default/lake": {ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"}},
		},
		css: map[string]*v1.CatalogService{
			"default/lake": {
				ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.CatalogServiceSpec{Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "lakehouse", Prefix: "raw"}}},
			},
		},
		buckets: map[string]*v1.Bucket{
			"default/lakehouse": {
				ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw"}}},
			},
		},
	}
}

func getRaw(t *testing.T, c *awss3.Client) ([]byte, error) {
	t.Helper()
	out, err := c.GetObject(context.Background(), &awss3.GetObjectInput{Bucket: ptrS("lakehouse"), Key: ptrS("raw/x.parquet")})
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(out.Body)
}

// scenario: function-key-cannot-sign-as-engine — Function lake's injected keypair cannot sign with the access
// key derived for CatalogService lake: the gateway re-derives the engine's secret from the kind the key carries,
// so the signature fails (403) and nothing is read. The engine's own keypair reads the same object.
func TestScenarioFunctionKeyCannotSignAsEngine(t *testing.T) {
	t.Parallel()
	g := newGateway(t, lakeMeta(), fixedPolicies{rev: "0"}, nil, memBucket)
	g.seed(t, "default", "lakehouse", "raw/x.parquet", []byte("engine data"))

	fnKey := s3gateway.DeriveKeypair(testMaster, v1.KindFunction, "default", "lake")
	engineKey := s3gateway.DeriveKeypair(testMaster, v1.KindCatalogService, "default", "lake")
	require.NotEqual(t, fnKey.SecretKey, engineKey.SecretKey)

	body, err := getRaw(t, g.clientWithKeys(t, engineKey.AccessKey, fnKey.SecretKey))
	require.Error(t, err, "the Function's secret does not sign as the engine")
	require.Equal(t, http.StatusForbidden, statusCode(err))
	require.Empty(t, body)

	body, err = getRaw(t, g.clientAs(t, v1.KindCatalogService, "default", "lake"))
	require.NoError(t, err)
	require.Equal(t, "engine data", string(body))
}

// preADRKeypair is the in-platform keypair as derived before the key carried the owner kind.
func preADRKeypair(master []byte, ns, name string) s3gateway.Keypair {
	access := "FUNCD" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(ns+"\x00"+name))
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("s3:" + ns + "/" + name))
	return s3gateway.Keypair{AccessKey: access, SecretKey: base64.StdEncoding.EncodeToString(mac.Sum(nil))}
}

// scenario: pre-adr-key-refused — a key in the two-part format no longer decodes, and with no ExternalKeys entry
// for it a request signed with it is 403 InvalidAccessKeyId.
func TestScenarioPreADRKeyRefused(t *testing.T) {
	t.Parallel()
	old := preADRKeypair(testMaster, "default", "lake")
	_, _, _, ok := s3gateway.DecodeAccess(old.AccessKey)
	require.False(t, ok, "a two-part body is not an in-platform key")

	g := newGateway(t, lakeMeta(), fixedPolicies{rev: "0"}, staticExternal{}, memBucket)
	g.seed(t, "default", "lakehouse", "raw/x.parquet", []byte("engine data"))
	_, err := getRaw(t, g.clientWithKeys(t, old.AccessKey, old.SecretKey))
	require.Equal(t, http.StatusForbidden, statusCode(err))
	var coded interface{ ErrorCode() string }
	require.True(t, errors.As(err, &coded), "an S3 API error: %v", err)
	require.Equal(t, "InvalidAccessKeyId", coded.ErrorCode())
}
