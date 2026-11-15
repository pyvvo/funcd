//go:build !e2e

package funcd_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// blobBodyCap is the local API's cap on a blob write's body, maxBlobBytes.
const blobBodyCap = 64 << 20

// blobCauseEnv runs a platform with opts whose default namespace holds the blob matrix's objects: Bucket s, Bucket full,
// which holds 64 bytes in all, Bucket unmeasured, whose usage the blob store may fail to measure, and Bucket capped,
// which takes an object of 16 bytes at most, each with a prefix p that owner owns; owner binds their prefixes as b, f, u
// and c, and binds gone to a Bucket that does not exist; reader and denied bind s's p as b, and a Policy forbids denied
// to read it.
func blobCauseEnv(t *testing.T, opts ...funcd.Option) *causeEnv {
	t.Helper()
	p := answersPlatform(t, opts...)
	c, base := answersClient(), "http://"+p.Addr()
	owner := function("owner")
	owner.Spec.Buckets = []v1.BucketEntry{{Ref: "s"}, {Ref: "full"}, {Ref: "unmeasured"}, {Ref: "capped"}, {Ref: "nope"}}
	owner.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "b", Bucket: "s", Prefix: "p"},
		{Alias: "f", Bucket: "full", Prefix: "p"}, {Alias: "u", Bucket: "unmeasured", Prefix: "p"},
		{Alias: "c", Bucket: "capped", Prefix: "p"}, {Alias: "gone", Bucket: "nope", Prefix: "p"}}
	create(t, c, base, owner)
	for _, name := range []v1.ObjectName{"reader", "denied"} {
		fn := function(name)
		fn.Spec.Buckets = []v1.BucketEntry{{Ref: "s"}}
		fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "b", Bucket: "s", Prefix: "p"}}
		create(t, c, base, fn)
	}
	full, unmeasured, capped := ownedBucket("full"), ownedBucket("unmeasured"), ownedBucket("capped")
	full.Spec.Quota.MaxBytes, unmeasured.Spec.Quota.MaxBytes, capped.Spec.Quota.MaxObjectBytes = 64, 1<<20, 16
	for _, b := range []*v1.Bucket{ownedBucket("s"), full, unmeasured, capped} {
		create(t, c, base, b)
	}
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-reads", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"s3::read", resource);`}})
	return &causeEnv{p: p}
}

// ownedBucket is a Bucket of default whose prefix p the Function owner owns.
func ownedBucket(name v1.ObjectName) *v1.Bucket {
	return &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "p", Owner: v1.FunctionOwner("owner")}}}}
}

// unmeasurableBlob is a blob store whose listing of the Bucket unmeasured fails, so its quota cannot be measured.
type unmeasurableBlob struct{ blob.Bucket }

func (u unmeasurableBlob) ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	if strings.HasPrefix(prefix, "s3/default/unmeasured/") {
		return nil, false, errors.New("the disk does not answer")
	}
	return u.Bucket.ListAfter(ctx, prefix, after, limit)
}

// openBlob opens the blob store at url, which the platform it is given to closes.
func openBlob(t *testing.T, url string) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), url)
	require.NoError(t, err)
	return b
}

// TestBlobCauseMatrix is the blob service's matrix: each cause that a blob endpoint declares, provoked by one request.
func TestBlobCauseMatrix(t *testing.T) {
	t.Parallel()
	envs := blobEnvs{
		main:     blobCauseEnv(t, funcd.WithBlob(unmeasurableBlob{openBlob(t, "mem://")})),
		file:     blobCauseEnv(t, funcd.WithBlob(openBlob(t, gocloud.FileURL(t.TempDir())))),
		floor:    blobCauseEnv(t, funcd.WithFreeSpaceFloor(func() (int64, error) { return 0, nil }, 1)),
		oversize: strings.Repeat("v", blobBodyCap+1),
	}
	runCauseMatrix(t, blobCauseRows(envs))
}

// blobEnvs are the platforms of the blob matrix: main, file, whose blob store is funcd's file store, and floor, whose
// data filesystem is below its free-space floor; and oversize, a body over the cap, which only a run of the matrix
// builds.
type blobEnvs struct {
	main, file, floor *causeEnv
	oversize          string
}

