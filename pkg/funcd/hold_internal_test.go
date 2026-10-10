package funcd

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// bucketOrphans lists each <ns>/<bucket> holding objects without its Bucket, once, and skips a prefix that is not a
// pair of DNS labels; it deletes nothing.
func TestBucketOrphans(t *testing.T) {
	onEachSubstrate(t, func(t *testing.T, ctx context.Context, shared blob.Bucket, st store.Store) {
		createBucket(t, st, "team", "keep")
		for _, k := range []string{"s3/team/keep/a", "s3/team/gone/a", "s3/team/gone/b", "s3/Team_/x/y", "funclog/z"} {
			require.NoError(t, shared.Put(ctx, k, []byte("x"), blob.PutOptions{}))
		}
		got, err := bucketOrphans(ctx, shared, st)
		require.NoError(t, err)
		require.Equal(t, []v1.ObjectRef{{Kind: v1.KindBucket, Namespace: "team", Name: "gone"}}, got)
		require.Len(t, keys(t, shared), 5)
	})
}

func tmeta(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

func teamMeta(name string) v1.ObjectMeta {
	return v1.ObjectMeta{Namespace: "team", ResourceGroup: "rg1", Name: v1.ObjectName(name)}
}

// runnerCase is one ADR-0206 Decision 6 runner: seed gives it work before the held platform runs, still checks that
// it did none.
type runnerCase struct {
	name  string
	seed  func(t *testing.T, p *Platform)
	still func(t *testing.T, p *Platform)
}

// TestEveryRunnerConsultsHold: on a held platform every Decision 6 runner built so far, the App's included, stays
// still; it fails when one acts. ADR-0205, ADR-0208 and ADR-0209 add their backup loops' cases.
func TestEveryRunnerConsultsHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithHold(h),
		WithWorkflow("", defaultWorkflowStepTimeout, 50*time.Millisecond, defaultWorkflowRetry, defaultWorkflowPayloadLimit),
		WithDeadLetterQueue("", 2, 50*time.Millisecond, 0), WithBlobPollInterval(50*time.Millisecond),
		WithPacing(Pacing{ReferentPollInterval: 50 * time.Millisecond}))
	require.NoError(t, err)
	st := p.cfg.store
	runsIn := func(t *testing.T) []string {
		l, err := st.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{Namespace: "team"})
		require.NoError(t, err)
		var names []string
		for _, o := range l.Items {
			names = append(names, string(o.GetName()))
		}
		return names
	}
	sensorRuns := func(t *testing.T) {
		for _, name := range runsIn(t) {
			require.False(t, strings.HasPrefix(name, "s-"), "the Sensor started run %s", name)
		}
	}
	var r1Version string
	cases := []runnerCase{
		{name: "timers", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: teamMeta("tick"), Spec: v1.EventSourceSpec{
				Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}}})
		}, still: func(t *testing.T, p *Platform) {
			l, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{Namespace: "team"})
			require.NoError(t, err)
			require.Empty(t, l.Items, "a held timer fired")
		}},
		{name: "blob sources", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: teamMeta("inbox")},
				&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: teamMeta("files"), Spec: v1.EventSourceSpec{
					Blob: &v1.BlobSource{Bucket: "inbox", Events: []v1.BlobEvent{{Name: "new"}}}}})
			require.NoError(t, p.cfg.blob.Put(ctx, bucketPrefix("team", "inbox")+"a", []byte("a"), blob.PutOptions{}))
		}, still: func(t *testing.T, p *Platform) {
			pending, err := p.blobWatcher.Pending(ctx, "team", "files")
			require.NoError(t, err)
			require.Equal(t, map[string]int{"new": 1}, pending, "a held blob source polled")
		}},
		{name: "Sensors", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: teamMeta("s"), Spec: v1.SensorSpec{
				On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}, {Name: "file", Source: "files", Event: "new"}},
				Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}, {Name: "b", On: "file", Workflow: "wf"}}}})
		}, still: func(t *testing.T, p *Platform) {
			ev, err := eventing.NewNamedEvent("team", "tick", "t")
			require.NoError(t, err)
			require.NoError(t, p.eventFanout.Publish(ctx, ev))
			require.Eventually(t, func() bool {
				dls, err := p.deadLetters.List(ctx, "team")
				require.NoError(t, err)
				return len(dls) == 2 && strings.Contains(dls[0].Reason+dls[1].Reason, "held")
			}, 5*time.Second, 20*time.Millisecond, "the held Sensor parks the delivery")
			sensorRuns(t)
		}},
		{name: "runs", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.WorkflowRun{TypeMeta: tmeta(v1.KindWorkflowRun), ObjectMeta: teamMeta("r1"), Spec: v1.WorkflowRunSpec{Workflow: "wf"}})
			obj, err := st.Get(ctx, v1.KindWorkflowRun.GVK(), "team", "r1")
			require.NoError(t, err)
			r1Version = obj.GetObjectMeta().ResourceVersion
		}, still: func(t *testing.T, p *Platform) {
			obj, err := st.Get(ctx, v1.KindWorkflowRun.GVK(), "team", "r1")
			require.NoError(t, err)
			require.Equal(t, r1Version, obj.GetObjectMeta().ResourceVersion, "a held run moved")
		}},
		{name: "workflow retention", seed: func(t *testing.T, p *Platform) {
			require.NoError(t, p.workflowRuns.Put(ctx, &runstate.Record{Namespace: "team", Name: "no-run", Workflow: "wf", Phase: v1.RunRunning}))
		}, still: func(t *testing.T, p *Platform) {
			rec, err := p.workflowRuns.Get(ctx, "team", "no-run")
			require.NoError(t, err)
			require.False(t, rec.Terminal(), "the retention sweep closed a held record")
		}},
		{name: "dead-letter retention and replay", seed: func(t *testing.T, p *Platform) {
			payload, err := json.Marshal(eventing.CloudEvent{})
			require.NoError(t, err)
			require.NoError(t, p.deadLetters.Put(ctx, deadletter.DeadLetter{ID: "01JA0000000000000000000001", Namespace: "team",
				Sensor: "s", Source: "tick", Event: "t", Action: "a", Payload: payload, FailedAt: v1.NewTimestamp(time.Now().Add(-time.Hour))}))
		}, still: func(t *testing.T, p *Platform) {
			_, err := p.deadLetters.Get(ctx, "team", "01JA0000000000000000000001")
			require.NoError(t, err, "the retention sweep evicted a held dead letter")
			require.Equal(t, fault.Unavailable, fault.KindOf(p.sensorReconciler.Replay(ctx, "team", "01JA0000000000000000000001")))
			sensorRuns(t)
		}},
		{name: "App", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: teamMeta("app"),
				Spec: v1.AppSpec{KV: []v1.AppKVStore{{Name: "appstore"}}}})
		}, still: func(t *testing.T, p *Platform) {
			_, err := st.Get(ctx, v1.KindKVStore.GVK(), "team", "appstore")
			require.Equal(t, fault.NotFound, fault.KindOf(err), "a held App wrote a part")
			obj, err := st.Get(ctx, v1.KindApp.GVK(), "team", "app")
			require.NoError(t, err)
			require.Empty(t, obj.(*v1.App).Status.Phase, "a held App wrote its status")
		}},
		{name: "KV reclaims", seed: func(t *testing.T, p *Platform) {
			seedObjects(t, st, &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: teamMeta("orders")})
			require.NoError(t, p.cfg.kvStore.Put(ctx, "team/gone/t/1", []byte("x")))
			require.NoError(t, p.cfg.kvStore.Put(ctx, "team/orders/old/1", []byte("x")))
		}, still: func(t *testing.T, p *Platform) {
			for _, k := range []string{"team/gone/t/1", "team/orders/old/1"} {
				_, found, err := p.cfg.kvStore.Get(ctx, k)
				require.NoError(t, err)
				require.True(t, found, "a held reclaim dropped %s", k)
			}
		}},
		{name: "Bucket reclaim", seed: func(t *testing.T, p *Platform) {
			require.NoError(t, p.cfg.blob.Put(ctx, bucketPrefix("team", "gone")+"x", []byte("x"), blob.PutOptions{}))
		}, still: func(t *testing.T, p *Platform) {
			ok, err := p.cfg.blob.Exists(ctx, bucketPrefix("team", "gone")+"x")
			require.NoError(t, err)
			require.True(t, ok, "the held boot purged a Bucket's objects")
		}},
	}
	for _, c := range cases {
		c.seed(t, p)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		pending, err := p.blobWatcher.Pending(ctx, "team", "files")
		return err == nil && pending["new"] == 1
	}, 5*time.Second, 10*time.Millisecond, "the blob source registers")
	// The timers tick every 100 ms; the sweeps, the held requeues and the blob polls every 50 ms.
	time.Sleep(500 * time.Millisecond)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.still(t, p) })
	}
}
