package controlplane

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// Nodes reads and releases a worker node's own hold and reads its platform backup (FEAT-0002 F250): a node holds and backs
// up its own stores, apart from the control plane's. An undeclared WorkerNode is fault.NotFound, an offline one
// fault.Unavailable.
type Nodes interface {
	HoldStatus(ctx context.Context, node v1.ObjectName) (hold.Evidence, error)
	ReleaseHold(ctx context.Context, node v1.ObjectName, advance []string) error
	BackupStatus(ctx context.Context, node v1.ObjectName) (runner.Status, error)
}

// NodeQuery names the worker node a route answers for.
type NodeQuery struct {
	Node v1.ObjectName `query:"node" doc:"The WorkerNode to answer for. Left out, the funcd that serves this API: the control plane, or a worker node on its own API."`
}

// noNodes are the Nodes of a funcd that no worker node joins.
type noNodes struct{}

func (noNodes) none(node v1.ObjectName) error {
	return fault.NotFoundf("controlplane.nodes", "no WorkerNode %s: no worker node joins this funcd", node)
}

func (n noNodes) HoldStatus(_ context.Context, node v1.ObjectName) (hold.Evidence, error) {
	return hold.Evidence{}, n.none(node)
}

func (n noNodes) ReleaseHold(_ context.Context, node v1.ObjectName, _ []string) error {
	return n.none(node)
}

func (n noNodes) BackupStatus(_ context.Context, node v1.ObjectName) (runner.Status, error) {
	return runner.Status{}, n.none(node)
}

// nodeQueryCases are the answers of a route that takes NodeQuery.
func nodeQueryCases() []ErrorCase {
	return []ErrorCase{
		problem(fault.NotFound, "The node parameter names no WorkerNode.", "funcdctl get workernodes lists them."),
		problem(fault.Unavailable, "The node parameter names a worker node that is offline.", nodeBack),
		blank(422, "The node parameter is not a DNS-1123 label.", "Send a WorkerNode's name, or leave node out."),
	}
}

// nodeBack is the fix of an answer that an offline worker node gives.
const nodeBack = "Retry once the node polls the control plane again: funcdctl get workernodes shows it Ready."

// hostElsewhere is the 409 of a claim, in field, on a host that a namespace on another worker node claims (FEAT-0002 F247).
func hostElsewhere(field string) ErrorCase {
	return problem(fault.Conflict, "The host in "+field+" is one that a namespace on another worker node claims: a host "+
		"belongs to one node.", "Use another host. To share one across nodes, put a gateway in front of them.")
}

// nodeAway is the 503 of a read or an action that the worker node of the namespace answers, while it is offline (F250).
func nodeAway() ErrorCase {
	return problem(fault.Unavailable, "The namespace runs on a worker node that is offline.", nodeBack)
}
