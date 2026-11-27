package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/store"
)

// joinedNode is a worker node vm1 of r, with the namespace e1 placed on it, whose agent a test stops and starts again.
type joinedNode struct {
	rig
	dir   string
	agent *agent.Agent
}

func joinNode(t *testing.T, opts ...func(*multinode.Config)) *joinedNode {
	t.Helper()
	ctx := context.Background()
	n := &joinedNode{rig: startCP(t, opts...), dir: t.TempDir()}
	token, _, err := n.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	_, err = agent.Join(ctx, longpoll.NewConnector(), n.server(), token, n.dir, "test", "linux/arm64")
	require.NoError(t, err)
	ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "e1", "vm1"
	_, err = n.cp.Create(ctx, ns)
	require.NoError(t, err)
	return n
}

// start runs the node's agent until the func it returns is called, and waits until the node follows e1.
func (n *joinedNode) start(t *testing.T) func() {
	t.Helper()
	return n.startVia(t, longpoll.NewConnector())
}

// startVia is start with the node side of the node API that conn gives.
func (n *joinedNode) startVia(t *testing.T, conn nodeapi.Connector) func() {
	t.Helper()
	a, err := agent.New(agent.Config{Connector: conn, Dir: n.dir, Store: n.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
	require.NoError(t, err)
	n.agent = a
	actx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	eventually(t, "the node follows e1", func() bool {
		_, err := n.node.Get(actx, v1.KindNamespace.GVK(), "", "e1")
		return err == nil
	})
	return func() { cancel(); <-done }
}

// forgetTimeline makes the stopped node take a whole snapshot at its next start, as after the control plane's restore.
func (n *joinedNode) forgetTimeline(t *testing.T) {
	t.Helper()
	cursor := filepath.Join(n.dir, "cursor")
	b, err := os.ReadFile(cursor)
	require.NoError(t, err)
	f := strings.Fields(string(b))
	require.Len(t, f, 3, "the cursor names its timeline, its revision and the store's timeline")
	require.NoError(t, os.WriteFile(cursor, []byte("restored 0 "+f[2]+"\n"), 0o600))
}

// onNode waits until the node holds kind name of e1 as cond wants it, and returns it.
func onNode[T v1.Object](t *testing.T, n *joinedNode, kind v1.Kind, name v1.ObjectName, what string, cond func(T) bool) T {
	t.Helper()
	return held(t, n.node, kind, name, what, cond)
}

// onCP waits until the control plane holds kind name of e1 as cond wants it, and returns it.
func onCP[T v1.Object](t *testing.T, n *joinedNode, kind v1.Kind, name v1.ObjectName, what string, cond func(T) bool) T {
	t.Helper()
	return held(t, n.cp, kind, name, what, cond)
}

func held[T v1.Object](t *testing.T, st store.Store, kind v1.Kind, name v1.ObjectName, what string, cond func(T) bool) T {
	t.Helper()
	ns := v1.NamespaceName("e1")
	if !kind.Namespaced() {
		ns = ""
	}
	var out T
	eventually(t, what, func() bool {
		obj, err := st.Get(context.Background(), kind.GVK(), ns, name)
		if err != nil {
			return false
		}
		out = obj.(T)
		return cond(out)
	})
	return out
}

func gone(t *testing.T, st store.Store, kind v1.Kind, name v1.ObjectName, what string) {
	t.Helper()
	ns := v1.NamespaceName("e1")
	if !kind.Namespaced() {
		ns = ""
	}
	eventually(t, what, func() bool {
		_, err := st.Get(context.Background(), kind.GVK(), ns, name)
		return fault.KindOf(err) == fault.NotFound
	})
}

// update writes the stored kind name of e1 as edit changes it, again when another write came first.
func update[T v1.Object](t *testing.T, st store.Store, kind v1.Kind, name v1.ObjectName, edit func(T)) T {
	t.Helper()
	ns := v1.NamespaceName("e1")
	if !kind.Namespaced() {
		ns = ""
	}
	var out T
	eventually(t, "write "+string(kind)+" "+string(name), func() bool {
		obj, err := st.Get(context.Background(), kind.GVK(), ns, name)
		if err != nil {
			return false
		}
		edit(obj.(T))
		obj, err = st.Update(context.Background(), obj)
		if err != nil {
			return false
		}
		out = obj.(T)
		return true
	})
	return out
}

func anyJob(*v1.Job) bool { return true }

func job(name v1.ObjectName) *v1.Job {
	j := &v1.Job{TypeMeta: meta(v1.KindJob)}
	j.Name, j.Namespace, j.ResourceGroup = name, "e1", "rg1"
	j.Spec.Runtime, j.Spec.Handler, j.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
	return j
}

func jobRun(name, of v1.ObjectName) *v1.JobRun {
	r := &v1.JobRun{TypeMeta: meta(v1.KindJobRun)}
	r.Name, r.Namespace, r.ResourceGroup, r.Spec.Job = name, "e1", "rg1", of
	return r
}

func TestANodesCopyIsTheControlPlanesObject(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	_, err := n.cp.Create(ctx, job("report"))
	require.NoError(t, err)
	onControlPlane := update(t, n.cp, v1.KindJob, "report", func(j *v1.Job) { j.Spec.Handler = "main" })
	require.Equal(t, int64(2), onControlPlane.Generation)
	stop := n.start(t)
	run := jobRun("report-1", "report")
	run.Status.JobUID, run.Status.StartedBy = onControlPlane.UID, "ada" // the API stamps both (ADR-0347 Decision 3)
	_, err = n.cp.Create(ctx, run)
	require.NoError(t, err)

	copied := onNode(t, n, v1.KindJob, "report", "the Job reaches the node", anyJob)
	require.Equal(t, onControlPlane.UID, copied.UID)
	require.Equal(t, onControlPlane.Generation, copied.Generation)
	require.Equal(t, onControlPlane.CreationTime, copied.CreationTime)
	got := onNode(t, n, v1.KindJobRun, "report-1", "the run reaches the node", func(*v1.JobRun) bool { return true })
	require.Equal(t, copied.UID, got.Status.JobUID, "the node's Job is the one the run was started for")
	require.Equal(t, "ada", got.Status.StartedBy)

	update(t, n.node, v1.KindJob, "report", func(j *v1.Job) { j.Status.ObservedGeneration = j.Generation })
	reported := onCP(t, n, v1.KindJob, "report", "the node's status comes back", func(j *v1.Job) bool { return j.Status.ObservedGeneration != 0 })
	require.Equal(t, reported.Generation, reported.Status.ObservedGeneration, "the generation it observed is the control plane's")

	// The child a node owns is the same object on both sides too.
	child := configMap("e1", "child", "1")
	child.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindJob, Namespace: "e1", Name: "report"}, UID: copied.UID}}
	made, err := n.node.Create(ctx, child)
	require.NoError(t, err)
	onCP(t, n, v1.KindConfigMap, "child", "the child reaches the control plane", func(c *v1.ConfigMap) bool {
		return c.UID == made.GetObjectMeta().UID
	})

	// One created again under its name while the node was away replaces the node's, which goes with what it held.
	kv := &v1.KVStore{TypeMeta: meta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = "kv1", "e1", "rg1"
	first, err := n.cp.Create(ctx, kv)
	require.NoError(t, err)
	onNode(t, n, v1.KindKVStore, "kv1", "the KVStore reaches the node", func(*v1.KVStore) bool { return true })
	update(t, n.node, v1.KindKVStore, "kv1", func(k *v1.KVStore) { k.Status.Phase = v1.PhaseReady })
	onCP(t, n, v1.KindKVStore, "kv1", "its status comes back", func(k *v1.KVStore) bool { return k.Status.Phase == v1.PhaseReady })
	w, err := n.node.Watch(ctx, v1.KindKVStore.GVK(), store.WatchOptions{})
	require.NoError(t, err)
	defer w.Stop()
	stop()
	quiet := store.WithoutChangeLog(ctx)
	require.NoError(t, n.cp.Delete(quiet, v1.KindKVStore.GVK(), "e1", "kv1", ""))
	second, err := n.cp.Create(quiet, kv)
	require.NoError(t, err)
	require.NotEqual(t, first.GetObjectMeta().UID, second.GetObjectMeta().UID)
	n.forgetTimeline(t)
	defer n.start(t)()
	again := onNode(t, n, v1.KindKVStore, "kv1", "the new KVStore replaces the node's", func(k *v1.KVStore) bool {
		return k.UID == second.GetObjectMeta().UID
	})
	require.Empty(t, again.Status.Phase, "the status was the old one's")
	var types []store.EventType
	for len(types) < 3 { // the watch opens with the first KVStore
		select {
		case ev := <-w.ResultChan():
			types = append(types, ev.Type)
		case <-time.After(5 * time.Second):
			require.Fail(t, "no event", "%v", types)
		}
	}
	require.Equal(t, []store.EventType{store.Added, store.Deleted, store.Added}, types, "the node sees a delete, so it drops the old one's data")
}

