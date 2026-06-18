package singlenode_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/scheduler"
	"github.com/green-0-rabbit/funcd/internal/scheduler/schedulercontract"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
)

// scenario: single-node-places-local — any request is placed on the configured local worker.
func TestScenarioSingleNodePlacesLocal(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("local")
	require.NoError(t, err)
	p, err := s.Schedule(context.Background(), scheduler.Request{Namespace: "default", Name: "echo", Replica: 0})
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("local"), p.WorkerNode)
}

// scenario: placement-is-deterministic — every placement targets the same local worker.
func TestScenarioPlacementIsDeterministic(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("node-0")
	require.NoError(t, err)
	for _, req := range []scheduler.Request{
		{Namespace: "default", Name: "a", Replica: 0},
		{Namespace: "default", Name: "a", Replica: 1},
		{Namespace: "team", Name: "b", Replica: 7},
	} {
		p, err := s.Schedule(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, v1.ObjectName("node-0"), p.WorkerNode, "single-node invariant: always the local worker")
	}
}

// scenario: empty-local-worker-rejected — construction with an empty local worker fails.
func TestScenarioEmptyLocalWorkerRejected(t *testing.T) {
	t.Parallel()
	_, err := singlenode.New("")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// scenario: scheduler-contract-holds — the single-node driver satisfies the port contract.
func TestScenarioSchedulerContractHolds(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("local")
	require.NoError(t, err)
	schedulercontract.Run(t, s)
}
