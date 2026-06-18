// Package schedulercontract is the shared conformance suite for the
// scheduler.Scheduler port (ADR-0017): Run asserts the port guarantee against any
// driver — a valid request yields a non-empty Placement.WorkerNode and no error. The
// single-node driver runs it in `just ci`; the future multi-node driver inherits it.
package schedulercontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/scheduler"
)

// Run asserts the Scheduler port guarantee against s: every valid request yields a
// non-empty Placement.WorkerNode and no error.
func Run(t *testing.T, s scheduler.Scheduler) {
	t.Helper()
	reqs := []scheduler.Request{
		{Namespace: "default", Name: "fn-a", Replica: 0},
		{Namespace: "team-x", Name: "fn-b", Replica: 3},
	}
	for _, req := range reqs {
		p, err := s.Schedule(context.Background(), req)
		require.NoError(t, err, "Schedule(%s/%s) must not fail", req.Namespace, req.Name)
		require.NotEmpty(t, string(p.WorkerNode), "Schedule(%s/%s) must name a worker", req.Namespace, req.Name)
	}
}