// A control plane restored to a point before the node's last reports shows them again, though nothing changed on the node
// since.
func TestANodeReportsEverythingAgainAfterASnapshot(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	defer n.start(t)()
	kv := &v1.KVStore{TypeMeta: meta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = "kv1", "e1", "rg1"
	_, err := n.cp.Create(ctx, kv)
	require.NoError(t, err)
	onNode(t, n, v1.KindKVStore, "kv1", "the KVStore reaches the node", func(*v1.KVStore) bool { return true })
	update(t, n.node, v1.KindKVStore, "kv1", func(k *v1.KVStore) { k.Status.Phase = v1.PhaseReady })
	child := configMap("e1", "child", "1")
	child.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindKVStore, Name: "kv1"}, UID: "u"}}
	_, err = n.node.Create(ctx, child)
	require.NoError(t, err)
	ready := func(k *v1.KVStore) bool { return k.Status.Phase == v1.PhaseReady }
	onCP(t, n, v1.KindKVStore, "kv1", "its status comes back", ready)
	onCP(t, n, v1.KindConfigMap, "child", "so does the child", func(*v1.ConfigMap) bool { return true })

	quiet := store.WithoutChangeLog(ctx) // as a restore from a backup: nothing of it reaches the node as a change
	obj, err := n.cp.Get(ctx, v1.KindKVStore.GVK(), "e1", "kv1")
	require.NoError(t, err)
	old := obj.(*v1.KVStore)
	old.Status = v1.KVStoreStatus{}
	_, err = n.cp.Update(quiet, old)
	require.NoError(t, err)
	require.NoError(t, n.cp.Delete(quiet, v1.KindConfigMap.GVK(), "e1", "child", ""))
	n.agent.ResyncForTest()
	_, err = n.cp.Create(ctx, configMap("e1", "next", "1")) // which ends the poll
	require.NoError(t, err)
	onCP(t, n, v1.KindKVStore, "kv1", "the status comes back again", ready)
	onCP(t, n, v1.KindConfigMap, "child", "and the child", func(*v1.ConfigMap) bool { return true })
}

