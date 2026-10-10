package funcd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: call-answered-while-gate-fails (ADR-0221, issue #849) — a call to a Degraded pooled member whose Secret is
// deleted is held, and answered once its /health/members entry reads ready, while RevisionReady names the gate and no
// worker is created.
func TestScenarioCallAnsweredWhileGateFails(t *testing.T) {
	t.Parallel()
	r := newPooledRig(t, 0, 2*time.Second)
	r.secret(t)
	r.member(t, "reader", 1, bindCreds)
	require.Eventually(t, func() bool { return r.fn(t).Status.Phase == v1.PhaseReady }, 5*time.Second, 10*time.Millisecond, "reader is Ready")
	r.host.setMember("reader", "restarting")
	require.Eventually(t, func() bool { return r.fn(t).Status.Phase == v1.PhaseDegraded }, 5*time.Second, 10*time.Millisecond, "reader is Degraded")
	creates := r.host.poolCreates()

	r.del(t, v1.KindSecret, "creds")
	require.Eventually(t, func() bool {
		fn := r.fn(t)
		rr := condition(fn, "RevisionReady")
		return fn.Status.Phase == v1.PhaseDegraded && rr.Status == v1.ConditionFalse && rr.Reason == "SecretResolveFailed"
	}, 5*time.Second, 10*time.Millisecond, "a gate pass keeps reader Degraded")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answered := make(chan error, 1)
	go func() { answered <- r.wake(ctx) }()
	require.Never(t, func() bool { return len(answered) > 0 }, 300*time.Millisecond, 10*time.Millisecond, "the call is held")
	r.host.setMember("reader", "ready")
	require.NoError(t, <-answered, "the held call is answered")

	fn := r.fn(t)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, v1.ConditionFalse, condition(fn, "RevisionReady").Status)
	require.Equal(t, "SecretResolveFailed", condition(fn, "RevisionReady").Reason)
	require.Equal(t, creates, r.host.poolCreates(), "no worker is created")
}
