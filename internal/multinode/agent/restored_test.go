package agent_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// cut is a backup of the node's store, without its Secrets (F251).
func (n *joinedNode) cut(t *testing.T) []snapshot.Record {
	t.Helper()
	var recs []snapshot.Record
	_, err := store.WithoutSecrets(n.node).Snapshot(context.Background(), func(r snapshot.Record) error {
		recs = append(recs, snapshot.Record{Key: bytes.Clone(r.Key), Value: bytes.Clone(r.Value)})
		return nil
	})
	require.NoError(t, err)
	return recs
}

// replaceStore gives the stopped node a new store, loaded with recs: a new machine's, or one restored from a backup.
func (n *joinedNode) replaceStore(t *testing.T, recs []snapshot.Record) {
	t.Helper()
	eng := memory.New()
	if len(recs) > 0 {
		require.NoError(t, eng.Load(context.Background(), snapshotcontract.Feed(recs)))
	}
	n.node = store.New(eng)
}

// prepare is the node's first step at a start, before its platform runs anything.
func (n *joinedNode) prepare(t *testing.T) {
	t.Helper()
	a, err := agent.New(agent.Config{Connector: longpoll.NewConnector(), Dir: n.dir, Store: n.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, a.Prepare(ctx))
	require.NoError(t, ctx.Err())
}

// owned makes obj a child of the Function fn.
func owned[T v1.Object](obj T, fn v1.Object) T {
	m := fn.GetObjectMeta()
	obj.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{UID: m.UID,
		ObjectRef: v1.ObjectRef{Kind: fn.GroupVersionKind().Kind, Namespace: m.Namespace, Name: m.Name}}}
	return obj
}

func secretObj(name v1.ObjectName, value string) *v1.Secret {
	s := &v1.Secret{TypeMeta: meta(v1.KindSecret)}
	s.Name, s.Namespace, s.ResourceGroup = name, "e1", "rg1"
	s.Spec.Data = map[string][]byte{"token": []byte(value)}
	return s
}

func anyConfigMap(*v1.ConfigMap) bool { return true }

// A node that starts with another store than the one its saved revision is of follows nothing from that revision: its
// store is new, or restored to an older point, so it takes its namespaces whole, with the children it made.
// oracle: docs/design/multinode/restore.fizz#NothingRunsBeforeTheTakeWhole
func TestANodeWithANewStoreTakesItsNamespacesAgain(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	owner, err := n.cp.Create(ctx, job("report"))
	require.NoError(t, err)
	_, err = n.cp.Create(ctx, configMap("e1", "settings", "1"))
	require.NoError(t, err)
	stop := n.start(t)
	onNode(t, n, v1.KindJob, "report", "the Job reaches the node", anyJob)
	onNode(t, n, v1.KindConfigMap, "settings", "so does the ConfigMap", anyConfigMap)
	kept, err := n.node.Create(ctx, owned(configMap("e1", "kept", "1"), owner))
	require.NoError(t, err)
	onCP(t, n, v1.KindConfigMap, "kept", "the child reaches the control plane", anyConfigMap)
	backup := n.cut(t)
	later, err := n.node.Create(ctx, owned(configMap("e1", "later", "1"), owner))
	require.NoError(t, err)
	cred, err := n.node.Create(ctx, owned(secretObj("credential", "s3cr3t"), owner))
	require.NoError(t, err)
	onCP(t, n, v1.KindConfigMap, "later", "a child made since the backup reaches the control plane", anyConfigMap)
	onCP(t, n, v1.KindSecret, "credential", "so does a credential's Secret", func(*v1.Secret) bool { return true })
	update(t, n.cp, v1.KindConfigMap, "settings", func(c *v1.ConfigMap) { c.Spec.Data["n"] = "2" })
	onNode(t, n, v1.KindConfigMap, "settings", "the change reaches the node", func(c *v1.ConfigMap) bool { return c.Spec.Data["n"] == "2" })
	time.Sleep(100 * time.Millisecond) // the node keeps the revision after that change
	stop()

	for what, recs := range map[string][]snapshot.Record{"restored from a backup": backup, "new": nil} {
		n.replaceStore(t, recs)
		n.prepare(t)
		get := func(kind v1.Kind, name v1.ObjectName) v1.Object {
			obj, err := n.node.Get(ctx, kind.GVK(), "e1", name)
			require.NoError(t, err, "a store that is %s holds %s %s", what, kind, name)
			return obj
		}
		require.Equal(t, "2", get(v1.KindConfigMap, "settings").(*v1.ConfigMap).Spec.Data["n"], what)
		require.Equal(t, owner.GetObjectMeta().UID, get(v1.KindJob, "report").GetObjectMeta().UID, what)
		require.Equal(t, kept.GetObjectMeta().UID, get(v1.KindConfigMap, "kept").GetObjectMeta().UID, what)
		require.Equal(t, later.GetObjectMeta().UID, get(v1.KindConfigMap, "later").GetObjectMeta().UID, what)
		secret := get(v1.KindSecret, "credential").(*v1.Secret)
		require.Equal(t, cred.GetObjectMeta().UID, secret.UID, what)
		require.Equal(t, "s3cr3t", string(secret.Spec.Data["token"]), "a credential outlives its node's machine")
	}

	// The node then goes on from that snapshot, and none of it changed on the control plane.
	defer n.start(t)()
	update(t, n.cp, v1.KindConfigMap, "settings", func(c *v1.ConfigMap) { c.Spec.Data["n"] = "3" })
	onNode(t, n, v1.KindConfigMap, "settings", "a later change reaches the node", func(c *v1.ConfigMap) bool { return c.Spec.Data["n"] == "3" })
	time.Sleep(200 * time.Millisecond)
	onCP(t, n, v1.KindConfigMap, "later", "the children stay on the control plane", func(c *v1.ConfigMap) bool {
		return c.UID == later.GetObjectMeta().UID
	})
}

