package multinode

import (
	"context"
	"crypto/x509"
	"maps"
	"slices"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// newTestCluster is a control plane of st over the HTTPS long poll on loopback.
func newTestCluster(t *testing.T, st store.Store) *Cluster {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir(), time.Now())
	require.NoError(t, err)
	tlsCfg, err := ca.ServerTLS(nil, time.Now)
	require.NoError(t, err)
	api, err := longpoll.Listen(longpoll.Config{Addr: "127.0.0.1:0", TLS: tlsCfg})
	require.NoError(t, err)
	c, err := New(Config{Store: st, CA: ca, API: api, LocalNode: "local", LocalRuns: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

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

// A write wakes only the feeds of the nodes it concerns (#1586): a record of a namespace wakes the node it is placed on,
// unless that node wrote it, and a Namespace wakes every node.
func TestAWriteWakesOnlyTheNodesItConcerns(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New(), store.WithChangeLog(0))
	for _, obj := range []v1.Object{workerNode("vm1"), workerNode("vm2"), namespace("e1", "vm1"), namespace("e2", "vm2")} {
		_, err := st.Create(ctx, obj)
		require.NoError(t, err)
	}
	h := newTestCluster(t, st).handler
	rctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	require.NoError(t, h.startRouting(rctx))
	woken := func(ch <-chan struct{}, within time.Duration) bool {
		select {
		case <-ch:
			return true
		case <-time.After(within):
			return false
		}
	}
	write := func(ctx context.Context, name v1.ObjectName) {
		cm := &v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap)}
		cm.Name, cm.Namespace, cm.ResourceGroup = name, "e1", "rg1"
		_, err := st.Create(ctx, cm)
		require.NoError(t, err)
	}

	vm1, vm2 := h.Changed("vm1"), h.Changed("vm2")
	write(ctx, "a")
	require.True(t, woken(vm1, 5*time.Second), "a write in e1 wakes vm1, which runs e1")
	require.False(t, woken(vm2, 200*time.Millisecond), "and leaves vm2 waiting")

	vm1 = h.Changed("vm1")
	write(store.WithOrigin(ctx, "vm1"), "b")
	require.False(t, woken(vm1, 200*time.Millisecond), "a write vm1 made itself does not wake it")

	vm1, vm2 = h.Changed("vm1"), h.Changed("vm2")
	_, err := st.Create(ctx, namespace("e3", ""))
	require.NoError(t, err)
	require.True(t, woken(vm1, 5*time.Second), "a Namespace wakes vm1")
	require.True(t, woken(vm2, 5*time.Second), "and vm2")
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

// A BackupSchedule's target stays outside the blob store of the node that runs its namespace, not the control plane's
// (decision 9, ADR-0222 Decision 6).
func TestABackupSchedulesTargetStaysOutsideItsNodesBlobStore(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New(), store.WithChangeLog(0))
	c := newTestCluster(t, st)
	reported := workerNode("vm1")
	reported.Status.BlobStore = "s3://node-blobs?region=eu-west-3"
	for _, obj := range []v1.Object{reported, workerNode("vm2"), namespace("e1", "vm1"), namespace("e2", "vm2"), namespace("here", "")} {
		_, err := st.Create(ctx, obj)
		require.NoError(t, err)
	}
	var recipients []string
	for range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		recipients = append(recipients, id.Recipient().String())
	}
	check := c.Forward([]admission.Admission{admission.NewBackupScheduleAdmission("s3://cp-blobs?region=eu-west-3")})[0]
	admit := func(ns v1.NamespaceName, bucket string) error {
		s := &v1.BackupSchedule{TypeMeta: tm(v1.KindBackupSchedule)}
		s.Name, s.Namespace = "nightly", ns
		s.Spec.Target.URL = "s3://" + bucket + "?region=eu-west-3"
		s.Spec.Encryption.Recipients = recipients
		_, err := check.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindBackupSchedule.GVK(), Object: s})
		return err
	}
	require.True(t, check.Handles(v1.KindBackupSchedule.GVK(), admission.Create))
	require.ErrorContains(t, admit("here", "cp-blobs"), "inside the blob store")
	require.NoError(t, admit("here", "node-blobs"))
	require.ErrorContains(t, admit("e1", "node-blobs"), "inside the blob store")
	require.NoError(t, admit("e1", "cp-blobs"), "the control plane's store is another one")
	require.NoError(t, admit("e2", "node-blobs"), "nothing is known of a node that has not joined")
}

