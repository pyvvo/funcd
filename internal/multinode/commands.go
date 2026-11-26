package multinode

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

// The node commands besides the log reads (FEAT-0002 F250): each acts where the namespace runs.

// Replayer replays a dead letter (controlplane.Replayer).
type Replayer interface {
	Replay(ctx context.Context, ns v1.NamespaceName, id string) error
}

// AppRetrier retries an App's failed hook (controlplane.AppRetrier).
type AppRetrier interface {
	Retry(ctx context.Context, ns v1.NamespaceName, app v1.ObjectName) error
}

// VolumeDeleter deletes a Function's retained volume (controlplane.VolumeDeleter).
type VolumeDeleter interface {
	DeleteVolume(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error
}

// Commands are a node's local parts that answer the node commands.
type Commands struct {
	DeadLetters deadletter.Store
	Replayer    Replayer
	Apps        AppRetrier
	Volumes     VolumeDeleter
	Admissions  []admission.Admission // the node-state checks before a write (NodeState)
	BlobStore   string                // the address of the node's blob store
	// Backup reads the node's platform backup (F251); nil ⇒ the node runs none.
	Backup Backups
	// Hold reads and releases the node's holds and returns its quarantined data.
	Hold Holder
}

// Backups reads a platform backup's status (controlplane.BackupStatuser).
type Backups interface {
	Status(ctx context.Context) (runner.Status, error)
}

// DeadLetterRouter is the dead-letter store the API reads, lists and discards with: a namespace that a worker node runs
// is answered there. Put, Update and SweepExpired are the local eventing's, which parks only its own namespaces'.
type DeadLetterRouter struct {
	router
	deadletter.Store
}

func (r DeadLetterRouter) List(ctx context.Context, ns v1.NamespaceName) ([]deadletter.DeadLetter, error) {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return nil, err
	case node == "" && r.Store == nil:
		return nil, errNoDeadLetters
	case node == "":
		return r.Store.List(ctx, ns)
	}
	a, err := r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallDeadLetters, Namespace: ns})
	if err != nil {
		return nil, err
	}
	var out []deadletter.DeadLetter
	if err := json.Unmarshal(a.Body, &out); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "multinode.DeadLetters", "decode worker node %s's answer", node)
	}
	return out, nil
}

func (r DeadLetterRouter) Get(ctx context.Context, ns v1.NamespaceName, id string) (deadletter.DeadLetter, error) {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return deadletter.DeadLetter{}, err
	case node == "" && r.Store == nil:
		return deadletter.DeadLetter{}, errNoDeadLetters
	case node == "":
		return r.Store.Get(ctx, ns, id)
	}
	a, err := r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallDeadLetter, Namespace: ns, Name: id})
	if err != nil {
		return deadletter.DeadLetter{}, err
	}
	var out deadletter.DeadLetter
	if err := json.Unmarshal(a.Body, &out); err != nil {
		return deadletter.DeadLetter{}, fault.Wrapf(err, fault.Internal, "multinode.DeadLetter", "decode worker node %s's answer", node)
	}
	return out, nil
}

func (r DeadLetterRouter) Delete(ctx context.Context, ns v1.NamespaceName, id string) error {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return err
	case node == "" && r.Store == nil:
		return errNoDeadLetters
	case node == "":
		return r.Store.Delete(ctx, ns, id)
	}
	_, err = r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallDiscardDeadLetter, Namespace: ns, Name: id})
	return err
}

var errNoDeadLetters = fault.Unavailablef("multinode.DeadLetters", "this control plane keeps no dead letters")

// ReplayRouter replays a dead letter where its namespace runs.
type ReplayRouter struct {
	router
	local Replayer
}

func (r ReplayRouter) Replay(ctx context.Context, ns v1.NamespaceName, id string) error {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return err
	case node == "" && r.local == nil:
		return fault.Unavailablef("multinode.Replay", "this control plane replays no dead letters")
	case node == "":
		return r.local.Replay(ctx, ns, id)
	}
	_, err = r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallReplayDeadLetter, Namespace: ns, Name: id})
	return err
}

// AppRetryRouter retries an App's failed hook where its namespace runs.
type AppRetryRouter struct {
	router
	local AppRetrier
}

func (r AppRetryRouter) Retry(ctx context.Context, ns v1.NamespaceName, app v1.ObjectName) error {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return err
	case node == "" && r.local == nil:
		return fault.Unavailablef("multinode.Retry", "this control plane runs no Apps")
	case node == "":
		return r.local.Retry(ctx, ns, app)
	}
	_, err = r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallAppRetry, Namespace: ns, Name: string(app)})
	return err
}

// VolumeRouter deletes a retained Function volume on the node that holds it, the one that runs its namespace.
type VolumeRouter struct {
	router
	local VolumeDeleter
}

func (r VolumeRouter) DeleteVolume(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error {
	node, err := r.remote(ctx, ns)
	switch {
	case err != nil:
		return err
	case node == "" && r.local == nil:
		return fault.Unavailablef("multinode.DeleteVolume", "this control plane keeps no Function volumes")
	case node == "":
		return r.local.DeleteVolume(ctx, ns, fn, name)
	}
	_, err = r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallVolumeDelete, Namespace: ns, Name: string(fn), Volume: name})
	return err
}

