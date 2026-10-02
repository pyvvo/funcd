// Package singlenode is the single-node scheduler driver (ADR-0017): it places every
// function replica on one configured local worker — deterministic, and total except for an
// artifact built for other platforms than the node's (ADR-0145). It is the V1 placement
// driver; multi-node bin-packing across registered worker nodes is a future driver behind
// the scheduler.Scheduler port.
package singlenode

import (
	"context"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/scheduler"
)

type driver struct {
	local v1.ObjectName
	node  v1.OCIPlatform
}

// New returns a single-node scheduler that places every replica on local, a worker node
// running node. It returns fault.Invalid if local is empty (a misconfigured scheduler must not
// place onto "") or node is not an <os>/<arch> platform.
func New(local v1.ObjectName, node v1.OCIPlatform) (scheduler.Scheduler, error) {
	if local == "" {
		return nil, fault.Invalidf("scheduler.singlenode.New", "local worker name must not be empty")
	}
	if err := node.Validate(); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, "scheduler.singlenode.New", "node platform")
	}
	return &driver{local: local, node: node}, nil
}

// Schedule places every request on the single local worker, unless the request lists the
// artifact's platforms and the node's is not among them (ADR-0145).
func (d *driver) Schedule(_ context.Context, req scheduler.Request) (scheduler.Placement, error) {
	if len(req.Platforms) > 0 && !slices.Contains(req.Platforms, d.node) {
		names := make([]string, len(req.Platforms))
		for i, p := range req.Platforms {
			names[i] = string(p)
		}
		return scheduler.Placement{}, fault.Wrapf(scheduler.ErrNoMatchingPlatform, fault.Invalid, "scheduler.singlenode.Schedule",
			"artifact provides [%s]; node %s runs %s", strings.Join(names, ", "), d.local, d.node)
	}
	return scheduler.Placement{WorkerNode: d.local}, nil
}
