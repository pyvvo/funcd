//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// joined makes n the worker node name of the control plane behind admin, whose node API is at cluster.
func (n *node) joined(t *testing.T, admin *sdk.Client, cluster string, name v1.ObjectName) {
	t.Helper()
	n.role = roleNode
	tok, err := admin.MintJoinToken(context.Background(), name, "")
	require.NoError(t, err)
	out, err := n.run("node", "join", "--server", "https://"+cluster, "--token", tok.Token)
	require.NoError(t, err)
	require.Contains(t, out, "joined as worker node "+string(name))
}

// newControlPlane is a node that serves as the control plane alone, and the address of its node API.
func newControlPlane(t *testing.T) (*node, string) {
	t.Helper()
	cluster := freeAddr(t)
	n := newNode(t, nil, fmt.Sprintf("cluster:\n  listenAddr: %q\n", cluster))
	n.role = roleServer
	return n, cluster
}

func applyAll(t *testing.T, c *sdk.Client, objs ...v1.Object) {
	t.Helper()
	for _, o := range objs {
		_, err := c.Apply(context.Background(), o)
		require.NoError(t, err, "apply %s", o.GetObjectMeta().Name)
	}
}

// teamOnNode are the WorkerNode name and the namespace team placed on it.
func teamOnNode(name v1.ObjectName) []v1.Object {
	wn := &v1.WorkerNode{TypeMeta: tmeta(v1.KindWorkerNode)}
	wn.Name = name
	ns := &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace)}
	ns.Name, ns.Spec.Node = "team", name
	return []v1.Object{wn, ns}
}

// TestAReplacedNodeComesBackFromItsBackup loses a worker node's machine (FEAT-0002 F251): a new one joins under its name
// and restores the node's own backup, which asks for no key. It boots held with what changed on the control plane since,
// its Secrets come from the control plane, the run the restore paused shows paused there, and a release lets it act.
//
// Serial: it reopens the control plane's Badger directories, which a parallel test's forked child can hold locked (#1178).
func TestAReplacedNodeComesBackFromItsBackup(t *testing.T) {
	ctx := context.Background()
	target := t.TempDir()
	cp, cluster := newControlPlane(t)
	admin, stopCP := cp.start(t)
	apply := func(objs ...v1.Object) { applyAll(t, admin, objs...) }
	settings := func(n string) *v1.ConfigMap {
		return &v1.ConfigMap{TypeMeta: tmeta(v1.KindConfigMap), ObjectMeta: om("settings"),
			Spec: v1.ConfigMapSpec{Data: map[string]string{"n": n}}}
	}
	apply(append(teamOnNode("vm1"), settings("1"), genSecret("api", "hunter2"), &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: om("cache")},
		&v1.WorkflowRun{TypeMeta: tmeta(v1.KindWorkflowRun), ObjectMeta: om("r1"), Spec: v1.WorkflowRunSpec{Workflow: "wf"}})...)

	recipients, identity := ageRecipients(t)
	backups := restorePacing + fmt.Sprintf("backup:\n  target: %q\n  encryption:\n    recipients:\n      - %q\n      - %q\n",
		gocloud.FileURL(target), recipients[0], recipients[1])
	first := newNode(t, nil, backups)
	first.joined(t, admin, cluster, "vm1")
	_, stopFirst := first.start(t)
	var manifests []string
	require.Eventually(t, func() bool {
		manifests, _ = filepath.Glob(filepath.Join(target, "nodes", "vm1", "gen", "*", "*", "manifest.yaml"))
		return len(manifests) > 0
	}, 20*time.Second, 50*time.Millisecond, "the node backs up under its own prefix: %s", first.logs.String())
	data, err := os.ReadFile(manifests[0])
	require.NoError(t, err)
	var m backup.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Len(t, m.Recipients, 2)
	require.Empty(t, m.SecretsKey, "the control plane holds the node's Secrets")
	require.Empty(t, m.MasterSecret, "and the masters of its namespaces")
	apply(settings("2"), &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: om("later")})
	ready := func(name v1.ObjectName) func() bool {
		return func() bool {
			obj, err := admin.Get(ctx, v1.KindKVStore, "team", name)
			return err == nil && obj.(*v1.KVStore).Status.Phase == v1.PhaseReady
		}
	}
	require.Eventually(t, ready("later"), 10*time.Second, 50*time.Millisecond, "the node runs on after its backup")
	stopFirst()
	stopCP() // which forgets that the lost node polled a moment ago, as the passing of defaultReadyFor would
	admin, _ = cp.start(t)

	second := newNode(t, upgradeKey(), backups) // a new machine with a Secrets key of its own
	second.joined(t, admin, cluster, "vm1")
	out, err := second.restore("run", "latest", "--identity", identity)
	require.NoError(t, err)
	require.NotContains(t, out, "master secret", "a node's credentials derive from its namespaces' masters")
	local, _ := second.start(t)
	var ev hold.Evidence
	require.Eventually(t, func() bool {
		ev, err = admin.HoldStatus(ctx, sdk.OnNode("vm1"))
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "the control plane answers for the node's hold")
	require.True(t, ev.Held)
	require.Equal(t, []string{"team/r1"}, reportOf(t, ev).PausedByRestore)
	ev, err = admin.HoldStatus(ctx)
	require.NoError(t, err)
	require.False(t, ev.Held, "the control plane's own hold is another one")
	obj, err := local.Get(ctx, v1.KindConfigMap, "team", "settings")
	require.NoError(t, err)
	require.Equal(t, "2", obj.(*v1.ConfigMap).Spec.Data["n"], "what changed since the backup comes from the control plane")
	obj, err = local.Get(ctx, v1.KindSecret, "team", "api")
	require.NoError(t, err)
	require.Equal(t, "hunter2", string(obj.(*v1.Secret).Spec.Data["token"]), "so do the Secrets")
	_, err = local.Get(ctx, v1.KindKVStore, "team", "later")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindWorkflowRun, "team", "r1")
		return err == nil && obj.(*v1.WorkflowRun).Spec.Paused
	}, 10*time.Second, 50*time.Millisecond, "the run shows paused on the control plane")

	require.NoError(t, admin.ReleaseHold(ctx, nil, sdk.OnNode("vm1")))
	ev, err = local.HoldStatus(ctx)
	require.NoError(t, err)
	require.False(t, ev.Held)
	require.Equal(t, []string{"team/r1"}, ev.PausedRuns, "and stays paused until a person resumes it")
}

