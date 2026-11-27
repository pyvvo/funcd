package multinode

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// liveRig is the liveness of a store that declares WorkerNode vm1, on the test's clock.
func liveRig(t *testing.T) (*Liveness, store.Store, *clock) {
	t.Helper()
	clk := &clock{now: time.Now()}
	st := store.New(memory.New())
	_, err := st.Create(context.Background(), workerNode("vm1"))
	require.NoError(t, err)
	return NewLiveness(st, slog.New(slog.DiscardHandler), clk.Now, 0, 0), st, clk
}

func vm1Status(t *testing.T, st store.Store) v1.WorkerNodeStatus {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindWorkerNode.GVK(), "", "vm1")
	require.NoError(t, err)
	return obj.(*v1.WorkerNode).Status
}

func readyReason(s v1.WorkerNodeStatus) string {
	for _, c := range s.Conditions {
		if c.Type == v1.ConditionNodeReady {
			return c.Reason
		}
	}
	return ""
}

func joined(t *testing.T, st store.Store) {
	t.Helper()
	require.NoError(t, writeNodeStatus(context.Background(), st, "vm1", func(n *v1.WorkerNode) bool {
		n.Status.Certificate = &v1.NodeCertificate{Serial: "1"}
		return true
	}))
}

func polls(l *Liveness) { l.Seen("vm1", "test", nodeapi.APIVersion, "linux/arm64") }

// oracle: docs/design/multinode/liveness.fizz#NoReadyLongAfterTheLastPoll
func TestAWorkerNodeReadsReadyWhileItsNodePolls(t *testing.T) {
	ctx := context.Background()
	l, st, clk := liveRig(t)
	require.NoError(t, l.Sync(ctx))
	require.Equal(t, v1.PhasePending, vm1Status(t, st).Phase)
	require.Equal(t, ReasonNotJoined, readyReason(vm1Status(t, st)))
	joined(t, st)
	polls(l)
	require.NoError(t, l.Sync(ctx))
	s := vm1Status(t, st)
	require.Equal(t, v1.PhaseReady, s.Phase)
	require.Equal(t, ReasonPolling, readyReason(s))
	require.Equal(t, "linux/arm64", s.Platform)
	require.Equal(t, "test", s.Version)
	require.Equal(t, nodeapi.APIVersion, s.NodeAPIVersion)
	clk.Add(defaultReadyFor)
	require.NoError(t, l.Sync(ctx))
	require.Equal(t, v1.PhaseDegraded, vm1Status(t, st).Phase, "a node that has not polled for defaultReadyFor")
	require.Equal(t, ReasonNotPolling, readyReason(vm1Status(t, st)))
	polls(l)
	require.NoError(t, l.Sync(ctx))
	require.Equal(t, v1.PhaseReady, vm1Status(t, st).Phase, "and polls again")
}

func TestALastSeenIsWrittenAtMostEverySeenEvery(t *testing.T) {
	ctx := context.Background()
	l, st, clk := liveRig(t)
	polls(l)
	require.NoError(t, l.Sync(ctx))
	first := vm1Status(t, st).LastSeen
	clk.Add(10 * time.Second)
	polls(l)
	require.NoError(t, l.Sync(ctx))
	require.Equal(t, first, vm1Status(t, st).LastSeen, "a poll within defaultSeenEvery writes no lastSeen")
	clk.Add(defaultSeenEvery)
	polls(l)
	require.NoError(t, l.Sync(ctx))
	require.True(t, time.Time(vm1Status(t, st).LastSeen).After(time.Time(first)))
}

// oracle: docs/design/multinode/liveness.fizz#NoDegradedWhilePolling
// oracle: docs/design/multinode/liveness.fizz#NoReadyLongAfterTheLastPoll
func TestAControlPlaneThatStartsFlipsNoNodeThatPollsAgain(t *testing.T) {
	ctx := context.Background()
	before, st, clk := liveRig(t)
	joined(t, st)
	polls(before)
	require.NoError(t, before.Sync(ctx))
	seen := vm1Status(t, st).LastSeen
	clk.Add(35 * time.Second) // past defaultSeenEvery, within defaultReadyFor
	after := clusterOn(t, st, clk)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = after.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool { return after.liveness.Ready("vm1") }, 5*time.Second, 10*time.Millisecond,
		"a control plane that starts takes a node that read Ready as polled, so a join under its name is refused")
	require.NoError(t, after.liveness.Sync(ctx))
	require.Equal(t, v1.PhaseReady, vm1Status(t, st).Phase, "a node that read Ready stays Ready while it may poll again")
	require.Equal(t, seen, vm1Status(t, st).LastSeen, "and its lastSeen stays that of its last poll, though stale")
	clk.Add(defaultReadyFor)
	require.NoError(t, after.liveness.Sync(ctx))
	require.Equal(t, v1.PhaseDegraded, vm1Status(t, st).Phase, "one that does not poll again reads Degraded")
	require.Equal(t, seen, vm1Status(t, st).LastSeen, "with the lastSeen of its last poll")
	again := NewLiveness(st, slog.New(slog.DiscardHandler), clk.Now, 0, 0)
	again.assumeReady(ctx)
	require.False(t, again.Ready("vm1"), "and a control plane that starts then takes no poll of it")
}