// blobCauseRows is the blob matrix over envs.
func blobCauseRows(envs blobEnvs) []causeRow {
	const (
		key, put, del, list = "GET /blob/{binding}/{key...}", "PUT /blob/{binding}/{key...}",
			"DELETE /blob/{binding}/{key...}", "GET /blob/{binding}"
	)
	get := func(path string) causeRequest { return causeRequest{method: http.MethodGet, path: path} }
	write := func(path, body string) causeRequest {
		return causeRequest{method: http.MethodPut, path: path, body: body}
	}
	remove := func(path string) causeRequest { return causeRequest{method: http.MethodDelete, path: path} }
	main, file, floor := envs.main, envs.file, envs.floor
	rows := []causeRow{
		{endpoint: key, cause: blob.ErrReadDenied, env: main, caller: "denied", request: get("/blob/b/k")},
		{endpoint: key, cause: blob.ErrWriteDenied, env: main, caller: "reader", request: get("/blob/b/k?sign=1&method=PUT")},
		{endpoint: key, cause: blob.ErrBucketMissing, env: main, caller: "owner", request: get("/blob/gone/k")},
		{endpoint: key, cause: blob.ErrObjectNotFound, env: main, caller: "owner", request: get("/blob/b/never-written")},
		{endpoint: key, cause: local.ErrSignMethodInvalid, env: main, caller: "owner",
			request: get("/blob/b/k?sign=1&method=POST")},
		{endpoint: key, cause: local.ErrSignExpiryInvalid, env: main, caller: "owner", request: get("/blob/b/k?sign=1&expiry=10")},
		{endpoint: key, cause: blob.ErrSignPutCapped, env: main, caller: "owner", request: get("/blob/c/k?sign=1&method=PUT")},
		{endpoint: key, cause: blob.ErrSignUnsupported, env: main, caller: "owner", request: get("/blob/b/k?sign=1")},
		{endpoint: key, cause: blob.ErrKeyNotUTF8, env: main, caller: "owner", request: get("/blob/b/%FF")},
		{endpoint: key, cause: blob.ErrKeyReserved, env: file, caller: "owner", request: get("/blob/b/k.attrs")},
		{endpoint: key, cause: blob.ErrKeyUnstorable, env: file, caller: "owner",
			before: []causeRequest{write("/blob/b/read", "v")}, request: get("/blob/b/read/k")},

		{endpoint: put, cause: local.ErrBodyTooLarge, env: main, caller: "owner", request: write("/blob/b/k", envs.oversize)},
		{endpoint: put, cause: local.ErrBodyUnreadable, env: main, caller: "owner", request: causeRequest{
			raw: "PUT /blob/b/k HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}},
		{endpoint: put, cause: blob.ErrWriteDenied, env: main, caller: "reader", request: write("/blob/b/k", "v")},
		{endpoint: put, cause: blob.ErrObjectOverCap, env: main, caller: "owner", request: write("/blob/c/k", strings.Repeat("v", 17))},
		{endpoint: put, cause: quota.ErrFloorReached, env: floor, caller: "owner", request: write("/blob/b/k", "v")},
		{endpoint: put, cause: quota.ErrExceeded, env: main, caller: "owner", request: write("/blob/f/k", strings.Repeat("v", 65))},
		{endpoint: put, cause: quota.ErrUnreadable, env: main, caller: "owner", request: write("/blob/u/k", "v")},
		{endpoint: put, cause: blob.ErrKeyNotUTF8, env: main, caller: "owner", request: write("/blob/b/%FF", "v")},
		{endpoint: put, cause: blob.ErrKeyReserved, env: file, caller: "owner", request: write("/blob/b/k.attrs", "v")},
		{endpoint: put, cause: blob.ErrKeyUnstorable, env: file, caller: "owner",
			before: []causeRequest{write("/blob/b/write", "v")}, request: write("/blob/b/write/k", "v")},

		{endpoint: del, cause: blob.ErrWriteDenied, env: main, caller: "reader", request: remove("/blob/b/k")},
		{endpoint: del, cause: blob.ErrObjectNotFound, env: main, caller: "owner", request: remove("/blob/b/never-written")},
		{endpoint: del, cause: blob.ErrKeyNotUTF8, env: main, caller: "owner", request: remove("/blob/b/%FF")},
		{endpoint: del, cause: blob.ErrKeyReserved, env: file, caller: "owner", request: remove("/blob/b/k.attrs")},
		{endpoint: del, cause: blob.ErrKeyUnstorable, env: file, caller: "owner",
			before: []causeRequest{write("/blob/b/remove", "v")}, request: remove("/blob/b/remove/k")},

		{endpoint: list, cause: blob.ErrReadDenied, env: main, caller: "denied", request: get("/blob/b")},
		{endpoint: list, cause: blob.ErrBucketMissing, env: main, caller: "owner", request: get("/blob/gone")},
	}
	for _, e := range []struct {
		endpoint string
		request  func(binding string) causeRequest
	}{
		{key, func(b string) causeRequest { return get("/blob/" + b + "/k") }},
		{put, func(b string) causeRequest { return write("/blob/"+b+"/k", "v") }},
		{del, func(b string) causeRequest { return remove("/blob/" + b + "/k") }},
		{list, func(b string) causeRequest { return get("/blob/" + b) }},
	} {
		rows = append(rows,
			causeRow{endpoint: e.endpoint, cause: blob.ErrCallerMissing, env: main, caller: "ghost", request: e.request("b")},
			causeRow{endpoint: e.endpoint, cause: blob.ErrNoBinding, env: main, caller: "owner", request: e.request("unbound")})
	}
	return rows
}