// TestANodeThatLostItsMetastoreKeepsItsData empties a stopped node's metastore, which only copies the control plane's: the
// node takes its namespaces again before a boot step could take what they hold for data of deleted objects.
//
// Serial: it reopens the node's Badger directories, which a parallel test's forked child can hold locked (#1178).
func TestANodeThatLostItsMetastoreKeepsItsData(t *testing.T) {
	ctx := context.Background()
	cp, cluster := newControlPlane(t)
	admin, _ := cp.start(t)
	applyAll(t, admin, append(teamOnNode("vm1"), &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: om("inbox")})...)
	vm := newNode(t, nil, restorePacing)
	vm.joined(t, admin, cluster, "vm1")
	ready := func() bool {
		obj, err := admin.Get(ctx, v1.KindBucket, "team", "inbox")
		return err == nil && obj.(*v1.Bucket).Status.Phase == v1.PhaseReady
	}
	_, stop := vm.start(t)
	require.Eventually(t, ready, 10*time.Second, 50*time.Millisecond, "the node runs the Bucket")
	b, err := gocloud.Open(ctx, gocloud.FileURL(filepath.Join(vm.dataDir, "blob")))
	require.NoError(t, err)
	require.NoError(t, b.Put(ctx, "s3/team/inbox/a.txt", []byte("a"), blob.PutOptions{}))
	require.NoError(t, b.Close())
	stop()
	require.NoError(t, os.RemoveAll(filepath.Join(vm.dataDir, "store")))

	_, stop = vm.start(t)
	stop()
	b, err = gocloud.Open(ctx, gocloud.FileURL(filepath.Join(vm.dataDir, "blob")))
	require.NoError(t, err)
	defer func() { _ = b.Close() }()
	data, err := b.Get(ctx, "s3/team/inbox/a.txt")
	require.NoError(t, err, "the Bucket keeps its object")
	require.Equal(t, "a", string(data))
}
