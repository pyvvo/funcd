package workflow

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

var timestampRe = regexp.MustCompile(v1.TimestampPattern)

// scenario: step-times-are-timestamps — a run driven over the fake dispatcher mirrors each step's startedAt and
// endedAt as timestamps, endedAt not before startedAt and both inside the run's window; a Pending step carries
// neither key.
func TestStepTimesAreTimestamps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	seedRun(t, s, "orders-01", "orders", `{}`)
	rstate, err := wbadger.New(wbadger.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rstate.Close() })
	eng, err := New(Deps{Runs: rstate, Dispatch: newFake()})
	require.NoError(t, err)

	before := time.Now().Truncate(time.Millisecond)
	_, run := reconcileRun(t, ctx, NewRunReconciler(s, eng, nil, nil, 0), s, "orders-01")
	after := time.Now()
	require.Equal(t, runSucceeded, run.Status.Phase)
	require.Len(t, run.Status.Steps, 1)
	a := run.Status.Steps[0]
	start, end := time.Time(a.StartedAt), time.Time(a.EndedAt)
	require.False(t, start.Before(before) || end.After(after), "[%v, %v] not in [%v, %v]", start, end, before, after)
	require.False(t, end.Before(start))
	b, err := json.Marshal(a)
	require.NoError(t, err)
	var wire struct {
		StartedAt string `json:"startedAt"`
		EndedAt   string `json:"endedAt"`
	}
	require.NoError(t, json.Unmarshal(b, &wire))
	require.Regexp(t, timestampRe, wire.StartedAt)
	require.Regexp(t, timestampRe, wire.EndedAt)

	pending := &v1.WorkflowRun{}
	mirror(pending, &runstate.Record{Phase: runRunning, Steps: []runstate.StepState{{Name: "a", Phase: v1.StepPending}}})
	b, err = json.Marshal(pending.Status.Steps[0])
	require.NoError(t, err)
	require.NotContains(t, string(b), "startedAt")
	require.NotContains(t, string(b), "endedAt")
}

// scenario: every-timestamp-utc-millisecond — a step stamped at …35.965999999 reads …35.965Z, and the kv-migration
// ConfigMap's COMPLETED_AT is in the form.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	ns := time.Date(2026, 10, 7, 20, 3, 35, 965999999, time.UTC).UnixNano()
	require.Equal(t, "2026-10-07T20:03:35.965Z", stepTime(ns).String())
	require.True(t, stepTime(0).IsZero())

	ctx := context.Background()
	s := newStore(t)
	before := time.Now().Truncate(time.Millisecond)
	require.NoError(t, MarkKVStoresOnce(ctx, s))
	after := time.Now()
	obj, err := s.Get(ctx, v1.KindConfigMap.GVK(), KVMigrationNamespace, KVMigrationRecord)
	require.NoError(t, err)
	at := obj.(*v1.ConfigMap).Spec.Data["COMPLETED_AT"]
	require.Regexp(t, timestampRe, at)
	parsed, err := time.Parse(v1.TimestampLayout, at)
	require.NoError(t, err)
	require.False(t, parsed.Before(before) || parsed.After(after), "%v not in [%v, %v]", parsed, before, after)
}
