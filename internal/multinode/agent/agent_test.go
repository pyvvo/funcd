package agent_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/auth/apitoken"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type fakeLogs struct{}

func (fakeLogs) Read(_ context.Context, q logread.Query) ([]logread.Line, error) {
	return []logread.Line{{Body: "hello from " + q.Namespace + "/" + q.Function}}, nil
}

func meta(kind v1.Kind) v1.TypeMeta {
	return v1.TypeMeta{APIVersion: kind.GVK().APIVersion(), Kind: kind}
}

func configMap(ns, name, n string) *v1.ConfigMap {
	c := &v1.ConfigMap{TypeMeta: meta(v1.KindConfigMap)}
	c.Name, c.Namespace, c.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg1"
	c.Spec.Data = map[string]string{"n": n}
	return c
}

type rig struct {
	cp, node store.Store
	cluster  *multinode.Cluster
	cancelCP context.CancelFunc
}

// cpMaster is the control plane's master secret, which every namespace's master derives from.
const cpMaster = "0123456789abcdef0123456789abcdef"

// startCP runs a control plane with the node API on loopback, and declares WorkerNode vm1.
func startCP(t *testing.T, opts ...func(*multinode.Config)) rig {
	t.Helper()
	cp := store.New(memory.New(), store.WithChangeLog(0))
	cfg := multinode.Config{Store: cp, Dir: t.TempDir(), ListenAddr: "127.0.0.1:0", LocalNode: "local", LocalRuns: true,
		Master: []byte(cpMaster)}
	for _, o := range opts {
		o(&cfg)
	}
	cluster, err := multinode.New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = cluster.Run(ctx) }()
	wn := &v1.WorkerNode{TypeMeta: meta(v1.KindWorkerNode)}
	wn.Name = "vm1"
	_, err = cp.Create(context.Background(), wn)
	require.NoError(t, err)
	return rig{cp: cp, node: store.New(memory.New()), cluster: cluster, cancelCP: cancel}
}

func (r rig) server() string { return "https://" + r.cluster.Addr() }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 10*time.Second, 20*time.Millisecond, what)
}

