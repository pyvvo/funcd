// Package singlenode is the single-node scheduler driver (ADR-0017): it places every
// function replica on one configured local worker — total and deterministic. It is
// the V1 placement driver; multi-node bin-packing across registered worker nodes is a
// future driver behind the scheduler.Scheduler port.
package singlenode

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/scheduler"
)

type driver struct {
	local v1.ObjectName
}

// New returns a single-node scheduler that places every replica on local. It returns
// fault.Invalid if local is empty (a misconfigured scheduler must not place onto "").
func New(local v1.ObjectName) (scheduler.Scheduler, error) {
	if local == "" {
		return nil, fault.Invalidf("scheduler.singlenode.New", "local worker name must not be empty")
	}
	return &driver{local: local}, nil
}

// Schedule places every request on the single local worker (one node, no choice).
func (d *driver) Schedule(_ context.Context, _ scheduler.Request) (scheduler.Placement, error) {
	return scheduler.Placement{WorkerNode: d.local}, nil
}
