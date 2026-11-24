package funcd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func TestANodesBackupLeavesItsSecretsOut(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	for _, k := range []v1.Kind{v1.KindSecret, v1.KindConfigMap} {
		obj, ok := v1.NewObject(k)
		require.True(t, ok)
		m := obj.GetObjectMeta()
		m.Name, m.Namespace, m.ResourceGroup = "app", "e1", "rg1"
		if s, ok := obj.(*v1.Secret); ok {
			s.Spec.Data = map[string][]byte{"token": []byte("s3cr3t")}
		}
		_, err := st.Create(ctx, obj)
		require.NoError(t, err)
	}
	var all, node [][]byte
	_, err := st.Snapshot(ctx, func(r snapshot.Record) error { all = append(all, r.Key); return nil })
	require.NoError(t, err)
	_, err = withoutSecrets{st}.Snapshot(ctx, func(r snapshot.Record) error { node = append(node, r.Key); return nil })
	require.NoError(t, err)
	has := func(keys [][]byte, k v1.Kind) bool {
		for _, key := range keys {
			if bytes.HasPrefix(key, []byte(k.GVK().String()+"\x00")) {
				return true
			}
		}
		return false
	}
	require.True(t, has(all, v1.KindSecret))
	require.False(t, has(node, v1.KindSecret), "a node's backup holds no Secret")
	require.True(t, has(node, v1.KindConfigMap), "and every other object")
	require.Len(t, node, len(all)-1)
}

// TestAnAddonObjectRunsOnTheWorkerNodeItsNamespaceIsPlacedOn registers an Addon on the control plane: the engine of an
// object of a node's namespace runs on that node, on the revision the control plane stamped, and keeps it while the Addon
// moves on, until the object is edited (FEAT-0002, ADR-0339).
func TestAnAddonObjectRunsOnTheWorkerNodeItsNamespaceIsPlacedOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cp := startAddonPlatform(t, newAddonRuntime(t), WithStore(store.New(memory.New(), store.WithChangeLog(0))),
		WithCluster(ClusterOptions{Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalRuns: true}))
	wn := &v1.WorkerNode{TypeMeta: tmeta(v1.KindWorkerNode)}
	wn.Name = "vm1"
	cp.apply(t, cp.admin, wn)
	ns := &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	cp.apply(t, cp.admin, ns)
	tok, err := cp.admin.MintJoinToken(ctx, "vm1", "")
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, "https://"+cp.p.ClusterAddr(), tok.Token, dir, "test", string(v1.HostPlatform()))
	require.NoError(t, err)
	rt := newAddonRuntime(t)
	node := startAddonPlatform(t, rt, WithNodeAgent(NodeAgentOptions{Dir: dir, Version: "test"}), WithAddonRuntime(rt))

	ad := rauthyAddon("rauthy")
	cp.apply(t, cp.admin, ad)
	cp.apply(t, cp.admin, rauthyKeys("e1", map[string]string{"ENC_KEYS": "k1"}))
	login := identityProvider("e1", "rauthy-keys")
	cp.apply(t, cp.admin, login)
	o := cp.waitReason(t, "e1", "login", v1.PhaseReady, "")
	require.Equal(t, v1.ObjectName("rauthy-1"), o.Status.AddonRevision)
	spec, running, starts := rt.state(ref("e1", "login"))
	require.True(t, running, "the node runs the engine")
	require.Equal(t, "info", spec.Env["LOG_LEVEL"])
	require.Equal(t, "k1", spec.Env["ENC_KEYS"])
	require.Equal(t, 1, starts)

	ad.Spec.Runtimes[0].Attributes.Env["LOG_LEVEL"] = "debug"
	cp.apply(t, cp.admin, ad)
	current := func(e *addonEnv) v1.ObjectName {
		obj, err := e.p.cfg.store.Get(ctx, v1.KindAddon.GVK(), "", "rauthy")
		require.NoError(t, err)
		return obj.(*v1.Addon).Status.CurrentRevision
	}
	require.Eventually(t, func() bool { return current(node) == "rauthy-2" }, 5*time.Second, 10*time.Millisecond,
		"the node's copy shows the control plane's revision")
	revisions := func(e *addonEnv) []v1.ObjectName {
		res, err := e.p.cfg.store.List(ctx, v1.KindAddonRevision.GVK(), store.ListOptions{})
		require.NoError(t, err)
		var out []v1.ObjectName
		for _, obj := range res.Items {
			out = append(out, obj.GetObjectMeta().Name)
		}
		return out
	}
	time.Sleep(300 * time.Millisecond) // three supervision periods
	require.Equal(t, []v1.ObjectName{"rauthy-1", "rauthy-2"}, revisions(cp), "the object on the node pins rauthy-1")
	require.Equal(t, []v1.ObjectName{"rauthy-1", "rauthy-2"}, revisions(node))
	_, _, starts = rt.state(ref("e1", "login"))
	require.Equal(t, 1, starts, "its engine goes on")

	login.Spec.Config = json.RawMessage(`{"issuer":"https://id.example.com/auth/v1","port":8080}`)
	cp.apply(t, cp.admin, login)
	require.Eventually(t, func() bool {
		o, ok := cp.object(t, "e1", "login")
		return ok && o.Status.AddonRevision == "rauthy-2" && o.Status.ObservedGeneration == o.Generation && o.Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond, "an edited object takes the current revision")
	spec, _, _ = rt.state(ref("e1", "login"))
	require.Equal(t, "debug", spec.Env["LOG_LEVEL"])
	require.Eventually(t, func() bool { return len(revisions(cp)) == 1 && len(revisions(node)) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the revision nothing uses goes on both")

	require.NoError(t, cp.admin.Delete(ctx, "IdentityProvider", "e1", "login"))
	require.Eventually(t, func() bool {
		_, running, _ := rt.state(ref("e1", "login"))
		return !running
	}, 5*time.Second, 10*time.Millisecond, "a deleted object's engine stops on the node")
	_, err = node.p.cfg.store.Get(ctx, v1.KindAddonObject.GVK(), "e1", "login")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}