func TestANodeJoinsFollowsItsNamespaceAndReportsBack(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	name, err := agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("vm1"), name)
	_, err = agent.Join(ctx, r.server(), token, t.TempDir(), "test", "linux/arm64")
	require.ErrorContains(t, err, "no join token waiting", "a token works once")

	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()

	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)
	_, err = r.cp.Create(ctx, configMap("e1", "probe", "1"))
	require.NoError(t, err)
	_, err = r.cp.Create(ctx, configMap("other", "probe", "1"))
	require.NoError(t, err)
	nodeHas := func(ns, n string) bool {
		obj, err := r.node.Get(ctx, v1.KindConfigMap.GVK(), v1.NamespaceName(ns), "probe")
		return err == nil && obj.(*v1.ConfigMap).Spec.Data["n"] == n
	}
	eventually(t, "the ConfigMap of the placed namespace reaches the node", func() bool { return nodeHas("e1", "1") })
	require.False(t, nodeHas("other", "1"), "a namespace placed on no node stays off the node")
	eventually(t, "the node holds the namespace's master", func() bool {
		_, err := a.NamespaceMaster("e1")
		return err == nil
	})

	cur, err := r.cp.Get(ctx, v1.KindConfigMap.GVK(), "e1", "probe")
	require.NoError(t, err)
	next := configMap("e1", "probe", "2")
	next.ResourceVersion = cur.GetObjectMeta().ResourceVersion
	_, err = r.cp.Update(ctx, next)
	require.NoError(t, err)
	eventually(t, "a change reaches the node", func() bool { return nodeHas("e1", "2") })

	kv := &v1.KVStore{TypeMeta: meta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = "kv1", "e1", "rg1"
	_, err = r.cp.Create(ctx, kv)
	require.NoError(t, err)
	eventually(t, "the KVStore reaches the node", func() bool {
		_, err := r.node.Get(ctx, v1.KindKVStore.GVK(), "e1", "kv1")
		return err == nil
	})
	local, err := r.node.Get(ctx, v1.KindKVStore.GVK(), "e1", "kv1")
	require.NoError(t, err)
	lkv := local.(*v1.KVStore)
	lkv.Status.Phase = v1.PhaseReady
	_, err = r.node.Update(ctx, lkv)
	require.NoError(t, err)
	eventually(t, "the node's status reaches the control plane", func() bool {
		obj, err := r.cp.Get(ctx, v1.KindKVStore.GVK(), "e1", "kv1")
		return err == nil && obj.(*v1.KVStore).Status.Phase == v1.PhaseReady
	})

	child := configMap("e1", "child", "1")
	child.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindKVStore, Name: "kv1"}, UID: lkv.UID}}
	_, err = r.node.Create(ctx, child)
	require.NoError(t, err)
	eventually(t, "a child the node owns reaches the control plane", func() bool {
		_, err := r.cp.Get(ctx, v1.KindConfigMap.GVK(), "e1", "child")
		return err == nil
	})
	require.NoError(t, r.node.Delete(ctx, v1.KindConfigMap.GVK(), "e1", "child", ""))
	eventually(t, "the child's delete reaches the control plane", func() bool {
		_, err := r.cp.Get(ctx, v1.KindConfigMap.GVK(), "e1", "child")
		return fault.KindOf(err) == fault.NotFound
	})

	logs := r.cluster.Logs(nil)
	var lines []logread.Line
	eventually(t, "a log read of the namespace goes through the node", func() bool {
		lines, err = logs.Read(ctx, logread.Query{Namespace: "e1", Function: "f"})
		return err == nil
	})
	require.Equal(t, "hello from e1/f", lines[0].Body)

	eventually(t, "the WorkerNode reads Ready", func() bool {
		obj, err := r.cp.Get(ctx, v1.KindWorkerNode.GVK(), "", "vm1")
		return err == nil && obj.(*v1.WorkerNode).Status.Phase == v1.PhaseReady
	})

	stop()
	<-done
	time.Sleep(3500 * time.Millisecond)
	start := time.Now()
	_, err = logs.Read(ctx, logread.Query{Namespace: "e1", Function: "f"})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a call to an offline node fails")
	require.Less(t, time.Since(start), time.Second, "and fails at once")
}

func TestAJoinRefusesAControlPlaneWithAnotherCA(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	tok, err := multinode.ParseToken(token)
	require.NoError(t, err)
	tok.CAHash = "00" + tok.CAHash[2:]
	_, err = agent.Join(ctx, r.server(), tok.String(), t.TempDir(), "test", "linux/arm64")
	require.ErrorContains(t, err, "does not present the CA the join token names")
}

func TestANodeResumesFromItsSavedRevision(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)

	run := func() (context.CancelFunc, chan struct{}) {
		a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Version: "test"})
		require.NoError(t, err)
		actx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = a.Run(actx); close(done) }()
		return stop, done
	}
	stop, done := run()
	_, err = r.cp.Create(ctx, configMap("e1", "a", "1"))
	require.NoError(t, err)
	eventually(t, "the first change arrives", func() bool {
		_, err := r.node.Get(ctx, v1.KindConfigMap.GVK(), "e1", "a")
		return err == nil
	})
	stop()
	<-done
	for _, n := range []string{"b", "c", "d"} {
		_, err = r.cp.Create(ctx, configMap("e1", n, "1"))
		require.NoError(t, err)
	}
	stop, done = run()
	defer func() { stop(); <-done }()
	eventually(t, "every change made while the node was stopped arrives", func() bool {
		for _, n := range []string{"b", "c", "d"} {
			if _, err := r.node.Get(ctx, v1.KindConfigMap.GVK(), "e1", v1.ObjectName(n)); err != nil {
				return false
			}
		}
		return true
	})
}

