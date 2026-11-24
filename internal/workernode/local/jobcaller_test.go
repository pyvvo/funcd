package local_test

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// kindKV is the KV port: it records the caller of each read, by kind and name.
type kindKV struct {
	local.KV
	mu   sync.Mutex
	seen []v1.WorkloadRef
}

func (k *kindKV) GetEntry(_ context.Context, _ v1.NamespaceName, fn v1.WorkloadRef, _, _ string) (kvstore.Entry, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen = append(k.seen, fn)
	return kvstore.Entry{Value: []byte("v"), Version: "t-1"}, true, nil
}

func (k *kindKV) callers() []v1.WorkloadRef {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.seen)
}

func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jc") // not t.TempDir(): a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runRef(run v1.ObjectName) local.Ref {
	return local.Ref{Namespace: "team", Kind: v1.KindJob, Name: "tofu", Run: run}
}

// ADR-0351 Decision 2: every run of a Job has a local API socket of its own, apart from the Function of the Job's name,
// and each serves its own caller: the facets see the Job by kind and name, the dependency check the run itself. A
// run's socket removed leaves the others serving.
func TestEveryRunHasItsOwnSocket(t *testing.T) {
	t.Parallel()
	kv, deps := &kindKV{}, &fakeDeps{}
	m := local.NewManager(socketDir(t), fakeStore{}, fakeInvoker{}, nil, deps, slog.New(slog.DiscardHandler),
		local.WithFacets(local.KVFacet(kv)))
	t.Cleanup(m.Close)
	refs := []local.Ref{runRef("tofu-1"), runRef("tofu-2"), local.FunctionRef("team", "tofu")}
	socks := make([]string, len(refs))
	for i, ref := range refs {
		sock, err := m.SocketFor(ref)
		require.NoError(t, err)
		again, err := m.SocketFor(ref)
		require.NoError(t, err)
		require.Equal(t, sock, again, "a ref keeps its socket")
		socks[i] = sock
		c := unixClient(t, sock)
		require.Equal(t, http.StatusOK, callAs(t, c, http.MethodGet, "/health/dependencies"))
		require.Equal(t, http.StatusOK, callAs(t, c, http.MethodGet, "/kv/state/key"))
	}
	require.Len(t, slices.Compact(slices.Sorted(slices.Values(socks))), len(refs), "no two refs share a socket")
	require.Equal(t, refs, deps.seen(), "each socket checks its own caller")
	require.Equal(t, []v1.WorkloadRef{v1.JobOwner("tofu"), v1.JobOwner("tofu"), v1.FunctionOwner("tofu")}, kv.callers())

	m.Remove(runRef("tofu-1"))
	_, err := net.Dial("unix", socks[0])
	require.Error(t, err, "the removed run's socket is gone")
	require.Equal(t, http.StatusOK, callAs(t, unixClient(t, socks[1]), http.MethodGet, "/health/dependencies"),
		"the other run's still serves")
}

// ADR-0351 Decision 2: a ref of any other kind, the zero value included, gets no socket, and a handler built for it
// refuses every request before any port or check is called.
func TestARefOfAnotherKindIsRefused(t *testing.T) {
	t.Parallel()
	kv, deps := &kindKV{}, &fakeDeps{}
	m := local.NewManager(socketDir(t), fakeStore{}, fakeInvoker{}, nil, deps, slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)
	for _, ref := range []local.Ref{
		{},
		{Namespace: "team", Kind: v1.KindLake, Name: "tofu"},
		{Namespace: "team", Kind: v1.KindJobRun, Name: "tofu-1"},
		{Namespace: "team", Kind: v1.KindJob, Name: "tofu"},
		{Namespace: "team", Kind: v1.KindFunction, Name: "tofu", Run: "tofu-1"},
		{Kind: v1.KindFunction, Name: "tofu"},
	} {
		_, err := m.SocketFor(ref)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%#v: %v", ref, err)
		h := local.NewHandler(ref, &fakeResolver{}, fakeInvoker{}, nil, deps, slog.New(slog.DiscardHandler),
			local.WithFacets(local.KVFacet(kv)))
		for _, route := range [][2]string{{http.MethodGet, "/health/dependencies"}, {http.MethodGet, "/kv/state/key"},
			{http.MethodPost, "/invoke/x"}} {
			require.Equal(t, http.StatusForbidden, do(t, h, route[0], route[1], "{}").Code, "%#v: %s", ref, route[1])
		}
	}
	require.Empty(t, deps.seen())
	require.Empty(t, kv.callers())
}
