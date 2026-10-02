// Package scheduler is the placement port (ADR-0017): the control-plane decision of
// which worker/node runs a function replica. V1 ships a single-node driver
// (internal/scheduler/singlenode); multi-node bin-packing across registered worker nodes
// is a driver swap behind this port. The controller's Function reconciler is the
// caller — this package imports no controller, store, or runtime; placement is
// decided from the Request alone.
package scheduler

import (
	"context"
	"errors"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Request identifies a function replica to place. A multi-node driver will extend
// this with resource fields (from the Function `resources` spec) when it first
// bin-packs on them; V1 carries no inert fields.
type Request struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Replica   int
	// Platforms the artifact provides (ADR-0145); empty means any.
	Platforms []v1.OCIPlatform
}

// ErrNoMatchingPlatform is wrapped (fault.Invalid) by Schedule when no worker node's platform is in
// Request.Platforms (ADR-0145).
var ErrNoMatchingPlatform = errors.New("no worker node matches the artifact's platforms")

// Placement is the scheduler's decision: which worker/node runs the replica.
type Placement struct {
	WorkerNode v1.ObjectName
}

// Scheduler decides where a function replica runs. V1 ships a single-node driver;
// multi-node bin-packing is a driver swap behind this port. Errors are api/fault;
// the method is ctx-first.
type Scheduler interface {
	Schedule(ctx context.Context, req Request) (Placement, error)
}