func TestAPersonsDecisionOnARunReachesItsNode(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	stop := n.start(t)
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	for _, name := range []v1.ObjectName{"approved", "away", "rejected"} {
		_, err := n.cp.Create(ctx, jobRun(name, "deploy"))
		require.NoError(t, err)
		onNode(t, n, v1.KindJobRun, name, "the run reaches the node", func(*v1.JobRun) bool { return true })
		update(t, n.node, v1.KindJobRun, name, func(r *v1.JobRun) {
			r.Status.Phase = v1.JobRunAwaitingApproval
			r.Status.Preview = &v1.JobRunPreview{Digest: digest, Document: []byte(`{}`), PreviewedAt: v1.NewTimestamp(time.Now())}
		})
		onCP(t, n, v1.KindJobRun, name, "its preview comes back", func(r *v1.JobRun) bool { return r.Status.Preview != nil })
	}
	// What the API server writes on an update it admitted (ADR-0348 Decisions 3 and 5).
	approve := func(r *v1.JobRun) {
		r.Spec.Approve, r.Status.ApprovedAt, r.Status.ApprovedBy = digest, v1.NewTimestamp(time.Now()), "ada"
	}
	update(t, n.cp, v1.KindJobRun, "approved", approve)
	update(t, n.cp, v1.KindJobRun, "rejected", func(r *v1.JobRun) { r.Spec.Reject, r.Status.RejectedBy = true, "bob" })
	got := onNode(t, n, v1.KindJobRun, "approved", "the approval reaches the node", (*v1.JobRun).Approved)
	require.Equal(t, "ada", got.Status.ApprovedBy)
	require.Equal(t, v1.JobRunAwaitingApproval, got.Status.Phase, "the phase is the node's")
	onNode(t, n, v1.KindJobRun, "rejected", "the rejection reaches the node", func(r *v1.JobRun) bool {
		return r.Spec.Reject && r.Status.RejectedBy == "bob"
	})

	// A node that was away reports the status it had first; the approval is not lost to it.
	stop()
	update(t, n.cp, v1.KindJobRun, "away", approve)
	defer n.start(t)()
	onNode(t, n, v1.KindJobRun, "away", "the approval made while it was away reaches the node", (*v1.JobRun).Approved)
	update(t, n.node, v1.KindJobRun, "away", func(r *v1.JobRun) { r.Status.Phase = v1.JobRunRunning })
	after := onCP(t, n, v1.KindJobRun, "away", "the run goes on", func(r *v1.JobRun) bool { return r.Status.Phase == v1.JobRunRunning })
	require.True(t, after.Approved(), "and the control plane still shows who approved it")
	require.Equal(t, "ada", after.Status.ApprovedBy)
}