// The control plane's children of a node's namespaces are what the node holds: one it deleted while the control plane was
// away, and whose delete its restart forgot, goes at its next start.
func TestAChildTheNodeNoLongerHoldsGoesFromTheControlPlane(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	owner, err := n.cp.Create(ctx, job("report"))
	require.NoError(t, err)
	stop := n.start(t)
	onNode(t, n, v1.KindJob, "report", "the Job reaches the node", anyJob)
	for _, name := range []string{"old", "current"} {
		_, err = n.node.Create(ctx, owned(configMap("e1", name, "1"), owner))
		require.NoError(t, err)
		onCP(t, n, v1.KindConfigMap, v1.ObjectName(name), "the child reaches the control plane", anyConfigMap)
	}
	stop()
	require.NoError(t, n.node.Delete(ctx, v1.KindConfigMap.GVK(), "e1", "old", ""))
	_, err = n.cp.Create(ctx, configMap("e1", "persons", "1"))
	require.NoError(t, err)
	defer n.start(t)()
	gone(t, n.cp, v1.KindConfigMap, "old", "the deleted child goes from the control plane")
	onCP(t, n, v1.KindConfigMap, "current", "the other stays", anyConfigMap)
	onNode(t, n, v1.KindConfigMap, "persons", "and a person's object reaches the node", anyConfigMap)
}

func workflowRun(name v1.ObjectName) *v1.WorkflowRun {
	r := &v1.WorkflowRun{TypeMeta: meta(v1.KindWorkflowRun)}
	r.Name, r.Namespace, r.ResourceGroup, r.Spec.Workflow = name, "e1", "rg1", "wf"
	return r
}

