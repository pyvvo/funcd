package funcd

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/platform/clock"
	funcdconfig "github.com/pyvvo/funcd/internal/platform/config"
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

// TestEveryRunnerConsultsHold: on a held platform every Decision 6 runner built so far, the App's and the platform
// backup's included, stays still; it fails when one acts. ADR-0208 and ADR-0209 add their backup loops' cases.
func TestEveryRunnerConsultsHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	quiet := slog.New(slog.DiscardHandler)
	// The platform backup runner as cmd/funcd builds it, its Hold the daemon's (ADR-0205 and ADR-0206, wired by ADR-0207).
	tg, err := backup.Open(ctx, backup.Config{Target: gocloud.FileURL(t.TempDir()), DataDir: dir, Retention: backup.Retention{Hourly: 1}, Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	sealer, err := envelope.New(envelope.Config{None: true, NoSecrets: true, Logger: quiet})
	require.NoError(t, err)
	backups, err := runner.New(runner.Config{Target: tg, Sealer: sealer, Hold: h, Logger: quiet,
		Times: funcdconfig.BackupTimes{Interval: 50 * time.Millisecond, RPO: 100 * time.Millisecond, RetryInterval: 50 * time.Millisecond}})
	require.NoError(t, err)
	p, err := New(InMemory(), WithLogger(quiet), WithHold(h),
		WithWorkflow("", defaultWorkflowStepTimeout, 50*time.Millisecond, defaultWorkflowRetry, defaultWorkflowPayloadLimit),
		WithDeadLetterQueue("", 2, 50*time.Millisecond, 0), WithBlobPollInterval(50*time.Millisecond),
		WithPacing(Pacing{ReferentPollInterval: 50 * time.Millisecond}), WithPlatformBackup(backups))
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
				Spec: v1.AppSpec{KV: []v1.KVStoreEntry{{Name: "appstore"}}}})
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
		{name: "platform backup", seed: func(*testing.T, *Platform) {}, still: func(t *testing.T, _ *Platform) {
			entries, err := tg.List(ctx)
			require.NoError(t, err)
			require.Empty(t, entries, "a held platform backup wrote a generation")
			st, err := backups.Status(ctx)
			require.NoError(t, err)
			require.True(t, st.Held)
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

// nsMeta is teamMeta in namespace ns.
func nsMeta(ns v1.NamespaceName, name string) v1.ObjectMeta {
	return v1.ObjectMeta{Namespace: ns, ResourceGroup: "rg1", Name: v1.ObjectName(name)}
}

// TestEveryRunnerConsultsHoldInNamespace (ADR-0222 Decision 8): with team held by a restore's data marker and web
// free, every per-namespace runner acts in web only: timers and Sensors start runs, a run moves, a blob source polls,
// an App writes its part, a schedule's slot creates a Backup, and a dead-letter replay is not refused; in team none
// does.
func TestEveryRunnerConsultsHoldInNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{"team"}, hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	clk := clock.NewManual(time.Now().UTC())
	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithHold(h),
		WithWorkflow("", defaultWorkflowStepTimeout, 50*time.Millisecond, defaultWorkflowRetry, defaultWorkflowPayloadLimit),
		WithDeadLetterQueue("", 2, 50*time.Millisecond, 0), WithBlobPollInterval(50*time.Millisecond),
		WithPacing(Pacing{ReferentPollInterval: 50 * time.Millisecond, SupervisionPeriod: 50 * time.Millisecond}), withBackupClock(clk, nil))
	require.NoError(t, err)
	st := p.cfg.store
	versions := map[v1.NamespaceName]string{}
	for _, ns := range []v1.NamespaceName{"team", "web"} {
		seedObjects(t, st,
			&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: nsMeta(ns, "tick"), Spec: v1.EventSourceSpec{
				Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}}},
			&v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: nsMeta(ns, "s"), Spec: v1.SensorSpec{
				On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}}, Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}}}},
			&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: nsMeta(ns, "inbox")},
			&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: nsMeta(ns, "files"), Spec: v1.EventSourceSpec{
				Blob: &v1.BlobSource{Bucket: "inbox", Events: []v1.BlobEvent{{Name: "new"}}}}},
			&v1.WorkflowRun{TypeMeta: tmeta(v1.KindWorkflowRun), ObjectMeta: nsMeta(ns, "r1"), Spec: v1.WorkflowRunSpec{Workflow: "wf"}},
			&v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: nsMeta(ns, "app"), Spec: v1.AppSpec{KV: []v1.KVStoreEntry{{Name: "appstore"}}}},
			&v1.BackupSchedule{TypeMeta: tmeta(v1.KindBackupSchedule), ObjectMeta: nsMeta(ns, "every-minute"), Spec: v1.BackupScheduleSpec{
				Schedule: "* * * * *", Scope: v1.BackupScope{KVStores: []v1.ObjectName{"appstore"}},
				Target: v1.BackupTarget{URL: "s3://unreachable", CredentialsSecret: "none"}, Encryption: v1.BackupEncryption{Recipients: []string{"a", "b"}}}},
		)
		require.NoError(t, p.cfg.blob.Put(ctx, bucketPrefix(ns, "inbox")+"a", []byte("a"), blob.PutOptions{}))
		obj, err := st.Get(ctx, v1.KindWorkflowRun.GVK(), ns, "r1")
		require.NoError(t, err)
		versions[ns] = obj.GetObjectMeta().ResourceVersion
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	clk.Advance(2 * time.Minute)

	sensorRuns := func(ns v1.NamespaceName) int {
		l, err := st.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{Namespace: ns})
		require.NoError(t, err)
		n := 0
		for _, o := range l.Items {
			if strings.HasPrefix(string(o.GetName()), "s-") {
				n++
			}
		}
		return n
	}
	acted := func(ns v1.NamespaceName) map[string]bool {
		out := map[string]bool{}
		out["timers and Sensors"] = sensorRuns(ns) > 0
		obj, err := st.Get(ctx, v1.KindWorkflowRun.GVK(), ns, "r1")
		require.NoError(t, err)
		out["runs"] = obj.GetObjectMeta().ResourceVersion != versions[ns]
		pending, err := p.blobWatcher.Pending(ctx, ns, "files")
		require.NoError(t, err)
		out["blob sources"] = pending["new"] == 0 && len(pending) == 1
		_, err = st.Get(ctx, v1.KindKVStore.GVK(), ns, "appstore")
		out["App"] = err == nil
		l, err := st.List(ctx, v1.KindBackup.GVK(), store.ListOptions{Namespace: ns})
		require.NoError(t, err)
		out["schedule slots"] = len(l.Items) > 0
		out["dead-letter replay"] = fault.KindOf(p.sensorReconciler.Replay(ctx, ns, "01JA0000000000000000000001")) != fault.Unavailable
		return out
	}
	all := map[string]bool{"timers and Sensors": true, "runs": true, "blob sources": true, "App": true, "schedule slots": true,
		"dead-letter replay": true}
	require.Eventually(t, func() bool { return reflect.DeepEqual(all, acted("web")) }, 10*time.Second, 20*time.Millisecond,
		"every per-namespace runner acts in web")
	require.Eventually(t, func() bool {
		pending, err := p.blobWatcher.Pending(ctx, "team", "files")
		return err == nil && pending["new"] == 1
	}, 5*time.Second, 10*time.Millisecond, "team's blob source registers")
	for name, did := range acted("team") {
		require.False(t, did, "%s acted in the held namespace", name)
	}
}