func TestARunThatANodeStartsReachesTheControlPlane(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	defer n.start(t)()
	started := jobRun("report-x1", "report")
	started.GenerateName = "report-"
	started.Status.StartedBy = `Function::"e1/planner"`
	made, err := n.node.Create(ctx, started)
	require.NoError(t, err)
	run := onCP(t, n, v1.KindJobRun, "report-x1", "the run reaches the control plane", func(*v1.JobRun) bool { return true })
	require.Equal(t, made.GetObjectMeta().UID, run.UID)
	require.Equal(t, `Function::"e1/planner"`, run.Status.StartedBy)

	update(t, n.cp, v1.KindJobRun, "report-x1", func(r *v1.JobRun) { r.Spec.Cancel = true })
	onNode(t, n, v1.KindJobRun, "report-x1", "a person's cancel reaches the node", func(r *v1.JobRun) bool { return r.Spec.Cancel })
	update(t, n.node, v1.KindJobRun, "report-x1", func(r *v1.JobRun) {
		r.Spec.Cancel = false // a node changes no spec a person wrote
		r.Status.End(v1.JobRunCancelled, v1.JobRunCancelledReason, "cancelled", v1.NewTimestamp(time.Now()))
	})
	ended := onCP(t, n, v1.KindJobRun, "report-x1", "its end comes back", func(r *v1.JobRun) bool { return r.Status.Phase.Ended() })
	require.True(t, ended.Spec.Cancel)
	require.NoError(t, n.node.Delete(ctx, v1.KindJobRun.GVK(), "e1", "report-x1", ""))
	gone(t, n.cp, v1.KindJobRun, "report-x1", "the node's retention sweep reaches the control plane")

	// A name another run holds on the control plane is that run's: the node's own goes when that run reaches the node.
	_, err = n.cp.Create(store.WithoutChangeLog(ctx), jobRun("taken", "report"))
	require.NoError(t, err)
	mine, err := n.node.Create(ctx, jobRun("taken", "report"))
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	theirs := onCP(t, n, v1.KindJobRun, "taken", "the control plane keeps its run", func(*v1.JobRun) bool { return true })
	require.NotEqual(t, mine.GetObjectMeta().UID, theirs.UID)
	update(t, n.cp, v1.KindJobRun, "taken", func(r *v1.JobRun) { r.Tags = v1.Tags{"by": "ada"} })
	onNode(t, n, v1.KindJobRun, "taken", "the node takes the control plane's run", func(r *v1.JobRun) bool { return r.UID == theirs.UID })

	// Deleted and created again on the control plane: the node's delete of the first one takes no second one along.
	require.NoError(t, n.cp.Delete(ctx, v1.KindJobRun.GVK(), "e1", "taken", ""))
	again, err := n.cp.Create(ctx, jobRun("taken", "report"))
	require.NoError(t, err)
	onNode(t, n, v1.KindJobRun, "taken", "the second one reaches the node", func(r *v1.JobRun) bool {
		return r.UID == again.GetObjectMeta().UID
	})
	time.Sleep(200 * time.Millisecond)
	onCP(t, n, v1.KindJobRun, "taken", "and stays on the control plane", func(r *v1.JobRun) bool { return r.UID == again.GetObjectMeta().UID })
}

