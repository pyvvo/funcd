package funcd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/gateway"
)

// scenario: inmemory-boots — New(InMemory()) returns a platform with every port wired.
func TestScenarioInMemoryBoots(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	require.NotNil(t, p)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.NotNil(t, p.cfg.store)
	require.NotNil(t, p.cfg.blob)
	require.NotNil(t, p.cfg.bus)
	require.NotNil(t, p.cfg.runtime)
	require.NotNil(t, p.cfg.gateway)
	require.NotNil(t, p.cfg.logger)
	require.NotNil(t, p.cfg.telemetry)
}

// scenario: inmemory-ports-roundtrip — the booted platform's store, blob, bus, and
// gateway all work on real code paths (no root/containerd/network).
func TestScenarioInMemoryPortsRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(ctx) })

	// store: Create + Get a typed object.
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cfg, ok := obj.(*v1.ConfigMap)
	require.True(t, ok)
	cfg.Name = "harness"
	cfg.Namespace = "default"
	cfg.ResourceGroup = "rg1"
	_, err = p.cfg.store.Create(ctx, cfg)
	require.NoError(t, err)
	got, err := p.cfg.store.Get(ctx, v1.KindConfigMap.GVK(), "default", "harness")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("harness"), got.GetObjectMeta().Name)

	// blob: Put + Get bytes.
	require.NoError(t, p.cfg.blob.Put(ctx, "k", []byte("v")))
	data, err := p.cfg.blob.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v"), data)

	// bus: Publish + Subscribe delivers.
	sub, err := p.cfg.bus.Subscribe(ctx, bus.Subject("harness.subj"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, p.cfg.bus.Publish(ctx, bus.Subject("harness.subj"), []byte("hi")))
	select {
	case msg := <-sub.C():
		require.Equal(t, []byte("hi"), msg.Data)
	case <-time.After(2 * time.Second):
		t.Fatal("bus did not deliver the published message")
	}

	// gateway: program a route + proxy to a test upstream.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	require.NoError(t, p.cfg.gateway.ProgramRoutes(ctx, []gateway.Route{
		{ID: "default/h", PathPrefix: "/h", Upstream: upstream.URL},
	}))
	rec := httptest.NewRecorder()
	p.cfg.gateway.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/h/x", nil))
	require.Equal(t, http.StatusOK, rec.Result().StatusCode)
}

// scenario: missing-required-dep — New() with no preset returns fault.Invalid naming
// the missing dependency and a nil *Platform (realizes ADR-0002 facade-missing-dep).
func TestScenarioMissingRequiredDep(t *testing.T) {
	t.Parallel()
	p, err := New()
	require.Error(t, err)
	require.Nil(t, p)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "store is required")
}

// Issue #41: an invoke socket dir too long for a Unix socket path fails New, instead of every function
// coming up Ready without its local API.
func TestIssue41_RejectsInvokeSocketDirOverUnixLimit(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithInvokeSocketDir(filepath.Join(t.TempDir(), strings.Repeat("d", 100))))
	if p != nil {
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	}
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "Unix socket limit")
}

// scenario: run-shutdown-lifecycle — Run returns nil on ctx cancel; concurrent
// Shutdown is idempotent (sync.Once) and the bus is closed afterwards.
func TestScenarioRunShutdownLifecycle(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "Run returns nil on graceful ctx cancel")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	// Second Shutdown is idempotent (no panic, no double-close).
	require.NoError(t, p.Shutdown(context.Background()))

	// The bus is closed: a Publish on the closed bus now fails.
	require.Error(t, p.cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")))
}

// WithNodePlatform (ADR-0145) sets the platform the scheduler and the materializer share; the default is the
// daemon's own, and a malformed one is refused.
func TestWithNodePlatform(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithNodePlatform(v1.PlatformLinuxAMD64))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, v1.PlatformLinuxAMD64, p.cfg.nodePlatform)

	d, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	require.Equal(t, v1.HostPlatform(), d.cfg.nodePlatform)

	_, err = New(InMemory(), WithNodePlatform("linux"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