func TestANodeKeepsTheMastersOfItsNamespacesOnly(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	run := func() (*agent.Agent, func()) {
		a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
		require.NoError(t, err)
		actx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = a.Run(actx); close(done) }()
		return a, func() { stop(); <-done }
	}
	a, stop := run()
	for _, name := range []v1.ObjectName{"e1", "e2", "other"} {
		ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
		ns.Name = name
		if name != "other" {
			ns.Spec.Node = "vm1"
		}
		_, err = r.cp.Create(ctx, ns)
		require.NoError(t, err)
	}
	eventually(t, "the node holds the masters of e1 and e2", func() bool {
		_, e1 := a.NamespaceMaster("e1")
		_, e2 := a.NamespaceMaster("e2")
		return e1 == nil && e2 == nil
	})
	m1, err := a.NamespaceMaster("e1")
	require.NoError(t, err)
	m2, err := a.NamespaceMaster("e2")
	require.NoError(t, err)
	require.NotEqual(t, m1, m2, "each namespace has its own master")
	obj, err := r.cp.Get(ctx, v1.KindNamespace.GVK(), "", "e1")
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(cpMaster))
	mac.Write([]byte("funcd-namespace-key/v1/" + string(obj.GetObjectMeta().UID)))
	require.Equal(t, mac.Sum(nil), m1, "a namespace's master derives from the platform's and the namespace's UID")
	_, err = a.NamespaceMaster("other")
	require.Error(t, err, "a namespace on no node has no master on this one")
	stop()

	a, stop = run()
	defer stop()
	kept, err := a.NamespaceMaster("e1")
	require.NoError(t, err)
	require.Equal(t, m1, kept, "the master is kept on disk, so a restart needs no control plane")
	require.NoError(t, r.cp.Delete(ctx, v1.KindNamespace.GVK(), "", "e2", ""))
	eventually(t, "the master of a namespace that left the node goes", func() bool {
		_, err := a.NamespaceMaster("e2")
		return err != nil
	})
	_, err = os.Stat(filepath.Join(dir, "keys", "e2.json"))
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// A node renews its certificate in the last third of its life, and its first call with the new one ends the old one.
func TestANodeRenewsItsCertificateBeforeItEnds(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	certificate := func() *v1.WorkerNode {
		obj, err := n.cp.Get(ctx, v1.KindWorkerNode.GVK(), "", "vm1")
		require.NoError(t, err)
		return obj.(*v1.WorkerNode)
	}
	first := certificate().Status.Certificate
	require.NotNil(t, first)
	old := nodeCaller(t, n.rig, n.dir)
	run := func(after time.Duration) func() {
		a, err := agent.New(agent.Config{Dir: n.dir, Store: n.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
			Now: func() time.Time { return time.Now().Add(after) }})
		require.NoError(t, err)
		actx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = a.Run(actx); close(done) }()
		return func() { cancel(); <-done }
	}
	stop := run(200 * 24 * time.Hour)
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, first.Serial, certificate().Status.Certificate.Serial, "a certificate in its first two thirds stays")
	stop()
	stop = run(300 * 24 * time.Hour)
	_, err := n.cp.Create(ctx, configMap("e1", "a", "1"))
	require.NoError(t, err)
	onNode(t, n, v1.KindConfigMap, "a", "the node follows on", anyConfigMap)
	eventually(t, "the node uses its new certificate", func() bool {
		c := certificate().Status
		return c.Certificate.Serial != first.Serial && c.RenewedCertificate == nil
	})
	status, _ := old(http.MethodGet, multinode.PathChildren, nil)
	require.Equal(t, http.StatusUnauthorized, status, "the old certificate ends")
	stop()
	defer run(0)()
	_, err = n.cp.Create(ctx, configMap("e1", "b", "1"))
	require.NoError(t, err)
	onNode(t, n, v1.KindConfigMap, "b", "a restarted node uses the new one", anyConfigMap)
}