// What a node sends late, or of an object the control plane no longer holds, changes nothing of the one it holds now.
func TestANodesLateWriteChangesNothingOfAnotherObject(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	client := nodeClient(t, n.rig, n.dir)
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	run := jobRun("deploy-1", "deploy")
	run.Spec.Approve, run.Status.Phase = digest, v1.JobRunAwaitingApproval
	run.Status.Preview = &v1.JobRunPreview{Digest: digest, Document: []byte(`{}`)}
	run.Status.ApprovedAt, run.Status.ApprovedBy, run.Status.StartedBy = v1.NewTimestamp(time.Now()), "ada", "ada"
	_, err := n.cp.Create(ctx, run)
	require.NoError(t, err)
	get := func() *v1.JobRun {
		obj, err := n.cp.Get(ctx, v1.KindJobRun.GVK(), "e1", "deploy-1")
		require.NoError(t, err)
		return obj.(*v1.JobRun)
	}
	status := func(uid v1.UID, s string) error {
		return client.Status(ctx, nodeapi.StatusWrite{Kind: v1.KindJobRun, Namespace: "e1", Name: "deploy-1", UID: uid, Status: []byte(s)})
	}
	mine := get().UID
	require.NoError(t, status(mine, `{"phase":"AwaitingApproval","preview":{"digest":"`+digest+`","document":{},"previewedAt":"2026-11-24T00:00:00.000Z"}}`))
	require.True(t, get().Approved(), "a status reported before the node saw the approval keeps it")
	require.Equal(t, []string{"ada", "ada"}, []string{get().Status.ApprovedBy, get().Status.StartedBy})
	require.Equal(t, fault.Conflict, fault.KindOf(status("of-another-run", `{"phase":"Failed"}`)))
	require.Equal(t, v1.JobRunAwaitingApproval, get().Status.Phase, "a status of another run of the name")
	require.NoError(t, status(mine, `{"phase":"AwaitingApproval","preview":{"digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","document":{},"previewedAt":"2026-11-24T00:00:00.000Z"}}`))
	require.Empty(t, get().Status.ApprovedBy, "an approval is of one preview")

	other := jobRun("deploy-1", "deploy")
	other.UID, other.Status.Phase = "of-another-run", v1.JobRunRunning
	raw, err := json.Marshal(other)
	require.NoError(t, err)
	require.Equal(t, fault.Conflict, fault.KindOf(client.PutObject(ctx, nodeapi.ObjectWrite{Kind: v1.KindJobRun, Object: raw})))
	require.Equal(t, v1.JobRunAwaitingApproval, get().Status.Phase, "a run the node made under a taken name")
	ref := nodeapi.ObjectRef{Kind: v1.KindJobRun, Namespace: "e1", Name: "deploy-1"}
	require.Equal(t, fault.Invalid, fault.KindOf(client.DeleteObject(ctx, ref)), "a delete names the object's uid")
	ref.UID = "of-another-run"
	require.NoError(t, client.DeleteObject(ctx, ref))
	require.Equal(t, mine, get().UID, "the object a delete named was another one")
	ref.UID = mine
	require.NoError(t, client.DeleteObject(ctx, ref))
	_, err = n.cp.Get(ctx, v1.KindJobRun.GVK(), "e1", "deploy-1")
	require.Equal(t, fault.NotFound, fault.KindOf(err))

	_, err = n.cp.Create(ctx, stack("net"))
	require.NoError(t, err)
	obj, err := n.cp.Get(ctx, v1.KindAddonObject.GVK(), "e1", "net")
	require.NoError(t, err)
	ref = nodeapi.ObjectRef{Kind: v1.KindAddonObject, Namespace: "e1", Name: "net", UID: obj.GetObjectMeta().UID}
	require.Equal(t, fault.Forbidden, fault.KindOf(client.DeleteObject(ctx, ref)), "an object a person wrote and did not delete")
}

