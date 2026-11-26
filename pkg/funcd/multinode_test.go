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
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	natsnode "github.com/pyvvo/funcd/internal/nodeapi/nats"
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
	runsTheNamespacePlacedOnIt(t, "", longpoll.NewConnector(), "https://")
}

// TestAWorkerNodeRunsTheNamespacePlacedOnItOverNATS is the same over the NATS driver: the control plane's cluster.driver
// is nats, and the node joins a nats:// URL, which its agent's connector follows.
func TestAWorkerNodeRunsTheNamespacePlacedOnItOverNATS(t *testing.T) {
	runsTheNamespacePlacedOnIt(t, "nats", natsnode.NewConnector(), "nats://")
}

func runsTheNamespacePlacedOnIt(t *testing.T, driver string, conn nodeapi.Connector, scheme string) {
	t.Helper()
	ctx := context.Background()
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", Driver: driver, LocalRuns: true}))
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
	_, err = agent.Join(ctx, conn, scheme+cp.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
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
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://"+node.Addr()+"/apis/funcd.io/v1alpha1/hold/release",
		strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+clusterAdmin)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.NotEqual(t, http.StatusMethodNotAllowed, resp.StatusCode, "but the node's own hold is released on the node")

	cur, err := admin.Get(ctx, v1.KindWorkerNode, "", "vm1")
	require.NoError(t, err)
	require.Error(t, admin.Delete(ctx, v1.KindWorkerNode, "", "vm1", sdk.IfVersion(cur.GetObjectMeta().ResourceVersion)),
		"a WorkerNode a namespace names cannot be deleted")
}

// TestAControlPlaneAloneRunsNoNamespace starts funcd server's platform: it serves the API and the node API, opens no data
// plane, and leaves an unplaced namespace's objects waiting for a node (FEAT-0002 F241).
func TestAControlPlaneAloneRunsNoNamespace(t *testing.T) {
	ctx := context.Background()
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0"}))
	require.Empty(t, cp.DataPlaneAddr())
	require.NotEmpty(t, cp.ClusterAddr())
	admin, err := sdk.New("http://"+cp.Addr(), sdk.WithToken(clusterAdmin))
	require.NoError(t, err)
	kv := &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = "kv1", "e1", "rg1"
	_, err = admin.Apply(ctx, kv)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)
	obj, err := admin.Get(ctx, v1.KindKVStore, "e1", "kv1")
	require.NoError(t, err)
	require.Empty(t, obj.(*v1.KVStore).Status.Phase)
}

func TestAnAPITokenKeptAsAHashWorksUntilItIsMintedAgainOrDeleted(t *testing.T) {
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New())))
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
	ask{what: "a ttl past a year", method: http.MethodPost, path: path, token: clusterAdmin, body: `{"ttl":"9000h"}`,
		status: http.StatusBadRequest}.run(t, c, base)
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
	_, err = agent.Join(ctx, longpoll.NewConnector(), "https://"+cp.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
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

	rt := &v1.Route{TypeMeta: tmeta(v1.KindRoute)}
	rt.Name, rt.Namespace, rt.ResourceGroup, rt.Spec.Host = "echo", "e1", "rg1", "echo.example.com"
	rt.Spec.Rules = []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: "echo"}}}
	_, err = admin.Apply(ctx, rt)
	require.NoError(t, err)
	edge := func(p *funcd.Platform) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+p.DataPlaneAddr()+"/", nil)
		require.NoError(t, err)
		req.Host = "echo.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	require.Eventually(t, func() bool { return edge(node) == http.StatusOK }, 10*time.Second, 50*time.Millisecond, "the node's edge serves the Route")
	ready := func(ns v1.NamespaceName, want v1.Phase) func() bool {
		return func() bool {
			obj, err := admin.Get(ctx, v1.KindRoute, ns, "echo")
			return err == nil && obj.(*v1.Route).Status.Phase == want
		}
	}
	require.Eventually(t, ready("e1", v1.PhaseReady), 10*time.Second, 50*time.Millisecond, "the Route reads Ready, as the node reports it")
	rt = &v1.Route{TypeMeta: tmeta(v1.KindRoute), ObjectMeta: v1.ObjectMeta{Name: "echo", Namespace: "default", ResourceGroup: "rg1"}, Spec: rt.Spec}
	rt.Spec.Host = "here.example.com"
	_, err = admin.Apply(ctx, rt)
	require.NoError(t, err)
	require.Eventually(t, ready("default", v1.PhasePending), 10*time.Second, 50*time.Millisecond, "the control plane passes over its own Routes")
	require.Equal(t, http.StatusNotFound, edge(cp), "its edge serves none of the node's")
	require.True(t, ready("e1", v1.PhaseReady)(), "nor does it write their status")

	cpWorkers.mu.Lock()
	defer cpWorkers.mu.Unlock()
	require.Empty(t, cpWorkers.insts, "the control plane starts no worker for a namespace placed on a node")
}

