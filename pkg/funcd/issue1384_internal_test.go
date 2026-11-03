package funcd

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/provider"
)

// countedEngine is a ready Lake engine runtime that counts what it is asked: a Converge starts an engine, a Teardown
// stops it.
type countedEngine struct {
	mu      sync.Mutex
	running bool
	starts  int
	stops   int
}

func (e *countedEngine) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		e.running = true
		e.starts++
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: "127.0.0.1:1"}, nil
}

func (e *countedEngine) Teardown(context.Context, provider.ProviderRef) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		e.running = false
		e.stops++
	}
	return nil
}

func (e *countedEngine) state() (running bool, starts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running, e.starts
}

// gatedPut is the shared blob store whose first Put of a key ending in file, once armed, tells reached and waits for
// proceed: a load held open.
type gatedPut struct {
	blob.Bucket
	file             string
	reached, proceed chan struct{}
	once             sync.Once
	armed            chan struct{}
}

func (b *gatedPut) Put(ctx context.Context, key string, data []byte, o blob.PutOptions) error {
	select {
	case <-b.armed:
		if strings.HasSuffix(key, b.file) {
			b.once.Do(func() {
				close(b.reached)
				<-b.proceed
			})
		}
	default:
	}
	return b.Bucket.Put(ctx, key, data, o)
}

func (*gatedPut) Close() error { return nil }

// Issue #1384: a Restore of a Lake in place loads it under the hold with no engine: the engine stops before the first
// put, no pass starts one during the load, its external path answers the hold's 503 (ADR-0290) rather than losing its edge
// entry, and after the release the next engine starts, which reads the copy.
func TestIssue1384_ARestoreLoadsALakeWithNoEngine(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &gatedPut{Bucket: mem, file: "/lake/main/t/f1.parquet", reached: make(chan struct{}), proceed: make(chan struct{}), armed: make(chan struct{})}
	engine := &countedEngine{}
	const catalog, file = "lake-catalogs/orders/lake-files/lake/_ducklake/catalog.db", "s3/orders/lake-files/lake/main/t/f1.parquet"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared), WithLakeProviderRuntime(engine)}, setup: func(r *wbRig) {
		r.lakeOn("lake-files", map[string]string{catalog: "C1", file: "f1"})
		obj, err := r.st.Get(r.ctx, v1.KindLake.GVK(), "orders", "lake")
		require.NoError(t, err)
		lake := obj.(*v1.Lake)
		lake.Spec.Ingress = &v1.Ingress{Enabled: new(true), Path: "/lake/lake"}
		_, err = r.st.Update(r.ctx, lake)
		require.NoError(t, err)
	}})
	proceed := sync.OnceFunc(func() { close(shared.proceed) })
	t.Cleanup(proceed)
	edge := func() *http.Response {
		req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, "http://"+r.p.DataPlaneAddr()+"/lake/lake/x", http.NoBody)
		require.NoError(t, err)
		req.Host = "orders.localhost"
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp
	}
	lakeStatus := func() *v1.Lake {
		obj, err := r.st.Get(r.ctx, v1.KindLake.GVK(), "orders", "lake")
		require.NoError(t, err)
		return obj.(*v1.Lake)
	}
	require.Eventually(t, func() bool { return lakeStatus().Status.Phase == v1.PhaseReady }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return edge().StatusCode != http.StatusNotFound }, 10*time.Second, 10*time.Millisecond, "its edge entry is programmed")
	r.create(r.dev, r.schedule("lake-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Lakes: []v1.ObjectName{"lake"}}
	}))
	r.create(r.dev, r.backupObj("b1", "lake-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	require.NoError(t, mem.Put(r.ctx, catalog, []byte("C2"), blob.PutOptions{}))

	close(shared.armed)
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}))
	select {
	case <-shared.reached:
	case <-time.After(15 * time.Second):
		t.Fatalf("the load never reached its first put\n%s", r.logs.String())
	}
	running, starts := engine.state()
	require.False(t, running, "the engine stopped before the first put")
	require.True(t, r.hold.HeldIn("orders"), "and after the hold")
	c, _ := lakeStatus().Status.Conditions.Get("Ready")
	require.Equal(t, "Restoring", c.Reason)
	refused := edge()
	require.Equal(t, http.StatusServiceUnavailable, refused.StatusCode, "the edge entry stays through the stop")
	require.Equal(t, "30", refused.Header.Get("Retry-After"))
	r.still(func() bool {
		running, now := engine.state()
		return !running && now == starts
	}, "no pass starts an engine during the load")

	proceed()
	r.waitRestore("rewind", v1.RunSucceeded)
	require.False(t, r.hold.HeldIn("orders"))
	require.Eventually(t, func() bool {
		running, now := engine.state()
		return running && now == starts+1 && lakeStatus().Status.Phase == v1.PhaseReady
	}, 10*time.Second, 10*time.Millisecond, "an engine comes back after the release")
	require.Equal(t, "C1", r.blobAt(catalog), "and it reads the copy")
}

