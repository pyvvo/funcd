//go:build !e2e

package funcd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// Each service's known causes, proven by one parametrized test over its own matrix (ADR-0330 Decision 7): one row per
// endpoint and cause, each with the platform it runs on, its caller and its request, and the requests that set it up.
// A row passes when TestMain's recorder sees the endpoint answer the cause and the answer has its kind's status and
// problem type. A cause that only a raw request provokes is a row whose request goes to the socket as it is.

// causeRow is one known cause of one endpoint of the local API.
type causeRow struct {
	endpoint string
	cause    *fault.KnownCause
	env      *causeEnv
	caller   v1.ObjectName
	before   []causeRequest // requests that set the row up, each answered 2xx
	request  causeRequest
}

// causeRequest is one request to a caller's local API: method, path, header lines and body, or raw, sent as it is.
type causeRequest struct {
	method, path string
	header       []string // name, value pairs, each value on a line of its own
	body         string
	raw          string
}

// causeEnv is a running platform that a matrix's rows call.
type causeEnv struct {
	p *funcd.Platform
}

// runCauseMatrix runs each row as a subtest of its own, in parallel.
func runCauseMatrix(t *testing.T, rows []causeRow) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.endpoint+" "+row.cause.Name(), func(t *testing.T) {
			t.Parallel()
			for _, req := range row.before {
				status, _, body := row.env.send(t, row.caller, req)
				require.True(t, status >= 200 && status < 300, "the setup %s %s: %d %s", req.method, req.path, status, body)
			}
			before := localGot.count(row.endpoint, row.cause)
			status, header, body := row.env.send(t, row.caller, row.request)
			want := fault.ToProblem(row.cause.Errorf("", ""))
			require.Equal(t, want.Status, status, "%s", body)
			require.Equal(t, "application/problem+json", header.Get("Content-Type"), "%s", body)
			var problem fault.Problem
			require.NoError(t, json.Unmarshal([]byte(body), &problem), body)
			require.Equal(t, want.Type, problem.Type, body)
			require.Greater(t, localGot.count(row.endpoint, row.cause), before, "%s answered %s: %s", row.endpoint,
				row.cause.Name(), body)
			if row.cause == local.ErrVersionMismatch {
				require.NotEmpty(t, header.Get("ETag"), "the 412 keeps the ETag that the endpoint set before its error")
			}
		})
	}
}

// send makes req on the local API of caller and returns the status, the headers and the body of the answer.
func (e *causeEnv) send(t *testing.T, caller v1.ObjectName, req causeRequest) (int, http.Header, string) {
	t.Helper()
	sock, err := funcd.LocalAPISocketForTest(e.p, "default", caller)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var resp *http.Response
	if req.raw != "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		_, err = io.WriteString(conn, req.raw)
		require.NoError(t, err)
		resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
	} else {
		client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
		defer client.CloseIdleConnections()
		r, err := http.NewRequestWithContext(ctx, req.method, "http://local"+req.path, strings.NewReader(req.body))
		require.NoError(t, err)
		for i := 0; i+1 < len(req.header); i += 2 {
			r.Header.Add(req.header[i], req.header[i+1])
		}
		resp, err = client.Do(r)
		require.NoError(t, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(body)
}

// kvCauseEnv runs a platform with opts whose default namespace holds the KV matrix's objects: store s, whose table t
// takes a value of 16 bytes and a key of 16 at most, store full, which holds 64 bytes in all, and store unmeasured,
// whose usage the KV driver cannot measure, each table owned by owner; owner binds them as b, f and u, and binds gone
// to a store that does not exist and notable to a table that s does not declare; reader and denied bind s's t as b,
// and a Policy forbids denied to read it.
func kvCauseEnv(t *testing.T, opts ...funcd.Option) *causeEnv {
	t.Helper()
	p := answersPlatform(t, opts...)
	c, base := answersClient(), "http://"+p.Addr()
	owner := function("owner")
	owner.Spec.KVStores = []v1.KVStoreEntry{{Ref: "s"}, {Ref: "full"}, {Ref: "unmeasured"}, {Ref: "nope"}}
	owner.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "b", Store: "s", Table: "t"}, {Alias: "f", Store: "full", Table: "t"},
		{Alias: "u", Store: "unmeasured", Table: "t"}, {Alias: "gone", Store: "nope", Table: "t"},
		{Alias: "notable", Store: "s", Table: "t9"}}
	create(t, c, base, owner)
	for _, name := range []v1.ObjectName{"reader", "denied"} {
		fn := function(name)
		fn.Spec.KVStores = []v1.KVStoreEntry{{Ref: "s"}}
		fn.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "b", Store: "s", Table: "t"}}
		create(t, c, base, fn)
	}
	s := ownedStore("s")
	s.Spec.Quota.MaxValueBytes, s.Spec.Quota.MaxKeyBytes = 16, 16
	full := ownedStore("full")
	full.Spec.Quota.MaxBytes = 64
	unmeasured := ownedStore("unmeasured")
	unmeasured.Spec.Quota.MaxBytes = 1 << 20
	for _, ks := range []*v1.KVStore{s, full, unmeasured} {
		create(t, c, base, ks)
	}
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-reads", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"kv::read", resource);`}})
	return &causeEnv{p: p}
}

