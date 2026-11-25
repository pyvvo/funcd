// Package schedulercontract is the shared conformance suite for the
// scheduler.Scheduler port (ADR-0017, ADR-0145): Run asserts the port guarantee against any
// driver — a valid request yields a non-empty Placement.WorkerNode and no error, unless it lists
// artifact platforms none of which its worker's platform, else the driver's node's, is. The single-node driver runs it
// in `just ci`; the future multi-node driver inherits it.
package schedulercontract

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/scheduler"
)

// Run asserts the Scheduler port guarantee against s, whose (only) node runs node.
func Run(t *testing.T, s scheduler.Scheduler, node v1.OCIPlatform) {
	t.Helper()
	reqs := []scheduler.Request{
		{Namespace: "default", Name: "fn-a", Replica: 0},
		{Namespace: "team-x", Name: "fn-b", Replica: 3},
		{Namespace: "default", Name: "fn-c", Replica: 0, Platforms: []v1.OCIPlatform{node}},
		{Namespace: "default", Name: "fn-d", Replica: 0, Platforms: []v1.OCIPlatform{"plan9/mips", node}},
		{Namespace: "default", Name: "fn-f", Replica: 0, Platforms: []v1.OCIPlatform{"plan9/mips"}, WorkerPlatform: "plan9/mips"},
	}
	for _, req := range reqs {
		p, err := s.Schedule(context.Background(), req)
		require.NoError(t, err, "Schedule(%s/%s) must not fail", req.Namespace, req.Name)
		require.NotEmpty(t, string(p.WorkerNode), "Schedule(%s/%s) must name a worker", req.Namespace, req.Name)
	}
	for why, req := range map[string]scheduler.Request{
		"an artifact for no node's platform":              {Namespace: "default", Name: "fn-e", Platforms: []v1.OCIPlatform{"plan9/mips"}},
		"an artifact for the node's and not the worker's": {Namespace: "default", Name: "fn-g", Platforms: []v1.OCIPlatform{node}, WorkerPlatform: "plan9/mips"},
	} {
		_, err := s.Schedule(context.Background(), req)
		require.True(t, errors.Is(err, scheduler.ErrNoMatchingPlatform), "%s is refused: %v", why, err)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	}
}