// Holder is the platform hold's status and release (controlplane.Holder).
type Holder interface {
	Status(ctx context.Context) (hold.Evidence, error)
	Release(ctx context.Context, advance []string) error
	ReleaseNamespaces(ctx context.Context, nss []v1.NamespaceName) error
	ReturnQuarantined(ctx context.Context, r hold.QuarantineReturn) error
}

// HoldRouter releases a namespace's hold and returns its quarantined data on the node that runs the namespace (F250).
// The status and the release of the platform's hold are this process's own; a worker node's are behind Nodes.
type HoldRouter struct {
	router
	Holder
}

func (r HoldRouter) ReleaseNamespaces(ctx context.Context, nss []v1.NamespaceName) error {
	var here []v1.NamespaceName
	remote := map[v1.ObjectName][]v1.NamespaceName{}
	for _, ns := range nss {
		node, err := r.remote(ctx, ns)
		if err != nil {
			return err
		}
		if node == "" {
			here = append(here, ns)
			continue
		}
		remote[node] = append(remote[node], ns)
	}
	if len(here) > 0 {
		if err := r.Holder.ReleaseNamespaces(ctx, here); err != nil {
			return err
		}
	}
	for _, node := range slices.Sorted(maps.Keys(remote)) {
		body, err := json.Marshal(remote[node])
		if err != nil {
			return fault.Wrapf(err, fault.Internal, "multinode.ReleaseNamespaces", "marshal")
		}
		if _, err := r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallReleaseNamespaces, Namespace: remote[node][0], Body: body}); err != nil {
			return err
		}
	}
	return nil
}

func (r HoldRouter) ReturnQuarantined(ctx context.Context, q hold.QuarantineReturn) error {
	node, err := r.remote(ctx, q.Namespace)
	switch {
	case err != nil:
		return err
	case node == "":
		return r.Holder.ReturnQuarantined(ctx, q)
	}
	body, err := json.Marshal(q)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "multinode.ReturnQuarantined", "marshal")
	}
	_, err = r.calls.Call(ctx, node, nodeapi.Call{Kind: nodeapi.CallReturnQuarantined, Namespace: q.Namespace, Body: body})
	return err
}

// Nodes reads and releases a worker node's own hold and reads its platform backup, by the node's name (F250): each side
// has its own hold and its own backup (decision 10). The control plane's own node is this process.
type Nodes struct {
	calls  caller
	st     store.Store
	local  v1.ObjectName
	hold   Holder
	backup Backups
}

// HoldStatus is the evidence of node's hold.
func (n Nodes) HoldStatus(ctx context.Context, node v1.ObjectName) (hold.Evidence, error) {
	if node == n.local {
		return n.hold.Status(ctx)
	}
	var out hold.Evidence
	err := n.ask(ctx, node, nodeapi.Call{Kind: nodeapi.CallHoldStatus}, &out)
	return out, err
}

// ReleaseHold releases node's hold.
func (n Nodes) ReleaseHold(ctx context.Context, node v1.ObjectName, advance []string) error {
	if node == n.local {
		return n.hold.Release(ctx, advance)
	}
	body, err := json.Marshal(advance)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "multinode.ReleaseHold", "marshal")
	}
	return n.ask(ctx, node, nodeapi.Call{Kind: nodeapi.CallReleaseHold, Body: body}, nil)
}

// BackupStatus is the status of node's platform backup; the zero Status is a node that runs none.
func (n Nodes) BackupStatus(ctx context.Context, node v1.ObjectName) (runner.Status, error) {
	var out runner.Status
	if node == n.local {
		if n.backup == nil {
			return out, nil
		}
		return n.backup.Status(ctx)
	}
	err := n.ask(ctx, node, nodeapi.Call{Kind: nodeapi.CallBackupStatus}, &out)
	return out, err
}

// ask sends c to node, a declared WorkerNode, and decodes its answer into out, unless nil.
//
//nolint:forbidigo // out is one of the node commands' typed answers
func (n Nodes) ask(ctx context.Context, node v1.ObjectName, c nodeapi.Call, out any) error {
	const op = "multinode.Nodes"
	_, err := n.st.Get(ctx, v1.KindWorkerNode.GVK(), "", node)
	if fault.KindOf(err) == fault.NotFound {
		return fault.NotFoundf(op, "no WorkerNode %s is declared", node)
	}
	if err != nil {
		return err
	}
	a, err := n.calls.Call(ctx, node, c)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(a.Body, out); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "decode worker node %s's answer", node)
	}
	return nil
}

// NodeBackup is what a WorkerNode's status keeps of st, its node's platform backup.
func NodeBackup(st runner.Status) v1.NodeBackup {
	out := v1.NodeBackup{Held: st.Held, RPORisk: st.RPORisk, LastSuccessTime: st.LastSuccessTime, NextRunTime: st.NextRunTime}
	if st.LastFailure != nil {
		out.LastFailure = st.LastFailure.Error
	}
	return out
}