func TestANodeOnANewerReleaseThanTheControlPlaneIsRefused(t *testing.T) {
	ctx := context.Background()
	r := startCP(t, func(c *multinode.Config) { c.Version = "v0.40.0" })
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	_, err = agent.Join(ctx, r.server(), token, t.TempDir(), "v0.41.0", "linux/arm64")
	require.ErrorContains(t, err, "newer than the control plane's v0.40.0")
	_, err = agent.Join(ctx, r.server(), token, t.TempDir(), "v0.39.2", "linux/arm64")
	require.NoError(t, err, "an older node joins")
}

// fakeCommands records the node commands a node ran.
type fakeCommands struct {
	mu  sync.Mutex
	ran []string
}

func (f *fakeCommands) record(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, s)
	return nil
}

func (f *fakeCommands) Replay(_ context.Context, ns v1.NamespaceName, id string) error {
	return f.record("replay " + string(ns) + "/" + id)
}

func (f *fakeCommands) Retry(_ context.Context, ns v1.NamespaceName, app v1.ObjectName) error {
	return f.record("retry " + string(ns) + "/" + string(app))
}

func (f *fakeCommands) DeleteVolume(_ context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error {
	return f.record("delete volume " + string(ns) + "/" + string(fn) + "/" + string(name))
}

func TestTheNodeCommandsRunOnTheNodeThatRunsTheNamespace(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	letters, f := dlmemory.New(), &fakeCommands{}
	require.NoError(t, letters.Put(ctx, deadletter.DeadLetter{ID: "01JDL", Namespace: "e1", Sensor: "s", Action: "a"}))
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
		Commands: multinode.Commands{DeadLetters: letters, Replayer: f, Apps: f, Volumes: f}})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)

	dl := r.cluster.DeadLetters(nil)
	eventually(t, "the control plane lists e1's dead letters on vm1", func() bool {
		got, err := dl.List(ctx, "e1")
		return err == nil && len(got) == 1 && got[0].ID == "01JDL"
	})
	got, err := dl.Get(ctx, "e1", "01JDL")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("s"), got.Sensor)
	_, err = dl.Get(ctx, "e1", "missing")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "the node's not-found stays a not-found")
	require.NoError(t, dl.Delete(ctx, "e1", "01JDL"))
	left, err := letters.List(ctx, "e1")
	require.NoError(t, err)
	require.Empty(t, left, "the discard ran on the node")
	require.NoError(t, r.cluster.Replayer(nil).Replay(ctx, "e1", "01JDM"))
	require.NoError(t, r.cluster.AppRetrier(nil).Retry(ctx, "e1", "shop"))
	require.NoError(t, r.cluster.Volumes(nil).DeleteVolume(ctx, "e1", "fn", "data"))
	f.mu.Lock()
	require.Equal(t, []string{"replay e1/01JDM", "retry e1/shop", "delete volume e1/fn/data"}, f.ran)
	f.mu.Unlock()

	_, err = dl.List(ctx, "elsewhere")
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a namespace this control plane runs has no dead letters here")
}

func TestANodeChecksTheAPITokensOfTheControlPlaneItself(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	run := func() func() {
		a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
		require.NoError(t, err)
		actx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = a.Run(actx); close(done) }()
		return func() { stop(); <-done }
	}
	stop := run()
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)
	at := &v1.APIToken{TypeMeta: meta(v1.KindAPIToken)}
	at.Name, at.Spec.Role, at.Spec.Namespaces = "ci", "developer", []v1.NamespaceName{"e1"}
	_, err = r.cp.Create(ctx, at)
	require.NoError(t, err)
	secret, _, err := apitoken.NewMinter(r.cp, nil).MintAPIToken(ctx, "ci", time.Hour)
	require.NoError(t, err)
	onNode := apitoken.NewCredentials(r.node, nil)
	eventually(t, "the node checks the token itself", func() bool {
		id, err := onNode.Lookup(ctx, secret)
		return err == nil && id.Role == "developer"
	})
	stop()

	// A token revoked while the node was behind goes at the node's next snapshot.
	require.NoError(t, r.cp.Delete(store.WithoutChangeLog(ctx), v1.KindAPIToken.GVK(), "", "ci", ""))
	cursor := filepath.Join(dir, "cursor")
	b, err := os.ReadFile(cursor)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cursor, []byte("an-older-timeline "+strings.Fields(string(b))[1]+"\n"), 0o600))
	defer run()()
	eventually(t, "the revoked token stops working on the node", func() bool {
		_, err := onNode.Lookup(ctx, secret)
		return fault.KindOf(err) == fault.Unauthorized
	})
}