func TestANodeGetsTheAPITokensAndTheNamespacesThatConcernIt(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	token := func(name v1.ObjectName, role string, spans ...v1.NamespaceName) {
		at := &v1.APIToken{TypeMeta: meta(v1.KindAPIToken)}
		at.Name, at.Spec.Role, at.Spec.Namespaces = name, role, spans
		at.Status.TokenHash = "hash-of-" + string(name)
		_, err := n.cp.Create(ctx, at)
		require.NoError(t, err)
	}
	namespace := func(name, node v1.ObjectName) {
		ns := &v1.Namespace{TypeMeta: meta(v1.KindNamespace)}
		ns.Name, ns.Spec.Node = name, node
		_, err := n.cp.Create(ctx, ns)
		require.NoError(t, err)
	}
	token("mine", "developer", "e1", "far")
	token("theirs", "viewer", "far")
	token("root", "admin")
	namespace("far", "")
	defer n.start(t)()
	anyToken := func(*v1.APIToken) bool { return true }
	lacks := func(name v1.ObjectName) {
		_, err := n.node.Get(ctx, v1.KindAPIToken.GVK(), "", name)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s", name)
	}
	onNode(t, n, v1.KindAPIToken, "mine", "a token that spans e1 is on the node", anyToken)
	onNode(t, n, v1.KindAPIToken, "root", "and an admin's, which spans every namespace", anyToken)
	lacks("theirs")

	// The feed sends the node nothing of the rest but a name.
	feed, err := nodeClient(t, n.rig, n.dir).Feed(ctx, 0, n.cp.(store.ChangeLog).Timeline(), time.Millisecond)
	require.NoError(t, err)
	body, err := json.Marshal(feed)
	require.NoError(t, err)
	require.Contains(t, string(body), "hash-of-mine")
	require.Contains(t, string(body), `"theirs"`)
	require.NotContains(t, string(body), "hash-of-theirs")
	for _, c := range feed.Changes {
		if c.Name == "far" || c.Name == "theirs" {
			require.Equal(t, store.Deleted, c.Type, "%s", c.Name)
			require.Empty(t, c.Object, "%s", c.Name)
		}
	}

	update(t, n.cp, v1.KindAPIToken, "mine", func(a *v1.APIToken) { a.Spec.Namespaces = []v1.NamespaceName{"far"} })
	gone(t, n.node, v1.KindAPIToken, "mine", "a token that spans none of the node's namespaces any more leaves it")
	token("later", "developer", "e2")
	namespace("e2", "vm1")
	onNode(t, n, v1.KindAPIToken, "later", "a token comes with the namespace it spans", anyToken)
	require.NoError(t, n.cp.Delete(ctx, v1.KindNamespace.GVK(), "", "e2", ""))
	gone(t, n.node, v1.KindAPIToken, "later", "and leaves with it")
	onNode(t, n, v1.KindAPIToken, "root", "the admin's stays", anyToken)
	lacks("theirs")
}

func stack(name v1.ObjectName) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: meta(v1.KindAddonObject)}
	o.Name, o.Namespace, o.ResourceGroup = name, "e1", "rg1"
	o.Spec.Kind, o.Spec.Deletion = "InfraStack", v1.DeletionDelete
	return o
}