// ownedStore is a KVStore of default whose table t the Function owner owns.
func ownedStore(name v1.ObjectName) *v1.KVStore {
	ks := kvStore(name)
	ks.Spec.Tables = []v1.KVTable{{Name: "t", Owner: "owner"}}
	return ks
}

// unmeasurableKV is the in-memory KV driver whose usage of a store named unmeasured cannot be read.
type unmeasurableKV struct {
	kvstore.KV
	kvstore.Conditional
	usage kvstore.Usager
}

func newUnmeasurableKV() unmeasurableKV {
	kv := kvmemory.New()
	return unmeasurableKV{KV: kv, Conditional: kv.(kvstore.Conditional), usage: kv.(kvstore.Usager)}
}

func (u unmeasurableKV) Usage(ctx context.Context, prefix string) (int64, error) {
	if strings.HasSuffix(prefix, "/unmeasured/") {
		return 0, errors.New("the disk does not answer")
	}
	return u.usage.Usage(ctx, prefix)
}

// TestKVCauseMatrix is the KV service's matrix: each cause that a KV endpoint declares, provoked by one request.
func TestKVCauseMatrix(t *testing.T) {
	t.Parallel()
	envs := kvEnvs{
		main:  kvCauseEnv(t, funcd.WithKVStore(newUnmeasurableKV())),
		plain: &causeEnv{p: answersPlatform(t, funcd.WithKVStore(unreclaimedKV{kvmemory.New()}))},
		floor: kvCauseEnv(t, funcd.WithFreeSpaceFloor(func() (int64, error) { return 0, nil }, 1)),
	}
	runCauseMatrix(t, kvCauseRows(envs))
}

// kvEnvs are the platforms of the KV matrix: main, plain, whose KV driver offers no versioned reads or conditional
// writes, and floor, whose data filesystem is below its free-space floor.
type kvEnvs struct {
	main, plain, floor *causeEnv
}