// The node API's own certificate is signed again before it ends, so a control plane that runs for years keeps its nodes.
func TestTheNodeAPIsCertificateIsRenewedBeforeItEnds(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	ca, err := LoadOrCreateCA(t.TempDir(), now)
	require.NoError(t, err)
	c := &serverCertificate{ca: ca, now: func() time.Time { return now }}
	first, err := c.get(nil)
	require.NoError(t, err)
	now = now.Add(certLifetime - renewWithin)
	same, err := c.get(nil)
	require.NoError(t, err)
	require.Equal(t, first.Certificate[0], same.Certificate[0])
	now = now.Add(time.Hour)
	next, err := c.get(nil)
	require.NoError(t, err)
	require.NotEqual(t, first.Certificate[0], next.Certificate[0])
	leaf, err := x509.ParseCertificate(next.Certificate[0])
	require.NoError(t, err)
	require.True(t, leaf.NotAfter.Equal(now.Add(certLifetime)))
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
	c := newTestCluster(t, st)
	_, _, err := c.MintJoinToken(ctx, "vm1", 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, err = st.Create(ctx, workerNode("vm1"))
	require.NoError(t, err)
	_, _, err = c.MintJoinToken(ctx, "vm1", 48*time.Hour)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, _, err = c.MintJoinToken(ctx, "vm1", 500*time.Millisecond)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a token lives a second at least")
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
	login := &v1.AddonObject{TypeMeta: tm(v1.KindAddonObject)}
	login.Name, login.Namespace, login.Spec.Ingress = "login", "c", &v1.Ingress{Host: "shop.example.com"}
	require.True(t, a.Handles(v1.KindAddonObject.GVK(), admission.Create))
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindAddonObject.GVK(), Object: login})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "an add-on object's ingress claims its host too")
}

func TestASpecFieldANodeIsTooOldForIsRefused(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	for _, n := range []*v1.Namespace{namespace("old", "vm1"), namespace("new", "vm2"), namespace("waiting", "vm3"), namespace("here", "")} {
		_, err := st.Create(ctx, n)
		require.NoError(t, err)
	}
	for name, api := range map[string]int{"vm1": 1, "vm2": 2, "vm3": 0} {
		wn := workerNode(name)
		wn.Status.NodeAPIVersion = api
		_, err := st.Create(ctx, wn)
		require.NoError(t, err)
	}
	a := nodeTooOld{st: st, placement: NewPlacement(st, "local", true),
		fields: FieldVersions{v1.KindConfigMap: {"spec.data.retry": 2}}}
	require.True(t, a.Handles(v1.KindConfigMap.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindSecret.GVK(), admission.Update), "a kind with no newer field is not checked")
	admit := func(ns, key string) error {
		c := &v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap)}
		c.Name, c.Namespace, c.ResourceGroup = "c", v1.NamespaceName(ns), "rg1"
		c.Spec.Data = map[string]string{key: "1"}
		_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindConfigMap.GVK(), Object: c})
		return err
	}
	err := admit("old", "retry")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "worker node vm1 speaks node API version 1, and spec.data.retry needs version 2")
	require.NoError(t, admit("old", "other"), "a field every version runs")
	require.NoError(t, admit("new", "retry"), "a node that speaks the version")
	require.NoError(t, admit("waiting", "retry"), "a node that has not joined has no known version")
	require.NoError(t, admit("here", "retry"), "the control plane's own node runs its own release")
	require.False(t, newerRelease("v0.39.0", "v0.40.0"))
	require.True(t, newerRelease("v0.41.0", "v0.40.0"))
	require.False(t, newerRelease("v0.41.0", "dev"), "a release that does not order passes")
}

func TestANodeWritesBackOnlyTheKindsItsReconcilersMake(t *testing.T) {
	for _, k := range []v1.Kind{v1.KindRevision, v1.KindFunction, v1.KindKVStore, v1.KindRoute, v1.KindSecret, v1.KindInvocation} {
		require.True(t, WrittenBack(k, true), "an owned %s", k)
	}
	for _, k := range []v1.Kind{v1.KindPolicy, v1.KindRole, v1.KindRolesAssignment, v1.KindIdentity, v1.KindEgressPolicy, v1.KindApp} {
		require.False(t, WrittenBack(k, true), "no %s, even owned", k)
	}
	owned := map[v1.Kind]bool{}
	for _, p := range gc.Pairs() {
		if p.Child.Namespaced() {
			owned[p.Child] = true
		}
	}
	require.ElementsMatch(t, slices.Collect(maps.Keys(owned)), childKinds(), "each child kind a reconciler stamps, and no other")
	require.True(t, WrittenBack(v1.KindWorkflowRun, false), "a WorkflowRun a Sensor fires")
	require.True(t, WrittenBack(v1.KindBackup, false), "a Backup record a sync finds")
	require.False(t, WrittenBack(v1.KindConfigMap, false), "a person's ConfigMap")
}