func TestAnEnginelessObjectStaysUntilItsNodeDestroyedWhatItHolds(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	defer n.start(t)()
	anyObject := func(*v1.AddonObject) bool { return true }
	mark := func(name v1.ObjectName) {
		onNode(t, n, v1.KindAddonObject, name, "the object reaches the node", anyObject)
		update(t, n.node, v1.KindAddonObject, name, func(o *v1.AddonObject) { o.Status.Workload = v1.WorkloadEngineless })
	}
	_, err := n.cp.Create(ctx, stack("net"))
	require.NoError(t, err)
	mark("net")
	o := onCP(t, n, v1.KindAddonObject, "net", "its mark comes back", func(o *v1.AddonObject) bool { return o.Status.Workload == v1.WorkloadEngineless })
	held, err := gc.DeleteAddonObject(ctx, n.cp, o) // the API's DELETE holds it (ADR-0345 Decision 6)
	require.NoError(t, err)
	require.NotNil(t, held)
	onNode(t, n, v1.KindAddonObject, "net", "the node sees it deleting", func(o *v1.AddonObject) bool { return o.DeletionTime != nil })
	require.NoError(t, n.node.Delete(ctx, v1.KindAddonObject.GVK(), "e1", "net", "")) // its Function answered ok
	gone(t, n.cp, v1.KindAddonObject, "net", "the end of the deletion reaches the control plane")

	// A delete that the control plane made before it saw the mark: the node still holds its copy until then.
	_, err = n.cp.Create(ctx, stack("dns"))
	require.NoError(t, err)
	mark("dns")
	require.NoError(t, n.cp.Delete(ctx, v1.KindAddonObject.GVK(), "e1", "dns", ""))
	onNode(t, n, v1.KindAddonObject, "dns", "the node holds its copy", func(o *v1.AddonObject) bool { return o.DeletionTime != nil })
	require.NoError(t, n.node.Delete(ctx, v1.KindAddonObject.GVK(), "e1", "dns", ""))
	gone(t, n.node, v1.KindAddonObject, "dns", "and lets it go")

	// Any other add-on object goes at once.
	_, err = n.cp.Create(ctx, stack("plain"))
	require.NoError(t, err)
	onNode(t, n, v1.KindAddonObject, "plain", "the object reaches the node", anyObject)
	require.NoError(t, n.cp.Delete(ctx, v1.KindAddonObject.GVK(), "e1", "plain", ""))
	gone(t, n.node, v1.KindAddonObject, "plain", "an object with no mark goes at once")
}

func TestANodeFollowsTheAddonsAndTheirRevisions(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	spec := v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider",
		Image:     "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809",
		Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080,
			Readiness: v1.AddonReadiness{Path: "/ping"}}}}}
	ad := &v1.Addon{TypeMeta: meta(v1.KindAddon), Spec: spec}
	ad.Name = "rauthy"
	created, err := n.cp.Create(ctx, ad)
	require.NoError(t, err)
	revision := func(number int64) {
		rev := &v1.AddonRevision{TypeMeta: meta(v1.KindAddonRevision), Spec: v1.AddonRevisionSpec{Addon: "rauthy", Number: number, Spec: spec}}
		rev.Name = v1.AddonRevisionName("rauthy", number)
		rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindAddon, Name: "rauthy"},
			UID: created.GetObjectMeta().UID, Controller: true}}
		_, err := n.cp.Create(ctx, rev)
		require.NoError(t, err)
		update(t, n.cp, v1.KindAddon, "rauthy", func(a *v1.Addon) { a.Status.CurrentRevision = rev.Name })
	}
	revision(1)
	stop := n.start(t)
	onNode(t, n, v1.KindAddon, "rauthy", "the Addon reaches the node with its revision", func(a *v1.Addon) bool {
		return a.Status.CurrentRevision == "rauthy-1" && a.UID == created.GetObjectMeta().UID
	})
	copied := onNode(t, n, v1.KindAddonRevision, "rauthy-1", "so does the revision", func(*v1.AddonRevision) bool { return true })
	require.Empty(t, copied.OwnerReferences, "the control plane's collector decides when it goes, not the node's")

	revision(2)
	onNode(t, n, v1.KindAddon, "rauthy", "the next revision is current on the node", func(a *v1.Addon) bool {
		return a.Status.CurrentRevision == "rauthy-2"
	})
	require.NoError(t, n.cp.Delete(ctx, v1.KindAddonRevision.GVK(), "", "rauthy-1", ""))
	onNode(t, n, v1.KindAddonRevision, "rauthy-1", "a retired revision is marked, for the node's add-ons to remove", func(r *v1.AddonRevision) bool {
		return r.DeletionTime != nil
	})

	stop()
	quiet := store.WithoutChangeLog(ctx)
	require.NoError(t, n.cp.Delete(quiet, v1.KindAddonRevision.GVK(), "", "rauthy-2", ""))
	require.NoError(t, n.cp.Delete(quiet, v1.KindAddon.GVK(), "", "rauthy", ""))
	n.forgetTimeline(t)
	defer n.start(t)()
	gone(t, n.node, v1.KindAddon, "rauthy", "an Addon removed while the node was away goes")
	onNode(t, n, v1.KindAddonRevision, "rauthy-2", "and its revision is marked", func(r *v1.AddonRevision) bool { return r.DeletionTime != nil })
}