// scenario: lake-restored-in-place-answers-the-copy, its failure (ADR-0312) — a load that fails keeps the hold and drops the
// mark, so a new engine starts on what the store holds; and a new Restore after the release succeeds.
// oracle: docs/design/lake/restore-stop.fizz#AnEngineComesBack
func TestIssue1384_AFailedLoadKeepsTheHoldAndBringsAnEngineBack(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/lake/main/t/f1.parquet", fail: true}
	engine := &countedEngine{}
	const catalog, file = "lake-catalogs/orders/lake-files/lake/_ducklake/catalog.db", "s3/orders/lake-files/lake/main/t/f1.parquet"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared), WithLakeProviderRuntime(engine)}, setup: func(r *wbRig) {
		r.lakeOn("lake-files", map[string]string{catalog: "C1", file: "f1"})
	}})
	require.Eventually(t, func() bool { running, _ := engine.state(); return running }, 10*time.Second, 10*time.Millisecond)
	r.create(r.dev, r.schedule("lake-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Lakes: []v1.ObjectName{"lake"}}
	}))
	r.create(r.dev, r.backupObj("b1", "lake-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	_, starts := engine.state()

	shared.armed.Store(true)
	member := v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}
	r.create(r.admin, r.restoreObj("fails", "b1", member))
	r.waitRestore("fails", v1.RunFailed)
	require.True(t, r.hold.HeldIn("orders"), "a failed load keeps the hold")
	require.Eventually(t, func() bool {
		running, now := engine.state()
		return running && now == starts+1
	}, 10*time.Second, 10*time.Millisecond, "the mark goes with the failure, so an engine comes back")

	shared.armed.Store(false)
	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	r.create(r.admin, r.restoreObj("again", "b1", member))
	r.waitRestore("again", v1.RunSucceeded)
	require.Equal(t, []string{"C1", "f1"}, []string{r.blobAt(catalog), r.blobAt(file)})
	require.Eventually(t, func() bool {
		running, now := engine.state()
		return running && now == starts+2
	}, 10*time.Second, 10*time.Millisecond, "the Restore that succeeds replaces the engine too")
}

// ADR-0312 Decision 9: a Lake restored under a namespace's data hold, as after a platform restore: the Restore's own marker
// comes and goes on top of it, the engine stops for the load and comes back, and the data hold stays until its release.
func TestIssue1384_StopEngineUnderADataHold(t *testing.T) {
	t.Parallel()
	engine := &countedEngine{}
	const catalog, file = "lake-catalogs/orders/lake-files/lake/_ducklake/catalog.db", "s3/orders/lake-files/lake/main/t/f1.parquet"
	r := newWBRig(t, wbOptions{opts: []Option{WithLakeProviderRuntime(engine)}, setup: func(r *wbRig) {
		r.lakeOn("lake-files", map[string]string{catalog: "C1", file: "f1"})
	}})
	require.Eventually(t, func() bool { running, _ := engine.state(); return running }, 10*time.Second, 10*time.Millisecond)
	r.create(r.dev, r.schedule("lake-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Lakes: []v1.ObjectName{"lake"}}
	}))
	r.create(r.dev, r.backupObj("b1", "lake-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	_, starts := engine.state()
	require.NoError(t, r.p.cfg.blob.Put(r.ctx, catalog, []byte("C2"), blob.PutOptions{}))
	require.NoError(t, r.hold.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))

	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "C1", r.blobAt(catalog))
	require.True(t, r.hold.HeldIn("orders"), "the data hold stays until its release")
	require.Eventually(t, func() bool {
		running, now := engine.state()
		return running && now == starts+1
	}, 10*time.Second, 10*time.Millisecond, "the engine stopped and came back")
}