// dbCheck is a SQLDatabase deletion protection that answers as told.
type dbCheck struct{ refuse bool }

func (dbCheck) Name() string           { return "sqldatabase-deletion-protection" }
func (dbCheck) Phase() admission.Phase { return admission.Validating }
func (dbCheck) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk.Kind == v1.KindSQLDatabase && op == admission.Delete
}

func (d dbCheck) Admit(_ context.Context, req admission.Request) (v1.Object, error) {
	if d.refuse {
		return nil, fault.Conflictf("admission.sqldatabase-deletion-protection", "SQLDatabase %s holds tables", req.Object.GetObjectMeta().Name)
	}
	return req.Object, nil
}

// volCheck is a Function volume booking that answers as told.
type volCheck struct{ refuse bool }

func (volCheck) Name() string           { return "function-volumes" }
func (volCheck) Phase() admission.Phase { return admission.Validating }
func (volCheck) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return (gvk.Kind == v1.KindFunction || gvk.Kind == v1.KindJob) && op == admission.Create
}

func (c volCheck) Admit(_ context.Context, req admission.Request) (v1.Object, error) {
	if c.refuse {
		return nil, fault.Conflictf("admission.function-volumes", "the node has no room for the volume")
	}
	return req.Object, nil
}

func TestTheChecksBeforeAWriteRunOnTheNodeThatHoldsTheState(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
		Commands: multinode.Commands{Admissions: []admission.Admission{dbCheck{refuse: true}, volCheck{refuse: true}}}})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)

	check := r.cluster.Forward([]admission.Admission{dbCheck{}})[0]
	del := func(ns v1.NamespaceName) error {
		db := &v1.SQLDatabase{TypeMeta: meta(v1.KindSQLDatabase)}
		db.Name, db.Namespace = "orders", ns
		_, err := check.Admit(ctx, admission.Request{Operation: admission.Delete, GVK: v1.KindSQLDatabase.GVK(), Object: db})
		return err
	}
	eventually(t, "the node that holds the database refuses its delete", func() bool {
		return fault.KindOf(del("e1")) == fault.Conflict
	})
	require.ErrorContains(t, del("e1"), "holds tables")
	require.NoError(t, del("here"), "a namespace the control plane runs gets its own check")

	book := r.cluster.Forward([]admission.Admission{volCheck{}})[0]
	create := func(volumes ...v1.VolumeEntry) error {
		fn := &v1.Function{TypeMeta: meta(v1.KindFunction)}
		fn.Name, fn.Namespace, fn.Spec.Volumes = "api", "e1", volumes
		_, err := book.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindFunction.GVK(), Object: fn})
		return err
	}
	require.NoError(t, create(), "a Function with no volume needs nothing of the node")
	require.Equal(t, fault.Conflict, fault.KindOf(create(v1.VolumeEntry{Name: "data"})), "one with a volume is booked on the node")
	j := job("report")
	_, err = book.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJob.GVK(), Object: j})
	require.NoError(t, err, "nor does a Job with none")
	j.Spec.Volumes = []v1.VolumeEntry{{Name: "data"}}
	_, err = book.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindJob.GVK(), Object: j})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a Job's volume is booked on the node too")
}

// fakeBackup is a platform backup of that status.
type fakeBackup runner.Status