// TestAJobRunsOnTheWorkerNodeItsNamespaceIsPlacedOn applies a Job and starts a run of it on the control plane before and
// after its node joins: the node runs it, and the control plane reads its result, who started it and the Job's generation
// as the node reports them (FEAT-0002 F244, ADR-0347).
func TestAJobRunsOnTheWorkerNodeItsNamespaceIsPlacedOn(t *testing.T) {
	ctx := context.Background()
	nodeWorkers := newFakeWorkers()
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))), funcd.WithRuntimeShim("node", "shim.mjs"),
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
	report := job("report")
	report.Namespace, report.Spec.Approval.Bypass = "e1", true
	report.Spec.Image = "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
	_, err = admin.Apply(ctx, report)
	require.NoError(t, err)
	report.Spec.Concurrency = 2
	applied, err := admin.Apply(ctx, report)
	require.NoError(t, err)
	require.Equal(t, int64(2), applied.GetObjectMeta().Generation)
	start := func(name v1.ObjectName) {
		run := jobRun(name, "report", "")
		run.Namespace = "e1"
		_, err := admin.Create(ctx, run)
		require.NoError(t, err)
	}
	start("before")

	tok, err := admin.MintJoinToken(ctx, "vm1", "")
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, longpoll.NewConnector(), "https://"+cp.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
	require.NoError(t, err)
	for _, name := range []v1.ObjectName{"before", "after"} {
		nodeWorkers.serve(t, name, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"from":"vm1"}`))
		})
	}
	runPlatform(t, funcd.WithNodeAgent(funcd.NodeAgentOptions{Dir: dir, Version: "test"}), funcd.WithRuntime(nodeWorkers),
		funcd.WithRuntimeShim("node", "shim.mjs"))
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindJob, "e1", "report")
		if err != nil {
			return false
		}
		j := obj.(*v1.Job)
		c, ok := j.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == j.Generation
	}, 20*time.Second, 50*time.Millisecond, "the Job reads Ready at its generation")
	start("after")
	for _, name := range []v1.ObjectName{"before", "after"} {
		var run *v1.JobRun
		require.Eventually(t, func() bool {
			obj, err := admin.Get(ctx, v1.KindJobRun, "e1", name)
			if err != nil {
				return false
			}
			run = obj.(*v1.JobRun)
			return run.Status.Phase.Ended()
		}, 20*time.Second, 50*time.Millisecond, "the run %s ends", name)
		c, _ := run.Status.Conditions.Get("Ready")
		require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "%s: %s %s", name, c.Reason, c.Message)
		require.JSONEq(t, `{"from":"vm1"}`, string(run.Status.Result))
		require.Equal(t, applied.GetObjectMeta().UID, run.Status.JobUID)
	}
}

// TestAWriteThatNeedsAnOfflineNodeIsRefusedAtOnce writes into a namespace whose worker node never joined: each write
// whose check reads the node's state answers 503 at once, and the others go through (FEAT-0002 F250).
func TestAWriteThatNeedsAnOfflineNodeIsRefusedAtOnce(t *testing.T) {
	cp := runPlatform(t, funcd.WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalRuns: true}))
	base := "http://" + cp.Addr()
	c := answersClient()
	const api = "/apis/funcd.io/v1alpha1"
	const e1 = api + "/namespaces/e1"
	meta := func(kind, name string) string {
		return `"apiVersion":"funcd.io/v1alpha1","kind":"` + kind + `","metadata":{"name":"` + name + `","namespace":"e1","resourceGroup":"rg1"}`
	}
	const fnSpec = `"spec":{"runtime":"nodejs22","handler":"handle","image":"file:///nonexistent/api.mjs"`
	const volumes = `,"volumes":[{"name":"scratch","type":"emptyDir","size":"1Mi"}]`
	for _, a := range []ask{
		{what: "declare the node", method: http.MethodPost, path: api + "/workernodes", token: clusterAdmin,
			body: `{"apiVersion":"funcd.io/v1alpha1","kind":"WorkerNode","metadata":{"name":"vm1"}}`, status: http.StatusOK},
		{what: "place e1 on it", method: http.MethodPost, path: api + "/namespaces", token: clusterAdmin,
			body: `{"apiVersion":"funcd.io/v1alpha1","kind":"Namespace","metadata":{"name":"e1"},"spec":{"node":"vm1"}}`, status: http.StatusOK},
		{what: "a store needs no node to be made", method: http.MethodPost, path: e1 + "/kvstores", token: clusterAdmin,
			body: `{` + meta("KVStore", "kv1") + `,"spec":{}}`, status: http.StatusOK},
		{what: "but its delete checks its keys on the node", method: http.MethodDelete, path: e1 + "/kvstores/kv1", token: clusterAdmin,
			status: http.StatusServiceUnavailable, detail: "is offline"},
		{what: "a Bucket", method: http.MethodPost, path: e1 + "/buckets", token: clusterAdmin,
			body: `{` + meta("Bucket", "b1") + `,"spec":{"prefixes":[{"name":"raw"}]}}`, status: http.StatusOK},
		{what: "its delete", method: http.MethodDelete, path: e1 + "/buckets/b1", token: clusterAdmin, status: http.StatusServiceUnavailable, detail: "is offline"},
		{what: "a SQLDatabase", method: http.MethodPost, path: e1 + "/sqldatabases", token: clusterAdmin,
			body: `{` + meta("SQLDatabase", "db1") + `,"spec":{}}`, status: http.StatusOK},
		{what: "its delete", method: http.MethodDelete, path: e1 + "/sqldatabases/db1", token: clusterAdmin, status: http.StatusServiceUnavailable, detail: "is offline"},
		{what: "a Function with no volume needs nothing of the node", method: http.MethodPost, path: e1 + "/functions", token: clusterAdmin,
			body: `{` + meta("Function", "api") + `,` + fnSpec + `}}`, status: http.StatusOK},
		{what: "one with a volume is booked on the node", method: http.MethodPost, path: e1 + "/functions", token: clusterAdmin,
			body: `{` + meta("Function", "worker") + `,` + fnSpec + volumes + `}}`, status: http.StatusServiceUnavailable, detail: "is offline"},
		{what: "and so is a replace that adds one", method: http.MethodPut, path: e1 + "/functions/api", token: clusterAdmin,
			body: `{` + meta("Function", "api") + `,` + fnSpec + volumes + `}}`, status: http.StatusServiceUnavailable, detail: "is offline"},
		{what: "an in-place Restore is checked on the node", method: http.MethodPost, path: e1 + "/restores", token: clusterAdmin,
			body: `{` + meta("Restore", "r1") + `,"spec":{"backup":"nightly","source":{"credentialsSecret":"creds","identitySecret":"age"},` +
				`"members":[{"kind":"KVStore","name":"kv1","inPlace":true}]}}`, status: http.StatusServiceUnavailable, detail: "is offline"},
	} {
		a.run(t, c, base)
	}
}

// asCluster makes an answers platform a control plane that worker nodes join, on loopback (FEAT-0002).
func asCluster(t *testing.T) funcd.Option {
	return funcd.WithCluster(funcd.ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalRuns: true})
}

// placeOnOfflineNode places namespace e1 of e, an asCluster platform, on worker node vm1, which never joins: a write
// whose check reads the node's state answers 503 there (FEAT-0002 F250).
func placeOnOfflineNode(t *testing.T, e *answerEnv) {
	t.Helper()
	ask{what: "declare the node", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/workernodes", token: answersAdmin,
		body: `{"apiVersion":"funcd.io/v1alpha1","kind":"WorkerNode","metadata":{"name":"vm1"}}`, status: http.StatusOK}.run(t, e.c, e.api)
	ns := namespace("e1")
	ns.Spec.Node = "vm1"
	create(t, e.c, e.api, ns)
}

// offlineRow is op's row for the case of a worker node that is offline, on a platform that placeOnOfflineNode set up.
func offlineRow(op string, e *answerEnv, setup func(*testing.T, *answerEnv), a ask) answerRow {
	a.token, a.detail = answersAdmin, "is offline"
	return answerRow{op: op, when: "worker node that is offline", on: e, setup: setup, ask: a}
}

// awayRow is op's row for a read or an action of namespace e1, whose worker node is offline, at path under e1.
func awayRow(op string, e *answerEnv, method, path string) answerRow {
	return offlineRow(op, e, nil, ask{what: "a namespace whose worker node is offline", method: method,
		path: "/apis/funcd.io/v1alpha1/namespaces/e1/" + path})
}

// hostRows are the create and the replace rows of a kind's claim on a host that a Route of e1 claims, whose worker node is
// not the control plane's own, on a platform that placeOnOfflineNode set up. body is the kind's object name with that host.
func hostRows(t *testing.T, e *answerEnv, kind, path string, name v1.ObjectName, body func(name v1.ObjectName, host string) string) []answerRow {
	t.Helper()
	const host = "taken.example.com"
	theirs := route("taken", "/")
	theirs.Namespace, theirs.Spec.Host = "e1", host
	create(t, e.c, e.api, theirs)
	coll := "/apis/funcd.io/v1alpha1/namespaces/default/" + path
	row := func(op, method, path string, name v1.ObjectName) answerRow {
		return answerRow{op: op + kind, when: "a namespace on another worker node claims", on: e, ask: ask{
			what: "a host of another node", method: method, path: path, token: answersAdmin, body: body(name, host),
			detail: "host " + host + " belongs to worker node vm1"}}
	}
	return []answerRow{row("create", http.MethodPost, coll, name+"-host"), row("replace", http.MethodPut, coll+"/"+string(name), name)}
}

// withVolume is fn with a volume, which its node books.
func withVolume(fn *v1.Function) *v1.Function {
	fn.Spec.Volumes = []v1.VolumeEntry{{Name: "scratch", Type: v1.VolumeEmptyDir, Size: "1Mi"}}
	return fn
}
