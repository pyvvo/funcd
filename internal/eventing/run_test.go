package eventing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func TestIssue114_TimerFiresOncePerInterval(t *testing.T) {
	const span = 10 * time.Second
	for _, interval := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, time.Second, 1100 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			pub := &capturePub{}
			src, err := NewSource(Deps{Store: store.New(memory.New()), Publisher: pub})
			require.NoError(t, err)
			src.registerTimer("team-a", "clock", &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: interval}}})
			start := src.timers[eventKey{ns: "team-a", source: "clock", event: "tick"}].lastFire

			// The Run loop's ticks: not aligned with the registration, with sub-millisecond ticker jitter.
			for k := 1; ; k++ {
				now := start.Add(7*time.Millisecond + time.Duration(k)*runTick + time.Duration(k%3)*100*time.Microsecond)
				if now.Sub(start) > span {
					break
				}
				for _, e := range src.dueTimers(now) {
					require.NoError(t, src.Fire(context.Background(), e.ns, e.source, e.event))
				}
			}
			require.InDelta(t, int(span/interval), pub.count(), 1, "a %s timer over %s", interval, span)
		})
	}
}
