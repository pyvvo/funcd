package funcd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// flakyKV is the platform's KV engine, unreachable while down.
type flakyKV struct {
	kvstore.KV
	down atomic.Bool
}

func (k *flakyKV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if k.down.Load() {
		return nil, false, fault.Unavailablef("kvstore.test", "the engine is closed")
	}
	return k.KV.Get(ctx, key)
}

// flakyBlob is the platform's blob store, unreachable while down.
type flakyBlob struct {
	blob.Bucket
	down atomic.Bool
}

func (b *flakyBlob) Exists(ctx context.Context, key string) (bool, error) {
	if b.down.Load() {
		return false, fault.Unavailablef("blob.test", "the storage is unreachable")
	}
	return b.Bucket.Exists(ctx, key)
}

// healthPlatform runs an in-memory platform on kv and b, with fast storage probes and a runtime that starts no process.
func healthPlatform(t *testing.T, kv kvstore.KV, b blob.Bucket) *Platform {
	t.Helper()
	p, err := New(InMemory(), WithoutLogCompaction(), WithRuntime(&recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}),
		WithKVStore(kv), WithBlob(b), WithPacing(Pacing{StorageProbeInterval: 20 * time.Millisecond, StorageProbeTimeout: 10 * time.Millisecond}))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = p.Shutdown(context.Background())
	})
	return p
}

// dependencies asks the platform's invoke socket of fn for GET /health/dependencies, as a new shim's readiness does.
func dependencies(t *testing.T, p *Platform, fn v1.ObjectName) (int, *local.DependencyReport) {
	t.Helper()
	sock, err := p.invokeMgr.SocketFor("default", fn)
	require.NoError(t, err)
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	defer c.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local/health/dependencies", http.NoBody)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusServiceUnavailable {
		return resp.StatusCode, nil
	}
	var rep local.DependencyReport
	require.NoError(t, json.Unmarshal(body, &rep))
	return resp.StatusCode, &rep
}

// todoParts stores the KVStore todo-store, the Bucket todo-files and the Function todo-api that binds both.
func todoParts(t *testing.T, p *Platform) {
	t.Helper()
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	ks.Name, ks.Namespace, ks.ResourceGroup = "todo-store", "default", "rg1"
	ks.Spec.Tables = []v1.KVTable{{Name: "todos", Owner: "todo-api"}}
	create(t, p.cfg.store, ks)
	b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	b.Name, b.Namespace, b.ResourceGroup = "todo-files", "default", "rg1"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "attachments", Owner: "todo-api"}}
	create(t, p.cfg.store, b)
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "todo-api", "default", "rg1"
	fn.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://todo-api:1",
		KV:   []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}},
		Blob: []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}}}
	create(t, p.cfg.store, fn)
}

// storeReady reads kind/name's phase and Ready condition.
func storeReady(t *testing.T, p *Platform, kind v1.Kind, name v1.ObjectName) (v1.Phase, v1.Condition, string) {
	t.Helper()
	obj, err := p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	require.NoError(t, err)
	st := obj.(v1.StatusObject).GetStatus()
	c, _ := st.Conditions.Get("Ready")
	if c.ObservedGeneration != obj.GetObjectMeta().Generation {
		return "", v1.Condition{}, ""
	}
	return st.Phase, c, obj.GetObjectMeta().ResourceVersion
}

