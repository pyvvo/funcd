//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
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

func TestAnAPITokenKeptAsAHashWorksUntilItIsMintedAgainOrDeleted(t *testing.T) {
	const dev = "cluster-dev-token"
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New())),
		funcd.WithCredentials(funcd.Credential{Token: clusterAdmin, Role: "admin"},
			funcd.Credential{Token: dev, Role: "developer", Namespaces: []string{"default"}}))
	base := "http://" + cp.Addr()
	c := answersClient()
	ask{what: "declare the token", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/apitokens", token: clusterAdmin,
		body:   `{"apiVersion":"funcd.io/v1alpha1","kind":"APIToken","metadata":{"name":"ci"},"spec":{"role":"developer","namespaces":["default"]}}`,
		status: http.StatusOK}.run(t, c, base)
	const path = "/apis/funcd.io/v1alpha1/apitokens/ci/token"
	mint := func() string {
		resp, err := ask{method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"1h"}`}.do(c, base)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var out struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out.Token
	}
	first := mint()
	const mine, other = "/apis/funcd.io/v1alpha1/namespaces/default/configmaps", "/apis/funcd.io/v1alpha1/namespaces/shop/configmaps"
	ask{what: "the token acts in its namespace", method: http.MethodGet, path: mine, token: first, status: http.StatusOK}.run(t, c, base)
	ask{what: "and in no other", method: http.MethodGet, path: other, token: first, status: http.StatusForbidden}.run(t, c, base)
	second := mint()
	ask{what: "a token minted again ends the one before", method: http.MethodGet, path: mine, token: first, status: http.StatusUnauthorized}.run(t, c, base)
	ask{what: "the new one works", method: http.MethodGet, path: mine, token: second, status: http.StatusOK}.run(t, c, base)
	for _, a := range []ask{
		{what: "no credential", method: http.MethodPost, path: path, body: `{}`, status: http.StatusUnauthorized},
		{what: "a developer", method: http.MethodPost, path: path, token: dev, body: `{}`, status: http.StatusForbidden},
		{what: "an undeclared token", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/apitokens/nope/token",
			token: clusterAdmin, body: `{}`, status: http.StatusNotFound},
		{what: "a name that is no DNS label", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/apitokens/CI_1/token",
			token: clusterAdmin, body: `{}`, status: http.StatusUnprocessableEntity},
		{what: "a body that is no JSON", method: http.MethodPost, path: path, token: clusterAdmin, body: `{`, status: http.StatusBadRequest},
		{what: "a ttl that is no duration", method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"forever"}`,
			status: http.StatusBadRequest},
		{what: "a ttl past a year", method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"9000h"}`,
			status: http.StatusBadRequest},
	} {
		a.run(t, c, base)
	}
	resp, err := ask{method: http.MethodGet, path: "/apis/funcd.io/v1alpha1/apitokens/ci", token: clusterAdmin}.do(c, base)
	require.NoError(t, err)
	stored, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.NotContains(t, string(stored), strings.TrimPrefix(second, "funcd.ci."), "the API shows a hash, never the token")
	resp, err = ask{method: http.MethodDelete, path: "/apis/funcd.io/v1alpha1/apitokens/ci", token: clusterAdmin}.do(c, base)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Less(t, resp.StatusCode, 300)
	ask{what: "a deleted token ends", method: http.MethodGet, path: mine, token: second, status: http.StatusUnauthorized}.run(t, c, base)
}

// TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn applies a Function on the control plane, and checks that the
// node it is placed on runs it, answers its calls on its own data plane and reports it Ready back (FEAT-0002 F241 to
// F244), while the control plane starts no worker for it.
func TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn(t *testing.T) {
	ctx := context.Background()
	cpWorkers, nodeWorkers := newFakeWorkers(), newFakeWorkers()
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))), funcd.WithRuntime(cpWorkers),
		funcd.WithRuntimeShim("node", "shim.mjs"),
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
	tok, err := admin.MintJoinToken(ctx, "vm1", "")
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, "https://"+cp.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
	require.NoError(t, err)
	nodeWorkers.serve(t, "echo", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("from vm1")) })
	node := runPlatform(t, funcd.WithNodeAgent(funcd.NodeAgentOptions{Dir: dir, Version: "test"}),
		funcd.WithRuntime(nodeWorkers), funcd.WithRuntimeShim("node", "shim.mjs"))

	fn := function("echo")
	fn.Namespace = "e1"
	fn.Spec.Image = "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
	fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
	_, err = admin.Apply(ctx, fn)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindFunction, "e1", "echo")
		return err == nil && obj.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 20*time.Second, 50*time.Millisecond, "the control plane reads the Function Ready, as the node reports it")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+node.DataPlaneAddr()+"/function/echo", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("X-Funcd-Namespace", "e1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Equal(t, "from vm1", string(body), "the node's data plane answers the call from the node's worker")

	cpWorkers.mu.Lock()
	defer cpWorkers.mu.Unlock()
	require.Empty(t, cpWorkers.insts, "the control plane starts no worker for a namespace placed on a node")
}
