package multinode

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func tm(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

func namespace(name, node string) *v1.Namespace {
	n := &v1.Namespace{TypeMeta: tm(v1.KindNamespace)}
	n.Name, n.Spec.Node = v1.ObjectName(name), v1.ObjectName(node)
	return n
}

func workerNode(name string) *v1.WorkerNode {
	n := &v1.WorkerNode{TypeMeta: tm(v1.KindWorkerNode)}
	n.Name = v1.ObjectName(name)
	return n
}

func TestATokenReadsBackAndAMalformedOneIsRefused(t *testing.T) {
	tok := Token{CAHash: HashSecret("ca"), Node: "vm1", Secret: "s3cret"}
	got, err := ParseToken(tok.String())
	require.NoError(t, err)
	require.Equal(t, tok, got)
	for _, bad := range []string{"", "K10abc::vm1:s", "K11" + HashSecret("ca") + "::vm1:s", "K10" + HashSecret("ca") + "::vm1"} {
		_, err := ParseToken(bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
	}
	require.True(t, secretMatches("s3cret", HashSecret("s3cret")))
	require.False(t, secretMatches("other", HashSecret("s3cret")))
}

func TestTheCASignsANodeCertificateForTheNodeAlone(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ca, err := LoadOrCreateCA(dir, now)
	require.NoError(t, err)
	again, err := LoadOrCreateCA(dir, now)
	require.NoError(t, err)
	require.Equal(t, ca.Hash(), again.Hash(), "the CA is kept")
	cert, err := ca.ServerCertificate([]string{"homebox.lan", "10.0.0.5"}, now)
	require.NoError(t, err)
	require.Len(t, cert.Certificate, 2, "the chain carries the CA, so a joining node can check its hash")
	require.Equal(t, ca.Hash(), CertHash(cert.Certificate[1]))
}

func TestPlacementSkipsWhatAnotherNodeRuns(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	for _, n := range []*v1.Namespace{namespace("here", ""), namespace("there", "vm1"), namespace("local", "local")} {
		_, err := st.Create(ctx, n)
		require.NoError(t, err)
	}
	aio := NewPlacement(st, "local", true)
	require.False(t, aio.Skip("here"), "an unplaced namespace runs on an all-in-one")
	require.False(t, aio.Skip("local"))
	require.False(t, aio.Skip("missing"))
	require.True(t, aio.Skip("there"))
	require.False(t, aio.Skip(""), "cluster-scoped objects always run")
	cp := NewPlacement(st, "local", false)
	require.True(t, cp.Skip("here"), "a control plane alone runs no unplaced namespace")
	require.True(t, cp.Skip("there"))
	got, err := aio.Namespaces(ctx, "vm1")
	require.NoError(t, err)
	require.Equal(t, []v1.NamespaceName{"there"}, got)
}

func TestANamespaceNamesADeclaredNodeAndKeepsItWhileItHoldsObjects(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	a := namespaceNode{st: st, local: "local"}
	req := func(op admission.Operation, n, old *v1.Namespace) admission.Request {
		r := admission.Request{Operation: op, GVK: v1.KindNamespace.GVK(), Object: n}
		if old != nil {
			r.Old = old
		}
		return r
	}
	_, err := a.Admit(ctx, req(admission.Create, namespace("e1", "vm1"), nil))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an undeclared node is refused")
	_, err = a.Admit(ctx, req(admission.Create, namespace("e1", "local"), nil))
	require.NoError(t, err, "the local node needs no WorkerNode")
	_, err = st.Create(ctx, workerNode("vm1"))
	require.NoError(t, err)
	_, err = a.Admit(ctx, req(admission.Create, namespace("e1", "vm1"), nil))
	require.NoError(t, err)

	_, err = a.Admit(ctx, req(admission.Update, namespace("e1", "vm1"), namespace("e1", "")))
	require.NoError(t, err, "an empty namespace may move")
	cm := &v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap)}
	cm.Name, cm.Namespace, cm.ResourceGroup = "c", "e1", "rg1"
	_, err = st.Create(ctx, cm)
	require.NoError(t, err)
	_, err = a.Admit(ctx, req(admission.Update, namespace("e1", "vm1"), namespace("e1", "")))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a namespace that holds objects keeps its node")
}

func TestAWorkerNodeANamespaceNamesCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	_, err := st.Create(ctx, workerNode("vm1"))
	require.NoError(t, err)
	_, err = st.Create(ctx, namespace("e1", "vm1"))
	require.NoError(t, err)
	a := workerNodeInUse{st: st}
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Delete, GVK: v1.KindWorkerNode.GVK(), Old: workerNode("vm1")})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Delete, GVK: v1.KindWorkerNode.GVK(), Old: workerNode("vm2")})
	require.NoError(t, err)
}

func TestAJoinTokenNeedsADeclaredNodeAndABoundedLife(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New(), store.WithChangeLog(0))
	c, err := New(Config{Store: st, Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalNode: "local", LocalRuns: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	_, _, err = c.MintJoinToken(ctx, "vm1", 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, err = st.Create(ctx, workerNode("vm1"))
	require.NoError(t, err)
	_, _, err = c.MintJoinToken(ctx, "vm1", 48*time.Hour)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	tok, exp, err := c.MintJoinToken(ctx, "vm1", time.Minute)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Minute), exp, 5*time.Second)
	parsed, err := ParseToken(tok)
	require.NoError(t, err)
	require.Equal(t, c.CA().Hash(), parsed.CAHash)
	obj, err := st.Get(ctx, v1.KindWorkerNode.GVK(), "", "vm1")
	require.NoError(t, err)
	require.Equal(t, HashSecret(parsed.Secret), obj.(*v1.WorkerNode).Status.PendingJoin.TokenHash, "only the hash is kept")
}

func TestCallsFailAtOnceForAnOfflineNode(t *testing.T) {
	calls := NewCalls(time.Now)
	_, err := calls.Do(context.Background(), "vm1", Call{Kind: CallLogs})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	go func() {
		for {
			for _, c := range calls.Poll(context.Background(), "vm1", time.Second) {
				_ = calls.Answer("vm2", c.ID, Answer{Error: "wrong sender"})
				_ = calls.Answer("vm1", c.ID, Answer{})
				return
			}
		}
	}()
	require.Eventually(t, func() bool { return calls.Online("vm1") }, 2*time.Second, 10*time.Millisecond)
	_, err = calls.Do(context.Background(), "vm1", Call{Kind: CallLogs})
	require.NoError(t, err, "only the node the call went to may answer it")
}

func TestAHostBelongsToOneNode(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	for _, n := range []*v1.Namespace{namespace("a", "vm1"), namespace("b", "vm1"), namespace("c", "vm2"), namespace("d", "")} {
		_, err := st.Create(ctx, n)
		require.NoError(t, err)
	}
	route := func(ns, name, host string) *v1.Route {
		r := &v1.Route{TypeMeta: tm(v1.KindRoute)}
		r.Name, r.Namespace, r.ResourceGroup, r.Spec.Host = v1.ObjectName(name), v1.NamespaceName(ns), "rg1", host
		r.Spec.Rules = []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: "f"}}}
		return r
	}
	_, err := st.Create(ctx, route("a", "r", "Shop.Example.COM."))
	require.NoError(t, err)
	a := hostOnOneNode{st: st, placement: NewPlacement(st, "local", true)}
	admit := func(r *v1.Route) error {
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindRoute.GVK(), Object: r})
		return err
	}
	require.NoError(t, admit(route("b", "r", "shop.example.com")), "namespaces on one node share a host")
	require.Equal(t, fault.Conflict, fault.KindOf(admit(route("c", "r", "shop.example.com:443"))), "another node may not claim it")
	require.Equal(t, fault.Conflict, fault.KindOf(admit(route("d", "r", "shop.example.com"))), "nor the control plane's own node")
	require.NoError(t, admit(route("c", "r", "other.example.com")))
	require.NoError(t, admit(route("c", "r", "")), "the namespace host is the node's own")
}