// A run that the restore of its node paused (ADR-0206 Decision 7) stays paused on both sides until a person resumes it.
// oracle: docs/design/multinode/restore.fizz#ThePauseStaysOnTheNode
// oracle: docs/design/multinode/restore.fizz#ThePauseShows
func TestARunTheRestoreOfItsNodePausedWaitsForAPerson(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	_, err := n.cp.Create(ctx, workflowRun("r1"))
	require.NoError(t, err)
	stop := n.start(t)
	onNode(t, n, v1.KindWorkflowRun, "r1", "the run reaches the node", func(*v1.WorkflowRun) bool { return true })
	update(t, n.node, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Status.Phase = v1.RunRunning })
	onCP(t, n, v1.KindWorkflowRun, "r1", "it runs", func(r *v1.WorkflowRun) bool { return r.Status.Phase == v1.RunRunning })
	stop()
	n.replaceStore(t, n.cut(t))
	update(t, n.node, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Spec.Paused = true }) // restore run's pause
	n.prepare(t)
	obj, err := n.node.Get(ctx, v1.KindWorkflowRun.GVK(), "e1", "r1")
	require.NoError(t, err)
	require.True(t, obj.(*v1.WorkflowRun).Spec.Paused, "the control plane's copy does not resume it")
	defer n.start(t)()
	onCP(t, n, v1.KindWorkflowRun, "r1", "the control plane shows it paused", func(r *v1.WorkflowRun) bool { return r.Spec.Paused })
	update(t, n.cp, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Spec.Paused = false })
	onNode(t, n, v1.KindWorkflowRun, "r1", "a person resumes it", func(r *v1.WorkflowRun) bool { return !r.Spec.Paused })
	update(t, n.node, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Status.Phase = v1.RunSucceeded })
	done := onCP(t, n, v1.KindWorkflowRun, "r1", "it ends", func(r *v1.WorkflowRun) bool { return r.Status.Phase == v1.RunSucceeded })
	require.False(t, done.Spec.Paused, "a late report of the node does not pause it again")
}

// What ended stays ended: a node restored to a point before the end takes the control plane's end, and what its own
// restore or its start made of the older status never replaces it.
// oracle: docs/design/multinode/restore.fizz#AnEndedRunStaysEnded
func TestWhatEndedStaysEndedAfterItsNodesRestore(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	_, err := n.cp.Create(ctx, jobRun("report-1", "report"))
	require.NoError(t, err)
	_, err = n.cp.Create(ctx, workflowRun("r1"))
	require.NoError(t, err)
	stop := n.start(t)
	onNode(t, n, v1.KindJobRun, "report-1", "the run reaches the node", func(*v1.JobRun) bool { return true })
	onNode(t, n, v1.KindWorkflowRun, "r1", "so does the other", func(*v1.WorkflowRun) bool { return true })
	backup := n.cut(t)
	update(t, n.node, v1.KindJobRun, "report-1", func(r *v1.JobRun) {
		r.Status.End(v1.JobRunSucceeded, "", "", v1.NewTimestamp(time.Now()))
	})
	update(t, n.node, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Status.Phase = v1.RunSucceeded })
	ended := func(r *v1.JobRun) bool { return r.Status.Phase == v1.JobRunSucceeded }
	over := func(r *v1.WorkflowRun) bool { return r.Status.Phase == v1.RunSucceeded }
	onCP(t, n, v1.KindJobRun, "report-1", "its end comes back", ended)
	onCP(t, n, v1.KindWorkflowRun, "r1", "so does the other's", over)
	stop()

	n.replaceStore(t, backup)
	update(t, n.node, v1.KindJobRun, "report-1", func(r *v1.JobRun) { // what restore run writes (ADR-0347 Decision 7)
		r.Status.End(v1.JobRunCancelled, v1.JobRunRestored, "a platform restore", v1.NewTimestamp(time.Now()))
	})
	update(t, n.node, v1.KindWorkflowRun, "r1", func(r *v1.WorkflowRun) { r.Spec.Paused = true })
	n.prepare(t)
	onNode(t, n, v1.KindJobRun, "report-1", "the node takes the end", ended)
	onNode(t, n, v1.KindWorkflowRun, "r1", "of both", over)
	defer n.start(t)()
	update(t, n.node, v1.KindJobRun, "report-1", func(r *v1.JobRun) {
		r.Status.End(v1.JobRunFailed, v1.JobRunInterrupted, "cut by the stop", v1.NewTimestamp(time.Now()))
	})
	time.Sleep(300 * time.Millisecond)
	onCP(t, n, v1.KindJobRun, "report-1", "the control plane keeps the end", ended)
	run := onCP(t, n, v1.KindWorkflowRun, "r1", "of both", over)
	require.False(t, run.Spec.Paused, "a run that ended takes no pause")
}
