package multinode

import (
	"context"
	"encoding/json"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
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
	a, err := r.calls.Do(ctx, node, Call{Kind: CallDeadLetters, Namespace: ns})
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
	a, err := r.calls.Do(ctx, node, Call{Kind: CallDeadLetter, Namespace: ns, Name: id})
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
	_, err = r.calls.Do(ctx, node, Call{Kind: CallDiscardDeadLetter, Namespace: ns, Name: id})
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
	_, err = r.calls.Do(ctx, node, Call{Kind: CallReplayDeadLetter, Namespace: ns, Name: id})
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
	_, err = r.calls.Do(ctx, node, Call{Kind: CallAppRetry, Namespace: ns, Name: string(app)})
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
	_, err = r.calls.Do(ctx, node, Call{Kind: CallVolumeDelete, Namespace: ns, Name: string(fn), Volume: name})
	return err
}