func (f fakeBackup) Status(context.Context) (runner.Status, error) { return runner.Status(f), nil }

func TestANodeReportsItsBackupInItsWorkerNode(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	last := v1.NewTimestamp(time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC))
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
		Commands: multinode.Commands{BlobStore: "s3://node-blobs", Backup: fakeBackup{LastSuccessTime: &last, RPORisk: true}}})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	eventually(t, "the WorkerNode shows the node's backup", func() bool {
		obj, err := r.cp.Get(ctx, v1.KindWorkerNode.GVK(), "", "vm1")
		if err != nil {
			return false
		}
		b := obj.(*v1.WorkerNode).Status.Backup
		return b != nil && b.RPORisk && b.LastSuccessTime != nil && time.Time(*b.LastSuccessTime).Equal(time.Time(last))
	})
	obj, err := r.cp.Get(ctx, v1.KindWorkerNode.GVK(), "", "vm1")
	require.NoError(t, err)
	require.Equal(t, "s3://node-blobs", obj.(*v1.WorkerNode).Status.BlobStore)
}

func TestTheObjectsANodeMakesReachTheControlPlaneAndNoOthers(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)
	eventually(t, "the node runs e1", func() bool {
		_, err := r.node.Get(ctx, v1.KindNamespace.GVK(), "", "e1")
		return err == nil
	})

	run := &v1.WorkflowRun{TypeMeta: meta(v1.KindWorkflowRun)}
	run.Name, run.Namespace, run.ResourceGroup, run.Spec.Workflow = "fired-1", "e1", "rg1", "orders"
	_, err = r.node.Create(ctx, run)
	require.NoError(t, err)
	eventually(t, "a WorkflowRun the node fired reaches the control plane", func() bool {
		_, err := r.cp.Get(ctx, v1.KindWorkflowRun.GVK(), "e1", "fired-1")
		return err == nil
	})

	// The node API itself refuses a Policy, even one that names an owner.
	pol := &v1.Policy{TypeMeta: meta(v1.KindPolicy)}
	pol.Name, pol.Namespace, pol.ResourceGroup = "grant-all", "e1", "rg1"
	pol.Spec.Cedar = "permit(principal, action, resource);"
	pol.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Name: "api"}, UID: "x"}}
	raw, err := json.Marshal(pol)
	require.NoError(t, err)
	status, _ := nodeCaller(t, r, dir)(http.MethodPut, multinode.PathObjects, multinode.ObjectWrite{Kind: v1.KindPolicy, Object: raw})
	require.Equal(t, http.StatusForbidden, status)
	_, err = r.cp.Get(ctx, v1.KindPolicy.GVK(), "e1", "grant-all")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// nodeCaller calls r's node API with the certificate of the node that joined under dir, and returns the status and the
// body it answers.
//
//nolint:forbidigo // body is one of the node API's typed requests, sent as JSON
func nodeCaller(t *testing.T, r rig, dir string) func(method, path string, body any) (int, []byte) {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "node.pem"), filepath.Join(dir, "node.pem"))
	require.NoError(t, err)
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert},
		RootCAs: roots, MinVersion: tls.VersionTLS13}}}
	return func(method, path string, body any) (int, []byte) {
		t.Helper()
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(context.Background(), method, r.server()+path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set(multinode.HeaderAPIVersion, strconv.Itoa(multinode.APIVersion))
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		answer, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, answer
	}
}

// fakeHold records the hold releases and quarantine returns it ran. Its evidence is held with the paused runs it names, until
// its release.
type fakeHold struct {
	mu       sync.Mutex
	paused   []string
	advanced []string
	released []v1.NamespaceName
	returned []hold.QuarantineReturn
}

func (f *fakeHold) Status(context.Context) (hold.Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return hold.Evidence{Held: f.advanced == nil, PausedRuns: f.paused}, nil
}

func (f *fakeHold) Release(_ context.Context, advance []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.advanced != nil {
		return fault.Conflictf("fakeHold.Release", "the platform is not held")
	}
	f.advanced = append([]string{}, advance...)
	return nil
}

