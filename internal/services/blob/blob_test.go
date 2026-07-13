package blob_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	iblob "github.com/green-0-rabbit/funcd/internal/blob"
	svcblob "github.com/green-0-rabbit/funcd/internal/services/blob"
)

// mapBucket is a tiny in-memory blob.Bucket for the facade tests. Unlike memblob it supports SignedURL,
// and its map is directly inspectable so a test can assert the SUBSTRATE key (the coexistence property).
type mapBucket struct{ m map[string][]byte }

func newMapBucket() *mapBucket { return &mapBucket{m: map[string][]byte{}} }

func (b *mapBucket) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := b.m[key]
	if !ok {
		return nil, fault.NotFoundf("mapBucket.Get", "key %q", key)
	}
	return v, nil
}
func (b *mapBucket) Put(_ context.Context, key string, data []byte) error {
	b.m[key] = data
	return nil
}
func (b *mapBucket) Delete(_ context.Context, key string) error { delete(b.m, key); return nil }
func (b *mapBucket) Exists(_ context.Context, key string) (bool, error) {
	_, ok := b.m[key]
	return ok, nil
}
func (b *mapBucket) List(_ context.Context, prefix string) ([]iblob.Attributes, error) {
	var out []iblob.Attributes
	for k, v := range b.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, iblob.Attributes{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (b *mapBucket) SignedURL(_ context.Context, key string, opts iblob.SignOptions) (string, error) {
	return "https://signed.example/" + key + "?method=" + string(opts.Method), nil
}
func (b *mapBucket) Close() error { return nil }

// fakeResolver binds caller "fn" via alias "files" → bucket "bkt", prefix "p"; everything else is
// default-deny (Forbidden), mirroring the ADR-0073 bind-as-grant BindingResolver.
type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (svcblob.Binding, error) {
	if fn == "fn" && alias == "files" {
		return svcblob.Binding{Bucket: "bkt", Prefix: "p"}, nil
	}
	return svcblob.Binding{}, fault.Forbiddenf("fakeResolver", "no blob binding for %s/%s", fn, alias)
}

// s3PDP is a test PDP: s3::read allowed iff readOK, s3::write iff writeOK (ADR-0080 S3Capability stand-in).
type s3PDP struct{ readOK, writeOK bool }

func (p s3PDP) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	switch req.Action {
	case auth.ActionS3Read:
		return auth.Decision{Allowed: p.readOK}, nil
	case auth.ActionS3Write:
		return auth.Decision{Allowed: p.writeOK}, nil
	default:
		return auth.Decision{Allowed: false}, nil
	}
}

func newFacade(t *testing.T, bkt iblob.Bucket, pdp auth.Authorizer) *svcblob.Facade {
	t.Helper()
	f, err := svcblob.NewFacade(svcblob.FacadeDeps{
		Resolver: fakeResolver{},
		BucketFor: func(_ v1.NamespaceName, name string) (iblob.Bucket, bool) {
			if name == "bkt" {
				return bkt, true
			}
			return nil, false
		},
		Authorizer: pdp,
	})
	require.NoError(t, err)
	return f
}

// scenario: blob-read-write — a bound function put/gets its prefix; the object is keyed at the SHARED
// substrate keyspace (prefix/key), so it is the same object the ADR-0080 S3 frontend serves.
func TestScenarioBlobReadWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bkt := newMapBucket()
	f := newFacade(t, bkt, s3PDP{readOK: true, writeOK: true})

	require.NoError(t, f.Put(ctx, "default", "fn", "files", "report.txt", []byte("hello")))
	require.Equal(t, []byte("hello"), bkt.m["p/report.txt"], "keyed at the shared s3gateway keyspace prefix/key")

	got, found, err := f.Get(ctx, "default", "fn", "files", "report.txt")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("hello"), got)

	_, found, err = f.Get(ctx, "default", "fn", "files", "absent")
	require.NoError(t, err)
	require.False(t, found, "a missing object is found=false, not an error")

	require.NoError(t, f.Delete(ctx, "default", "fn", "files", "report.txt"))
	_, found, err = f.Get(ctx, "default", "fn", "files", "report.txt")
	require.NoError(t, err)
	require.False(t, found)
}

// scenario: blob-list — list returns exactly the binding's keys under the prefix, the prefix sub-domain
// stripped so the caller sees only its own key space.
func TestScenarioBlobList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bkt := newMapBucket()
	f := newFacade(t, bkt, s3PDP{readOK: true, writeOK: true})

	for _, k := range []string{"bronze/a", "bronze/b", "silver/x"} {
		require.NoError(t, f.Put(ctx, "default", "fn", "files", k, []byte("v")))
	}
	require.Equal(t, []string{"p/bronze/a", "p/bronze/b", "p/silver/x"}, sortedKeys(bkt), "substrate keys share the prefix")

	keys, err := f.List(ctx, "default", "fn", "files", "bronze/")
	require.NoError(t, err)
	require.Equal(t, []string{"bronze/a", "bronze/b"}, keys)
}

// scenario: blob-unbound-forbidden — an alias the function did not declare is default-deny (Forbidden),
// and a bound alias with no permitting Policy is also denied.
func TestScenarioBlobUnboundForbidden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	f := newFacade(t, newMapBucket(), s3PDP{readOK: true, writeOK: true})
	_, _, err := f.Get(ctx, "default", "fn", "nope", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "an unbound alias is Forbidden (resolver default-deny)")

	fDeny := newFacade(t, newMapBucket(), s3PDP{readOK: false, writeOK: false})
	_, _, err = fDeny.Get(ctx, "default", "fn", "files", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a bound alias with no permitting Policy is Forbidden (PDP deny)")
}

// scenario: blob-signed-url — signedUrl returns a substrate presigned URL; a GET-sign needs s3::read, a
// PUT-sign needs s3::write (no read→write escalation).
func TestScenarioBlobSignedURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t, newMapBucket(), s3PDP{readOK: true, writeOK: false})

	url, err := f.SignedURL(ctx, "default", "fn", "files", "report.txt", iblob.SignOptions{})
	require.NoError(t, err)
	require.Contains(t, url, "p/report.txt", "keyed at the shared keyspace")

	_, err = f.SignedURL(ctx, "default", "fn", "files", "report.txt", iblob.SignOptions{Method: iblob.SignPut})
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a PUT-sign needs s3::write — no read→write escalation")
}

// TestBlobTypeHandler covers the unchanged Service-dispatcher TypeHandler (ADR-0021).
func TestBlobTypeHandler(t *testing.T) {
	t.Parallel()
	h := svcblob.NewHandler()
	require.Equal(t, v1.ServiceTypeBlob, h.Type())

	svc := &v1.Service{}
	svc.Spec.Blob = &v1.BlobServiceSpec{Binding: "files"}
	_, err := h.Reconcile(context.Background(), svc)
	require.NoError(t, err)

	_, err = h.Reconcile(context.Background(), &v1.Service{})
	require.Error(t, err, "a blob service without a binding is invalid")
}

func sortedKeys(b *mapBucket) []string {
	out := make([]string, 0, len(b.m))
	for k := range b.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
