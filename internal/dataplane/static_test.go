package dataplane_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// TestIssue108_StaticPathDecodedOnce: net/http has already percent-decoded r.URL.Path, so a static
// Route serves the object whose name is the URL decoded exactly once (ADR-0120 §2).
func TestIssue108_StaticPathDecodedOnce(t *testing.T) {
	t.Parallel()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	for k, v := range map[string]string{
		"web/100%.html":   "HUNDRED-PCT",
		"web/a%20b.txt":   "LITERAL-PCT20",
		"web/a b.txt":     "SPACE-FILE",
		"web/index.html":  "INDEX",
		"secret-file.txt": "TOP-SECRET",
	} {
		require.NoError(t, b.Put(context.Background(), k, []byte(v), blob.PutOptions{}))
	}
	sh, err := static.New(static.Deps{Buckets: onlyBucket(b, "default", "site")})
	require.NoError(t, err)

	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/", Static: &v1.StaticBackend{Bucket: "site", Prefix: "web/", Index: "index.html"}}},
	}}, nil))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, rtr, "", nil, nil, sh, 0, nil)

	for target, want := range map[string]string{
		"/100%25.html":  "HUNDRED-PCT",
		"/a%2520b.txt":  "LITERAL-PCT20",
		"/a%20b.txt":    "SPACE-FILE",
		"/a%20b%2etxt":  "SPACE-FILE",
		"/":             "INDEX",
		"/index%2Ehtml": "INDEX",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rec.Code, "GET %s: %s", target, rec.Body.String())
		require.Equal(t, want, rec.Body.String(), "GET %s served the wrong object", target)
	}

	for _, target := range []string{"/..%2fsecret-file.txt", "/%2e%2e/secret-file.txt", "/%252e%252e/secret-file.txt"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, "GET %s", target)
		require.NotContains(t, rec.Body.String(), "TOP-SECRET", "GET %s escaped the prefix", target)
	}
}

// onlyBucket resolves Bucket name of namespace ns to b, and any other to fault.NotFound.
func onlyBucket(b blob.Bucket, ns v1.NamespaceName, name string) static.BucketResolver {
	return func(gotNS v1.NamespaceName, got string) (blob.Bucket, error) {
		if gotNS == ns && got == name {
			return b, nil
		}
		return nil, fault.NotFoundf("test", "no bucket %s/%s", gotNS, got)
	}
}
