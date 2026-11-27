package agent_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/multinode/agent"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func records(t *testing.T, src snapshot.Source) []snapshot.Record {
	t.Helper()
	var recs []snapshot.Record
	_, err := src.Snapshot(context.Background(), func(r snapshot.Record) error {
		recs = append(recs, snapshot.Record{Key: bytes.Clone(r.Key), Value: bytes.Clone(r.Value)})
		return nil
	})
	require.NoError(t, err)
	return recs
}

func kvStore(name v1.ObjectName) *v1.KVStore {
	kv := &v1.KVStore{TypeMeta: meta(v1.KindKVStore)}
	kv.Name, kv.Namespace, kv.ResourceGroup = name, "e1", "rg1"
	return kv
}

func anyKVStore(*v1.KVStore) bool { return true }

// A control plane restored to a point before what its node holds: the node keeps it all and follows nothing while the
// control plane is held, then starts again on the restored state, with the runs it made since.
func TestANodeOutlivesTheRestoreOfItsControlPlane(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	_, err := n.cp.Create(ctx, kvStore("kept"))
	require.NoError(t, err)
	stop := n.start(t)
	kept := onNode(t, n, v1.KindKVStore, "kept", "the store reaches the node", anyKVStore)
	_, err = n.node.Create(ctx, owned(configMap("e1", "dropped", "1"), kept))
	require.NoError(t, err)
	onCP(t, n, v1.KindConfigMap, "dropped", "a child reaches the control plane", anyConfigMap)
	backup := records(t, n.cp)
	require.NoError(t, n.node.Delete(ctx, v1.KindConfigMap.GVK(), "e1", "dropped", ""))
	gone(t, n.cp, v1.KindConfigMap, "dropped", "so does its end")
	_, err = n.cp.Create(ctx, kvStore("later"))
	require.NoError(t, err)
	onNode(t, n, v1.KindKVStore, "later", "so does one made since the backup", anyKVStore)
	_, err = n.node.Create(ctx, jobRun("made", "report"))
	require.NoError(t, err)
	onCP(t, n, v1.KindJobRun, "made", "a run the node made reaches the control plane", func(*v1.JobRun) bool { return true })
	stop()
	addr := n.cluster.Addr()
	n.cancelCP()

	eng := memory.New()
	require.NoError(t, eng.Load(ctx, snapshotcontract.Feed(backup)))
	n.cp = store.New(eng, store.WithChangeLog(0))
	var held atomic.Bool
	held.Store(true)
	restored := newCluster(t, multinode.Config{Store: n.cp, LocalNode: "local", LocalRuns: true, Master: []byte(cpMaster),
		Held: held.Load}, n.caDir, addr)
	cpctx, stopCP := context.WithCancel(ctx)
	defer stopCP()
	go func() { _ = restored.Run(cpctx) }()

	a, err := agent.New(agent.Config{Connector: longpoll.NewConnector(), Dir: n.dir, Store: n.node, Logs: fakeLogs{}, Version: "test", Platform: "linux/arm64"})
	require.NoError(t, err)
	require.NoError(t, a.Prepare(ctx), "a node that holds its namespaces starts without the control plane")
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)
	_, err = n.node.Get(ctx, v1.KindKVStore.GVK(), "e1", "later")
	require.NoError(t, err, "while the control plane is held its node drops nothing")
	select {
	case err := <-done:
		require.Fail(t, "the agent stopped while the control plane is held", "%v", err)
	default:
	}

	held.Store(false)
	select {
	case err := <-done:
		require.True(t, errors.Is(err, agent.ErrRestored), "%v", err)
	case <-time.After(20 * time.Second):
		require.Fail(t, "the agent did not see the restore")
	}
	_, err = n.node.Get(ctx, v1.KindKVStore.GVK(), "e1", "later")
	require.NoError(t, err, "a running platform drops nothing either")

	n.prepare(t) // the platform's next start, before it runs anything
	gone(t, n.node, v1.KindKVStore, "later", "the restored control plane does not know the store")
	onNode(t, n, v1.KindKVStore, "kept", "the other stays", anyKVStore)
	onNode(t, n, v1.KindJobRun, "made", "so does the run the node made", func(*v1.JobRun) bool { return true })
	onCP(t, n, v1.KindJobRun, "made", "which the control plane takes again", func(*v1.JobRun) bool { return true })
	gone(t, n.node, v1.KindConfigMap, "dropped", "a child the node dropped does not come back")
	defer n.start(t)()
	gone(t, n.cp, v1.KindConfigMap, "dropped", "and goes from the control plane again")
	_, err = n.cp.Create(ctx, kvStore("next"))
	require.NoError(t, err)
	onNode(t, n, v1.KindKVStore, "next", "and the node follows the restored control plane", anyKVStore)
}