// TestDataPlaneAsksTheNamespaceHold (ADR-0290 Decision 5): the daemon hands its hold to the data plane, so a data marker
// that restore run wrote refuses calls into its namespace until the release, and another namespace is not refused.
func TestDataPlaneAsksTheNamespaceHold(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{"team"}, hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithHold(h))
	require.NoError(t, err)
	for _, ns := range []v1.NamespaceName{"team", "web"} {
		seedObjects(t, p.cfg.store,
			&v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(ns)}, Spec: v1.NamespaceSpec{
				DefaultExposure: v1.ExposureImplicit, EdgeDefaults: &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthOpen}}}},
			&v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: nsMeta(ns, "api"), Spec: v1.FunctionSpec{
				Runtime: "nodejs22", Handler: "handle", Image: "file:///fn"}})
	}
	call := func(ns string) (int, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/function/api", nil)
		req.Header.Set("X-Funcd-Namespace", ns)
		rec := httptest.NewRecorder()
		p.dataPlaneServer.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := call("team")
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	require.Contains(t, body, "namespace team is held for its data since ")
	_, body = call("web")
	require.Contains(t, body, "activating web/api", "a call into web passes the hold and reaches the activator")
	require.NoError(t, h.ReleaseNamespace("team", hold.ReasonData))
	_, body = call("team")
	require.Contains(t, body, "activating team/api", "after the release a call into team reaches the activator")
}
