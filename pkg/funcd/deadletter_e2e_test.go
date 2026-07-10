package funcd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/eventing"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario (in-process): driver-independent DLQ end-to-end (ADR-0118, F85) — a running InMemory platform
// (the in-memory bus / ADR-0108 Fanout — NO JetStream DLQ) whose Sensor `function:` action targets a
// non-existent function. One firing exhausts the bounded retry and dead-letters; the operator lists it,
// replays it (still broken ⇒ re-parked), and discards it — all over the real SDK + control-plane routes.
func TestScenarioDeadLetterEndToEnd(t *testing.T) {
	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	// A Sensor binding a named event to a `function:` action whose target does not exist ⇒ delivery fails.
	sen := &v1.Sensor{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: "notifier", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "d", Source: "gitsrc", Event: "push"}},
			Do: []v1.Action{{Name: "notify", On: "d", Function: "ghost"}},
		},
	}
	_, err = c.Apply(ctx, sen)
	require.NoError(t, err)

	// Wait until the Sensor is reconciled Ready (subscribed to the Fanout) before publishing.
	require.Eventually(t, func() bool {
		obj, gerr := c.Get(ctx, v1.KindSensor, "default", "notifier")
		if gerr != nil {
			return false
		}
		cond, ok := obj.(*v1.Sensor).Status.Conditions.Get("Ready")
		return ok && cond.Status == v1.ConditionTrue
	}, 10*time.Second, 50*time.Millisecond, "the Sensor becomes Ready (subscribed)")

	// Publish ONE named CloudEvent on the in-memory Fanout (the in-process bus) — as if the EventSource
	// ticked. The action delivery to the missing function fails every attempt and is dead-lettered.
	ev, err := eventing.NewNamedEvent("default", "gitsrc", "push")
	require.NoError(t, err)
	require.NoError(t, p.eventFanout.Publish(ctx, ev))

	// The operator lists the parked failure over the SDK + control-plane read route.
	require.Eventually(t, func() bool {
		items, lerr := c.DeadLetters(ctx, "default")
		return lerr == nil && len(items) == 1
	}, 10*time.Second, 50*time.Millisecond, "the exhausted delivery is dead-lettered and listable")

	items, err := c.DeadLetters(ctx, "default")
	require.NoError(t, err)
	require.Len(t, items, 1)
	dl := items[0]
	require.Equal(t, v1.ObjectName("notifier"), dl.Sensor)
	require.Equal(t, "notify", dl.Action)
	require.Equal(t, 3, dl.Attempts)

	// describe round-trips the same record.
	got, err := c.DeadLetter(ctx, "default", dl.ID)
	require.NoError(t, err)
	require.Equal(t, dl.ID, got.ID)

	// replay against the still-broken target fails and re-parks (never lost).
	require.Error(t, c.ReplayDeadLetter(ctx, "default", dl.ID))
	after, err := c.DeadLetters(ctx, "default")
	require.NoError(t, err)
	require.Len(t, after, 1, "a re-failed replay keeps the entry")

	// discard removes it.
	require.NoError(t, c.DiscardDeadLetter(ctx, "default", dl.ID))
	require.Eventually(t, func() bool {
		its, lerr := c.DeadLetters(ctx, "default")
		return lerr == nil && len(its) == 0
	}, 5*time.Second, 50*time.Millisecond, "a discarded entry is gone")

	// a replay of an absent id ⇒ NotFound over the wire.
	require.Equal(t, fault.NotFound, fault.KindOf(c.ReplayDeadLetter(ctx, "default", "01ABSENT")))
}
