package agent_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
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

func TestANodeTakesASnapshotAfterTheControlPlaneIsRestored(t *testing.T) {
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
	run := func() func() {
		a, err := agent.New(agent.Config{Dir: dir, Store: r.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
		require.NoError(t, err)
		actx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = a.Run(actx); close(done) }()
		return func() { stop(); <-done }
	}
	stop := run()
	eventually(t, "the node follows e1", func() bool {
		_, err := r.node.Get(ctx, v1.KindNamespace.GVK(), "", "e1")
		return err == nil
	})
	stop()

	// A write the change log never holds reaches a node only through a snapshot: one comes when the node's saved
	// revision is of another timeline of the metastore, as after a restore from a backup.
	_, err = r.cp.Create(store.WithoutChangeLog(ctx), configMap("e1", "probe", "restored"))
	require.NoError(t, err)
	cursor := filepath.Join(dir, "cursor")
	b, err := os.ReadFile(cursor)
	require.NoError(t, err)
	f := strings.Fields(string(b))
	require.Len(t, f, 2, "the cursor names its timeline and revision")
	require.NoError(t, os.WriteFile(cursor, []byte("an-older-timeline "+f[1]+"\n"), 0o600))
	defer run()()
	eventually(t, "the node takes a new snapshot", func() bool {
		obj, err := r.node.Get(ctx, v1.KindConfigMap.GVK(), "e1", "probe")
		return err == nil && obj.(*v1.ConfigMap).Spec.Data["n"] == "restored"
	})
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