func (f *fakeHold) ReleaseNamespaces(_ context.Context, nss []v1.NamespaceName) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, nss...)
	return nil
}

func (f *fakeHold) ReturnQuarantined(_ context.Context, q hold.QuarantineReturn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.returned = append(f.returned, q)
	return nil
}

// Each side has its own hold and its own backup: a node's are read, and its hold released, by the node's name.
func TestANodesOwnHoldAndBackupAnswerByItsName(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	here := &fakeHold{paused: []string{"default/r1"}}
	nodes := r.cluster.Nodes(here, nil)
	_, err = nodes.HoldStatus(ctx, "vm1")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, "worker node vm1 is offline")
	_, err = nodes.BackupStatus(ctx, "vm2")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)

	onNode := &fakeHold{paused: []string{"e1/r1"}}
	last := v1.NewTimestamp(time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC))
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
		Commands: multinode.Commands{Hold: onNode, Backup: fakeBackup{Enabled: true, LastSuccessTime: &last}}})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	var ev hold.Evidence
	eventually(t, "the node answers for its hold", func() bool {
		ev, err = nodes.HoldStatus(ctx, "vm1")
		return err == nil
	})
	require.True(t, ev.Held)
	require.Equal(t, []string{"e1/r1"}, ev.PausedRuns)
	st, err := nodes.BackupStatus(ctx, "vm1")
	require.NoError(t, err)
	require.True(t, st.Enabled)
	require.True(t, time.Time(last).Equal(time.Time(*st.LastSuccessTime)))

	require.NoError(t, nodes.ReleaseHold(ctx, "vm1", []string{"e1/files"}))
	onNode.mu.Lock()
	require.Equal(t, []string{"e1/files"}, onNode.advanced)
	onNode.mu.Unlock()
	err = nodes.ReleaseHold(ctx, "vm1", nil)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "the node's own answer: %v", err)

	ev, err = nodes.HoldStatus(ctx, "local")
	require.NoError(t, err)
	require.Equal(t, []string{"default/r1"}, ev.PausedRuns, "the control plane's own node is this process")
	st, err = nodes.BackupStatus(ctx, "local")
	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.NoError(t, nodes.ReleaseHold(ctx, "local", nil))
	here.mu.Lock()
	require.NotNil(t, here.advanced)
	here.mu.Unlock()
}

func TestANamespacesHoldIsReleasedOnTheNodeThatRunsIt(t *testing.T) {
	ctx := context.Background()
	r := startCP(t)
	token, _, err := r.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = agent.Join(ctx, r.server(), token, dir, "test", "linux/arm64")
	require.NoError(t, err)
	onNode := &fakeHold{}
	a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64",
		Commands: multinode.Commands{Hold: onNode}})
	require.NoError(t, err)
	actx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { stop(); <-done }()
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = r.cp.Create(ctx, ns)
	require.NoError(t, err)

	onCP := &fakeHold{}
	h := r.cluster.Hold(onCP)
	eventually(t, "the node releases e1's hold", func() bool {
		return h.ReleaseNamespaces(ctx, []v1.NamespaceName{"e1"}) == nil
	})
	require.NoError(t, h.ReleaseNamespaces(ctx, []v1.NamespaceName{"here"}))
	require.NoError(t, h.ReturnQuarantined(ctx, hold.QuarantineReturn{Kind: v1.KindKVStore, Namespace: "e1", Name: "kv1"}))
	onNode.mu.Lock()
	require.Equal(t, []v1.NamespaceName{"e1"}, onNode.released)
	require.Len(t, onNode.returned, 1)
	onNode.mu.Unlock()
	onCP.mu.Lock()
	require.Equal(t, []v1.NamespaceName{"here"}, onCP.released, "a namespace the control plane runs is released here")
	require.Empty(t, onCP.returned)
	onCP.mu.Unlock()
}