// kvCauseRows is the KV matrix over envs.
func kvCauseRows(envs kvEnvs) []causeRow {
	const (
		key, put, del, list = "GET /kv/{binding}/{key...}", "PUT /kv/{binding}/{key...}", "DELETE /kv/{binding}/{key...}",
			"GET /kv/{binding}"
	)
	long := strings.Repeat("k", v1.MaxKeyBytesLimit+1)
	get := func(path string) causeRequest { return causeRequest{method: http.MethodGet, path: path} }
	write := func(path, body string, header ...string) causeRequest {
		return causeRequest{method: http.MethodPut, path: path, body: body, header: header}
	}
	remove := func(path string, header ...string) causeRequest {
		return causeRequest{method: http.MethodDelete, path: path, header: header}
	}
	main, plain, floor := envs.main, envs.plain, envs.floor
	rows := []causeRow{
		{endpoint: key, cause: kvstore.ErrNoConditional, env: plain, caller: "owner", request: get("/kv/b/k")},
		{endpoint: key, cause: kvstore.ErrKeyOverLimit, env: main, caller: "owner", request: get("/kv/b/" + long)},
		{endpoint: key, cause: local.ErrKeyNotFound, env: main, caller: "owner", request: get("/kv/b/never-written")},
		{endpoint: key, cause: kvstore.ErrReadDenied, env: main, caller: "denied", request: get("/kv/b/k")},

		{endpoint: put, cause: kvstore.ErrWriteDenied, env: main, caller: "reader", request: write("/kv/b/k", "v")},
		{endpoint: put, cause: kvstore.ErrNoConditional, env: plain, caller: "owner",
			request: write("/kv/b/k", "v", "If-None-Match", "*")},
		{endpoint: put, cause: local.ErrIfMatchRepeated, env: main, caller: "owner",
			request: write("/kv/b/k", "v", "If-Match", `"a"`, "If-Match", `"b"`)},
		{endpoint: put, cause: local.ErrIfMatchMalformed, env: main, caller: "owner",
			request: write("/kv/b/k", "v", "If-Match", "unquoted")},
		{endpoint: put, cause: local.ErrKeyAbsent, env: main, caller: "owner",
			request: write("/kv/b/never-written", "v", "If-Match", `"0123"`)},
		{endpoint: put, cause: local.ErrVersionMismatch, env: main, caller: "owner",
			before: []causeRequest{write("/kv/b/created", "v")}, request: write("/kv/b/created", "w", "If-None-Match", "*")},
		{endpoint: put, cause: local.ErrIfNoneMatchRepeated, env: main, caller: "owner",
			request: write("/kv/b/k", "v", "If-None-Match", "*", "If-None-Match", "*")},
		{endpoint: put, cause: local.ErrIfNoneMatchNotStar, env: main, caller: "owner",
			request: write("/kv/b/k", "v", "If-None-Match", `"a"`)},
		{endpoint: put, cause: local.ErrConditionsBoth, env: main, caller: "owner",
			request: write("/kv/b/k", "v", "If-None-Match", "*", "If-Match", `"a"`)},
		{endpoint: put, cause: local.ErrTTLInvalid, env: main, caller: "owner", request: write("/kv/b/k?ttl=0", "v")},
		{endpoint: put, cause: local.ErrBodyTooLarge, env: main, caller: "owner",
			request: write("/kv/b/k", strings.Repeat("v", int(v1.MaxValueBytesLimit)+1))},
		{endpoint: put, cause: local.ErrBodyUnreadable, env: main, caller: "owner", request: causeRequest{
			raw: "PUT /kv/b/k HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}},
		{endpoint: put, cause: kvstore.ErrValueOverCap, env: main, caller: "owner",
			request: write("/kv/b/k", strings.Repeat("v", 17))},
		{endpoint: put, cause: kvstore.ErrKeyOverCap, env: main, caller: "owner",
			request: write("/kv/b/"+strings.Repeat("k", 17), "v")},
		{endpoint: put, cause: quota.ErrFloorReached, env: floor, caller: "owner", request: write("/kv/b/k", "v")},
		{endpoint: put, cause: quota.ErrExceeded, env: main, caller: "owner", request: write("/kv/f/k", strings.Repeat("v", 60))},
		{endpoint: put, cause: quota.ErrUnreadable, env: main, caller: "owner", request: write("/kv/u/k", "v")},

		{endpoint: del, cause: kvstore.ErrWriteDenied, env: main, caller: "reader", request: remove("/kv/b/k")},
		{endpoint: del, cause: kvstore.ErrNoConditional, env: plain, caller: "owner", request: remove("/kv/b/k", "If-Match", `"a"`)},
		{endpoint: del, cause: local.ErrIfMatchRepeated, env: main, caller: "owner",
			request: remove("/kv/b/k", "If-Match", `"a"`, "If-Match", `"b"`)},
		{endpoint: del, cause: local.ErrIfMatchMalformed, env: main, caller: "owner", request: remove("/kv/b/k", "If-Match", `""`)},
		{endpoint: del, cause: local.ErrKeyAbsent, env: main, caller: "owner",
			request: remove("/kv/b/never-written", "If-Match", `"0123"`)},
		{endpoint: del, cause: local.ErrVersionMismatch, env: main, caller: "owner",
			before: []causeRequest{write("/kv/b/kept", "v")}, request: remove("/kv/b/kept", "If-Match", `"0123"`)},
		{endpoint: del, cause: local.ErrDeleteIfNoneMatch, env: main, caller: "owner",
			request: remove("/kv/b/k", "If-None-Match", "*")},
		{endpoint: del, cause: kvstore.ErrKeyOverLimit, env: main, caller: "owner", request: remove("/kv/b/" + long)},

		{endpoint: list, cause: kvstore.ErrReadDenied, env: main, caller: "denied", request: get("/kv/b")},
	}
	for _, e := range []struct {
		endpoint string
		request  func(binding string) causeRequest
	}{
		{key, func(b string) causeRequest { return get("/kv/" + b + "/k") }},
		{put, func(b string) causeRequest { return write("/kv/"+b+"/k", "v") }},
		{del, func(b string) causeRequest { return remove("/kv/" + b + "/k") }},
		{list, func(b string) causeRequest { return get("/kv/" + b) }},
	} {
		rows = append(rows,
			causeRow{endpoint: e.endpoint, cause: kvstore.ErrCallerMissing, env: main, caller: "ghost", request: e.request("b")},
			causeRow{endpoint: e.endpoint, cause: kvstore.ErrNoBinding, env: main, caller: "owner", request: e.request("unbound")},
			causeRow{endpoint: e.endpoint, cause: kvstore.ErrStoreMissing, env: main, caller: "owner", request: e.request("gone")},
			causeRow{endpoint: e.endpoint, cause: kvstore.ErrTableMissing, env: main, caller: "owner", request: e.request("notable")})
	}
	return rows
}

// causeMatrices are the matrices of the described services, by the first segment of their endpoints' paths.
func causeMatrices() map[string][]causeRow {
	return map[string][]causeRow{"kv": kvCauseRows(kvEnvs{})}
}

// TestEveryDeclaredCauseHasARow holds each described endpoint's declared causes to the matrix of its service: one row
// for each, and no row for a cause that its endpoint does not declare; a missing row fails, naming its endpoint and
// its cause.
func TestEveryDeclaredCauseHasARow(t *testing.T) {
	endpoints := local.Endpoints()
	require.NoError(t, checkCauseRows(endpoints, causeMatrices()))
	rows := kvCauseRows(kvEnvs{})
	require.EqualError(t, checkCauseRows(endpoints, map[string][]causeRow{"kv": rows[1:]}),
		rows[0].endpoint+" declares "+rows[0].cause.Name()+", which no row of the kv matrix proves")
	require.EqualError(t, checkCauseRows(endpoints, map[string][]causeRow{"kv": append(rows, rows[0])}),
		rows[0].endpoint+" has 2 rows for "+rows[0].cause.Name()+" in the kv matrix")
	stray := causeRow{endpoint: "GET /kv/{binding}", cause: local.ErrKeyNotFound}
	require.EqualError(t, checkCauseRows(endpoints, map[string][]causeRow{"kv": append(rows, stray)}),
		"the kv matrix has a row for GET /kv/{binding} and local.key-not-found, which the endpoint does not declare")
	require.EqualError(t, checkCauseRows(endpoints, nil), "the kv service declares causes and has no matrix")
}

// checkCauseRows fails on a declared cause with no row in its service's matrix or with two, on a row whose endpoint
// does not declare its cause, and on a described service with causes and no matrix.
func checkCauseRows(endpoints []local.Endpoint, matrices map[string][]causeRow) error {
	var errs []error
	declared := map[string][]*fault.KnownCause{}
	for _, e := range endpoints {
		declared[e.Pattern] = e.Causes
		if len(e.Causes) == 0 {
			continue
		}
		service := serviceOf(e.Pattern)
		rows, ok := matrices[service]
		if !ok {
			if !slices.ContainsFunc(errs, func(err error) bool { return strings.Contains(err.Error(), "the "+service+" service") }) {
				errs = append(errs, fmt.Errorf("the %s service declares causes and has no matrix", service))
			}
			continue
		}
		for _, c := range e.Causes {
			switch n := countRows(rows, e.Pattern, c); n {
			case 0:
				errs = append(errs, fmt.Errorf("%s declares %s, which no row of the %s matrix proves", e.Pattern, c.Name(), service))
			case 1:
			default:
				errs = append(errs, fmt.Errorf("%s has %d rows for %s in the %s matrix", e.Pattern, n, c.Name(), service))
			}
		}
	}
	for _, service := range slices.Sorted(maps.Keys(matrices)) {
		for _, row := range matrices[service] {
			if !slices.Contains(declared[row.endpoint], row.cause) {
				errs = append(errs, fmt.Errorf("the %s matrix has a row for %s and %s, which the endpoint does not declare",
					service, row.endpoint, row.cause.Name()))
			}
		}
	}
	return errors.Join(errs...)
}

func countRows(rows []causeRow, pattern string, cause *fault.KnownCause) int {
	n := 0
	for _, r := range rows {
		if r.endpoint == pattern && r.cause == cause {
			n++
		}
	}
	return n
}

// serviceOf is the first segment of a pattern's path: "kv" for "GET /kv/{binding}".
func serviceOf(pattern string) string {
	_, path, _ := strings.Cut(pattern, " ")
	if path == "" {
		path = pattern
	}
	segment, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return segment
}
