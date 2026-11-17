package funcd_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const clusterAdmin = "cluster-admin-token"

func runPlatform(t *testing.T, opts ...funcd.Option) *funcd.Platform {
	t.Helper()
	p, err := funcd.New(append([]funcd.Option{funcd.InMemory(),
		funcd.WithCredentials(funcd.Credential{Token: clusterAdmin, Role: "admin"})}, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(sync.OnceFunc(func() { cancel(); <-done }))
	return p
}

func tmeta(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

// TestAWorkerNodeRunsTheNamespacePlacedOnIt joins a worker node to a control plane, both built from the platform's own
// options, and checks the node runs what the control plane holds and reports it back (FEAT-0002 F241 to F246, F250).
func TestAWorkerNodeRunsTheNamespacePlacedOnIt(t *testing.T) {
	ctx := context.Background()
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalRuns: true}))
	admin, err := sdk.New("http://"+cp.Addr(), sdk.WithToken(clusterAdmin))
	require.NoError(t, err)

	wn := &v1.WorkerNode{TypeMeta: tmeta(v1.KindWorkerNode)}
	wn.Name = "vm1"
	_, err = admin.Apply(ctx, wn)
	require.NoError(t, err)
	ns := &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = admin.Apply(ctx, ns)
	require.NoError(t, err)
	kv := &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = "kv1", "e1", "rg1"
	_, err = admin.Apply(ctx, kv)
	require.NoError(t, err)

	phase := func(c *sdk.Client) v1.Phase {
		obj, err := c.Get(ctx, v1.KindKVStore, "e1", "kv1")
		if err != nil {
			return ""
		}
		return obj.(*v1.KVStore).Status.Phase
	}
	time.Sleep(time.Second)
	require.Empty(t, phase(admin), "the control plane leaves a namespace placed on a node to that node")

	tok, err := admin.MintJoinToken(ctx, "vm1", "")
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, "https://"+cp.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
	require.NoError(t, err)
	node := runPlatform(t, funcd.WithNodeAgent(funcd.NodeAgentOptions{Dir: dir, Version: "test"}))
	local, err := sdk.New("http://"+node.Addr(), sdk.WithToken(clusterAdmin))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return phase(local) == v1.PhaseReady }, 15*time.Second, 50*time.Millisecond,
		"the node runs the KVStore")
	require.Eventually(t, func() bool { return phase(admin) == v1.PhaseReady }, 15*time.Second, 50*time.Millisecond,
		"its status comes back to the control plane")
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindWorkerNode, "", "vm1")
		return err == nil && obj.(*v1.WorkerNode).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 100*time.Millisecond, "the WorkerNode reads Ready")

	_, err = admin.Logs(ctx, "e1", "absent", sdk.LogsOptions{})
	require.NoError(t, err, "a log read of the node's namespace goes through the node")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+node.Addr()+"/apis/funcd.io/v1alpha1/namespaces/e1/configmaps",
		strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+clusterAdmin)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "a worker node's own API answers reads only")

	cur, err := admin.Get(ctx, v1.KindWorkerNode, "", "vm1")
	require.NoError(t, err)
	require.Error(t, admin.Delete(ctx, v1.KindWorkerNode, "", "vm1", sdk.IfVersion(cur.GetObjectMeta().ResourceVersion)),
		"a WorkerNode a namespace names cannot be deleted")
}

// TestMintingAJoinTokenAnswersEachCase provokes each answer the join-token route lists (ADR-0329 Decision 4).
func TestMintingAJoinTokenAnswersEachCase(t *testing.T) {
	const dev = "cluster-dev-token"
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		funcd.WithCredentials(funcd.Credential{Token: clusterAdmin, Role: "admin"},
			funcd.Credential{Token: dev, Role: "developer", Namespaces: []string{"default"}}),
		funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalRuns: true}))
	base := "http://" + cp.Addr()
	c := answersClient()
	ask{what: "declare the node", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/workernodes", token: clusterAdmin,
		body: `{"apiVersion":"funcd.io/v1alpha1","kind":"WorkerNode","metadata":{"name":"vm1"}}`, status: http.StatusOK}.run(t, c, base)
	const path = "/apis/funcd.io/v1alpha1/workernodes/vm1/token"
	for _, a := range []ask{
		{what: "a token for a declared node", method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"10m"}`, status: http.StatusOK},
		{what: "no credential", method: http.MethodPost, path: path, body: `{}`, status: http.StatusUnauthorized},
		{what: "a developer", method: http.MethodPost, path: path, token: dev, body: `{}`, status: http.StatusForbidden},
		{what: "an undeclared node", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/workernodes/vm2/token",
			token: clusterAdmin, body: `{}`, status: http.StatusNotFound},
		{what: "a name that is no DNS label", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/workernodes/VM_1/token",
			token: clusterAdmin, body: `{}`, status: http.StatusUnprocessableEntity},
		{what: "a body that is no JSON", method: http.MethodPost, path: path, token: clusterAdmin, body: `{`,
			status: http.StatusBadRequest},
		{what: "a ttl that is no duration", method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"forever"}`,
			status: http.StatusBadRequest},
	} {
		a.run(t, c, base)
	}
}