// A run the node made while the control plane was away outlives the snapshot the node starts again from.
// oracle: docs/design/multinode/writeback.fizz#NoRunIsLost
func TestARunTheNodeMadeOutlivesASnapshot(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	stop := n.start(t)
	stop()
	_, err := n.node.Create(ctx, jobRun("made", "report"))
	require.NoError(t, err)
	n.agent.ResyncForTest()
	a := n.agent
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()
	defer func() { cancel(); <-done }()
	onCP(t, n, v1.KindJobRun, "made", "the run reaches the control plane", func(*v1.JobRun) bool { return true })
	onNode(t, n, v1.KindJobRun, "made", "and stays on the node", func(*v1.JobRun) bool { return true })
}

// A node that joins anew, as after a restore of the control plane to a point before its certificate, keeps its store and the
// runs it made, and follows from a whole snapshot.
func TestANodeThatJoinsAgainKeepsTheRunsItMade(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t)
	stop := n.start(t)
	_, err := n.cp.Create(ctx, kvStore("kv1"))
	require.NoError(t, err)
	kv := onNode(t, n, v1.KindKVStore, "kv1", "the store reaches the node", anyKVStore)
	_, err = n.node.Create(ctx, owned(configMap("e1", "dropped", "1"), kv))
	require.NoError(t, err)
	onCP(t, n, v1.KindConfigMap, "dropped", "a child reaches the control plane", anyConfigMap)
	stop()
	require.NoError(t, n.node.Delete(ctx, v1.KindConfigMap.GVK(), "e1", "dropped", ""))
	n.cancelCP() // a new control plane process knows no node that polled a moment ago
	n.cluster = newCluster(t, multinode.Config{Store: n.cp, LocalNode: "local", LocalRuns: true, Master: []byte(cpMaster)},
		n.caDir, "127.0.0.1:0")
	cpctx, stopCP := context.WithCancel(ctx)
	defer stopCP()
	go func() { _ = n.cluster.Run(cpctx) }()
	_, err = n.node.Create(ctx, jobRun("made", "report"))
	require.NoError(t, err)
	require.NoError(t, n.cp.Delete(store.WithoutChangeLog(ctx), v1.KindKVStore.GVK(), "e1", "kv1", ""))
	token, _, err := n.cluster.MintJoinToken(ctx, "vm1", 0)
	require.NoError(t, err)
	_, err = agent.Join(ctx, longpoll.NewConnector(), n.server(), token, n.dir, "test", "linux/arm64")
	require.NoError(t, err)
	n.prepare(t)
	gone(t, n.node, v1.KindKVStore, "kv1", "a whole snapshot shows what went meanwhile")
	onNode(t, n, v1.KindJobRun, "made", "the run the node made stays", func(*v1.JobRun) bool { return true })
	onCP(t, n, v1.KindJobRun, "made", "and reaches the control plane", func(*v1.JobRun) bool { return true })
	gone(t, n.node, v1.KindConfigMap, "dropped", "a child the node dropped does not come back")
}
