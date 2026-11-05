package dataplane_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const dialCause = "dial tcp 10.63.0.7:8080: connect: connection refused"

// failingFunctionReads fails each read of a Function.
type failingFunctionReads struct{ store.Store }

func (s failingFunctionReads) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindFunction {
		return nil, errors.New(dialCause)
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// failingEndpoints fails each upstream lookup.
type failingEndpoints struct{}

func (failingEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return "", false, errors.New(dialCause)
}

// refusingBefore refuses a call before its worker.
type refusingBefore struct{}

func (refusingBefore) Name() string              { return "refusing-before" }
func (refusingBefore) Applies(*invoke.Call) bool { return true }
func (refusingBefore) Before(context.Context, *invoke.Call) *invoke.Refusal {
	return &invoke.Refusal{Err: fault.Wrapf(errors.New(dialCause), fault.Unavailable, "contractcheck", "the contract cannot be read")}
}

// refusingAfter replaces the worker's answer.
type refusingAfter struct{}

func (refusingAfter) Name() string              { return "refusing-after" }
func (refusingAfter) Applies(*invoke.Call) bool { return true }
func (refusingAfter) After(context.Context, *invoke.Call, *invoke.Answer) *invoke.Refusal {
	return &invoke.Refusal{Err: fault.Wrapf(errors.New(dialCause), fault.Unavailable, "invoke.Answer.Body", "read the worker's answer")}
}

// failingContracts fails each contract lookup, as a materializer that cannot fetch the artifact does.
type failingContracts struct{}

func (failingContracts) ContractOf(context.Context, activator.FunctionRef, *v1.Function) (*contractcheck.Pair, error) {
	return nil, fault.NotFoundf("artifact.Pull", "fetch artifact x: %v", errors.New(dialCause))
}

// failingAttributes fails each stat of an object.
type failingAttributes struct{ blob.Bucket }

func (failingAttributes) Attributes(context.Context, string) (blob.Attributes, error) {
	return blob.Attributes{}, fault.Wrapf(errors.New(dialCause), fault.Internal, "blob.Attributes", "read the attributes")
}

// A 5xx's detail carries the operation and its message; the cause goes to the logger Handler is given, once.
func TestServerFaultCauseGoesToTheLog(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(up.Close)
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	stat, err := static.New(static.Deps{Buckets: func(v1.NamespaceName, string) (blob.Bucket, bool) { return failingAttributes{mem}, true }})
	require.NoError(t, err)
	site := router.New()
	require.NoError(t, site.Program(ctx, []router.Entry{{
		Namespace: "default",
		Auth:      v1.AuthOpen,
		Rules:     []router.CompiledRule{{Path: "/", Static: &v1.StaticBackend{Bucket: "site", Prefix: "web/", Index: "index.html"}}},
	}}, nil))

	for _, tc := range []struct {
		name      string
		reads     func(store.Store) store.Store
		endpoints activator.Endpoints
		rtr       router.Router
		opts      []dataplane.Option
		internal  bool
		method    string
		target    string
		status    int
		typ       string
		detail    string
	}{
		{
			name: "a failed read of the Function, by name", reads: func(s store.Store) store.Store { return failingFunctionReads{s} },
			endpoints: fakeEndpoints{upstream: up.URL}, target: "/function/f",
			status: http.StatusInternalServerError, typ: "urn:funcd:problem:internal", detail: "dataplane.ServeHTTP: function default/f",
		},
		{
			name: "a failed read of the Function, on an internal call", reads: func(s store.Store) store.Store { return failingFunctionReads{s} },
			endpoints: fakeEndpoints{upstream: up.URL}, internal: true, target: "/function/f",
			status: http.StatusInternalServerError, typ: "urn:funcd:problem:internal", detail: "dataplane.ServeHTTP: function default/f",
		},
		{
			name: "a failed upstream lookup", endpoints: failingEndpoints{}, target: "/function/f",
			status: http.StatusServiceUnavailable, typ: "urn:funcd:problem:unavailable", detail: "activator.Wake: resolve upstream for default/f",
		},
		{
			name: "a refusal before the worker", endpoints: fakeEndpoints{upstream: up.URL},
			opts: []dataplane.Option{dataplane.WithChain(invoke.NewChain(refusingBefore{}))}, target: "/function/f",
			status: http.StatusServiceUnavailable, typ: "urn:funcd:problem:unavailable", detail: "contractcheck: the contract cannot be read",
		},
		{
			name: "a refusal of the worker's answer", endpoints: fakeEndpoints{upstream: up.URL},
			opts: []dataplane.Option{dataplane.WithChain(invoke.NewChain(refusingAfter{}))}, target: "/function/f",
			status: http.StatusServiceUnavailable, typ: "urn:funcd:problem:unavailable", detail: "invoke.Answer.Body: read the worker's answer",
		},
		{
			name: "a contract that cannot be read", endpoints: fakeEndpoints{upstream: up.URL},
			opts: []dataplane.Option{dataplane.WithChain(invoke.NewChain(contractcheck.Plugins(failingContracts{})...))}, target: "/function/f",
			status: http.StatusServiceUnavailable, typ: "urn:funcd:problem:unavailable", detail: "contractcheck: the contract of default/f cannot be read",
		},
		{
			name: "a failed read of a static object", endpoints: fakeEndpoints{upstream: up.URL}, rtr: site, method: http.MethodGet, target: "/page.html",
			status: http.StatusInternalServerError, typ: "urn:funcd:problem:internal", detail: `edge.static: stat "web/page.html"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			seedFunction(t, st, "f")
			reads := st
			if tc.reads != nil {
				reads = tc.reads(st)
			}
			act, err := activator.New(activator.Deps{Store: st, Endpoints: tc.endpoints, Scaler: noScaler{}})
			require.NoError(t, err)
			var logs bytes.Buffer
			var stats *static.Handler
			if tc.rtr != nil {
				stats = stat
			}
			h := dataplane.Handler(reads, act, tc.rtr, "", nil, nil, stats, 0, slog.New(slog.NewTextHandler(&logs, nil)), tc.opts...)

			r := httptest.NewRequest(cmp.Or(tc.method, http.MethodPost), tc.target, strings.NewReader(`{}`))
			if tc.internal {
				r = r.WithContext(dataplane.WithInternal(r.Context()))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var p fault.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			require.Equal(t, tc.typ, p.Type)
			require.Equal(t, tc.detail, p.Detail)
			require.Equal(t, 1, strings.Count(logs.String(), dialCause), logs.String())
		})
	}
}

// A 4xx is the caller's to correct: it keeps its text, and nothing is left out for the log.
func TestCallerFaultLeavesNothingForTheLog(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	var logs bytes.Buffer
	h := dataplane.Handler(st, act, nil, "", nil, nil, nil, 0, slog.New(slog.NewTextHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/function/missing", http.NoBody))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "function default/missing is not exposed by name")
	require.Empty(t, logs.String())
}

// A body that stops arriving answers 400 with a fixed text, over a real connection.
func TestSlowBodyAnswersAFixedText(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(up.Close)
	h, st := newHandler(t, up.URL)
	seedFunction(t, st, "f")
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadHeaderTimeout = 10 * time.Second
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = io.WriteString(conn, "POST /function/f HTTP/1.1\r\nHost: any\r\nContent-Length: 100\r\n\r\nabc")
	require.NoError(t, err)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	var p fault.Problem
	require.NoError(t, json.Unmarshal(body, &p))
	require.Equal(t, "urn:funcd:problem:invalid", p.Type)
	require.Equal(t, "dataplane.ServeHTTP: could not read request body", p.Detail)
}
