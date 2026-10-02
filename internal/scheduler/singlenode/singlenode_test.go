package singlenode_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/scheduler"
	"github.com/pyvvo/funcd/internal/scheduler/schedulercontract"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
)

// scenario: single-node-places-local — any request is placed on the configured local worker.
func TestScenarioSingleNodePlacesLocal(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("local", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	p, err := s.Schedule(context.Background(), scheduler.Request{Namespace: "default", Name: "echo", Replica: 0})
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("local"), p.WorkerNode)
}

// scenario: placement-is-deterministic — every placement targets the same local worker.
func TestScenarioPlacementIsDeterministic(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("node-0", v1.PlatformLinuxARM64)
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
	_, err := singlenode.New("", v1.PlatformLinuxARM64)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// scenario: scheduler-contract-holds — the single-node driver satisfies the port contract.
func TestScenarioSchedulerContractHolds(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("local", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	schedulercontract.Run(t, s, v1.PlatformLinuxARM64)
}

// scenario: placement-filters-by-platform (ADR-0145) — a node running linux/arm64 refuses an artifact built only
// for linux/amd64, with a message naming both sides, and places one that provides its platform.
func TestScenarioPlacementFiltersByPlatform(t *testing.T) {
	t.Parallel()
	s, err := singlenode.New("local", v1.PlatformLinuxARM64)
	require.NoError(t, err)
	_, err = s.Schedule(context.Background(), scheduler.Request{
		Namespace: "default", Name: "reader", Platforms: []v1.OCIPlatform{v1.PlatformLinuxAMD64},
	})
	require.True(t, errors.Is(err, scheduler.ErrNoMatchingPlatform))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "artifact provides [linux/amd64]; node local runs linux/arm64")

	p, err := s.Schedule(context.Background(), scheduler.Request{
		Namespace: "default", Name: "reader", Platforms: []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64},
	})
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("local"), p.WorkerNode)
}

// An invalid node platform is a misconfiguration, refused at construction.
func TestInvalidNodePlatformRejected(t *testing.T) {
	t.Parallel()
	_, err := singlenode.New("local", "linux")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