// scenario: health-storage-down — the KV probe fails ⇒ within health.storageProbeInterval every KVStore is Degraded,
// Ready=False StorageUnreachable, and todo-api fails its dependency check; the probe passes again ⇒ all Ready. The same
// for the blob probe and every Bucket; a new Bucket is Ready=True at its generation after one pass; while the result
// does not change, no status is written.
func TestScenarioHealthStorageDown(t *testing.T) {
	t.Parallel()
	kv := &flakyKV{KV: kvmemory.New()}
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	b := &flakyBlob{Bucket: mem}
	p := healthPlatform(t, kv, b)
	todoParts(t, p)
	const wait, tick = 5 * time.Second, 5 * time.Millisecond
	readyIs := func(kind v1.Kind, name v1.ObjectName, phase v1.Phase, status v1.ConditionStatus, reason, msg string) func() bool {
		return func() bool {
			ph, c, _ := storeReady(t, p, kind, name)
			return ph == phase && c.Status == status && c.Reason == reason && c.Message == msg
		}
	}
	checkIs := func(code int, want *local.DependencyReport) func() bool {
		return func() bool {
			got, rep := dependencies(t, p, "todo-api")
			return got == code && (want == nil) == (rep == nil) && (want == nil || *want == *rep)
		}
	}

	require.Eventually(t, readyIs(v1.KindKVStore, "todo-store", v1.PhaseReady, v1.ConditionTrue, "", ""), wait, tick)
	require.Eventually(t, readyIs(v1.KindBucket, "todo-files", v1.PhaseReady, v1.ConditionTrue, "", ""), wait, tick,
		"a new Bucket is Ready at its generation after one pass")
	require.Eventually(t, checkIs(http.StatusOK, nil), wait, tick)
	_, _, rv := storeReady(t, p, v1.KindKVStore, "todo-store")
	time.Sleep(100 * time.Millisecond)
	_, _, again := storeReady(t, p, v1.KindKVStore, "todo-store")
	require.Equal(t, rv, again, "five probes with the same result write no status")

	kv.down.Store(true)
	require.Eventually(t, readyIs(v1.KindKVStore, "todo-store", v1.PhaseDegraded, v1.ConditionFalse, "StorageUnreachable",
		"kvstore.test: the engine is closed"), wait, tick)
	require.Eventually(t, checkIs(http.StatusServiceUnavailable, &local.DependencyReport{Kind: "kv", Binding: "store",
		Reason: "StorageUnreachable", Message: "kvstore.test: the engine is closed"}), wait, tick)
	phase, _, _ := storeReady(t, p, v1.KindBucket, "todo-files")
	require.Equal(t, v1.PhaseReady, phase, "the blob store is still reachable")

	kv.down.Store(false)
	require.Eventually(t, readyIs(v1.KindKVStore, "todo-store", v1.PhaseReady, v1.ConditionTrue, "", ""), wait, tick)
	require.Eventually(t, checkIs(http.StatusOK, nil), wait, tick)

	b.down.Store(true)
	require.Eventually(t, readyIs(v1.KindBucket, "todo-files", v1.PhaseDegraded, v1.ConditionFalse, "StorageUnreachable",
		"blob.test: the storage is unreachable"), wait, tick)
	require.Eventually(t, checkIs(http.StatusServiceUnavailable, &local.DependencyReport{Kind: "blob", Binding: "files",
		Reason: "StorageUnreachable", Message: "blob.test: the storage is unreachable"}), wait, tick)

	b.down.Store(false)
	require.Eventually(t, readyIs(v1.KindBucket, "todo-files", v1.PhaseReady, v1.ConditionTrue, "", ""), wait, tick)
	require.Eventually(t, checkIs(http.StatusOK, nil), wait, tick)
}

// The platform's dependency check (ADR-0215 Decision 3) reads the cedar PDP through the KV Facade: a forbid Policy on
// todo-api's kv::read is a Forbidden report naming the binding, and deleting it passes again.
func TestDependencyCheckReadsThePolicies(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	p := healthPlatform(t, kvmemory.New(), mem)
	todoParts(t, p)
	require.Eventually(t, func() bool {
		code, _ := dependencies(t, p, "todo-api")
		return code == http.StatusOK
	}, 5*time.Second, 5*time.Millisecond, "the binding grants the read")

	pol := &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy}}
	pol.Name, pol.Namespace, pol.ResourceGroup = "forbid-read", "default", "rg1"
	pol.Spec.Cedar = `forbid(principal == Function::"default/todo-api", action == Action::"kv::read", resource);`
	create(t, p.cfg.store, pol)
	var rep *local.DependencyReport
	require.Eventually(t, func() bool {
		var code int
		code, rep = dependencies(t, p, "todo-api")
		return code == http.StatusServiceUnavailable
	}, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, "kv", rep.Kind)
	require.Equal(t, "store", rep.Binding)
	require.Equal(t, "Forbidden", rep.Reason)

	require.NoError(t, p.cfg.store.Delete(context.Background(), v1.KindPolicy.GVK(), "default", "forbid-read", ""))
	require.Eventually(t, func() bool {
		code, _ := dependencies(t, p, "todo-api")
		return code == http.StatusOK
	}, 5*time.Second, 5*time.Millisecond)
}
