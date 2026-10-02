package funcd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/gateway"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
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

// Issue #330: the temp invoke socket dir New creates when no WithInvokeSocketDir is set is removed by
// Shutdown, and by a New that fails after creating it.
func TestIssue330_TempInvokeSocketDirRemoved(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		p, err := New(InMemory())
		require.NoError(t, err)
		sock, err := p.invokeMgr.SocketFor("default", "fn")
		require.NoError(t, err)
		require.NoError(t, p.Shutdown(context.Background()))
		require.NoDirExists(t, filepath.Dir(sock))
	})
	t.Run("failed New", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), strings.Repeat("d", 100))
		require.NoError(t, os.Mkdir(tmp, 0o700))
		t.Setenv("TMPDIR", tmp)
		_, err := New(InMemory())
		require.ErrorContains(t, err, "Unix socket limit")
		left, err := filepath.Glob(filepath.Join(tmp, "funcd-invoke*"))
		require.NoError(t, err)
		require.Empty(t, left)
	})
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

// Issue #350: a platform built without WithWorkflow (funcdctl dev, any InMemory embedding) runs the workflow
// engine with the daemon's ADR-0094 defaults, so a step that never answers times out instead of hanging.
func TestIssue350_WorkflowDefaultsWithoutWithWorkflow(t *testing.T) {
	t.Parallel()
	daemon, err := platformconfig.Load("", platformconfig.Flags{})
	require.NoError(t, err)
	stepTimeout, err := time.ParseDuration(daemon.Workflow.DefaultStepTimeout)
	require.NoError(t, err)
	retention, err := time.ParseDuration(daemon.Workflow.Retention)
	require.NoError(t, err)

	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, stepTimeout, p.cfg.workflowStepTimeout)
	require.Equal(t, retention, p.workflowRetention)
	require.Equal(t, daemon.Workflow.PayloadLimit, p.cfg.workflowPayloadLimit)
	require.Equal(t, daemon.Workflow.DefaultRetry, p.cfg.workflowDefaultRetry)

	p, err = New(WithWorkflow("", 0, 0, 1, 0), InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Zero(t, p.cfg.workflowStepTimeout, "an explicit zero still means no step timeout, in any option order")
}

// Issue #94: a failed New releases what the options and the build acquired (ADR-0014: never a partial
// platform), so the caller can fix the cause and call New again with the same data dirs.
func TestIssue94_FailedNewReleasesResources(t *testing.T) {
	t.Parallel()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	controlAddr := free.Addr().String()
	require.NoError(t, free.Close())

	persist := []Option{
		WithWorkflow(t.TempDir(), time.Second, 0, 1, 0),
		WithDeadLetterQueue(t.TempDir(), 1, 0, 0),
	}
	var cfg *config
	capture := func(c *config) error { cfg = c; return nil }
	publish := func() error { return cfg.bus.Publish(context.Background(), bus.Subject("x"), []byte("y")) }

	p, err := New(append([]Option{InMemory(), capture, WithListenAddr(controlAddr), WithDataPlaneAddr(busy.Addr().String())}, persist...)...)
	require.ErrorContains(t, err, "bind data-plane listener")
	require.Nil(t, p)
	ln, err := net.Listen("tcp", controlAddr)
	require.NoError(t, err, "the control-plane port is released")
	require.NoError(t, ln.Close())
	require.Error(t, publish(), "the preset's bus is closed")

	_, err = New(InMemory(), capture, func(*config) error { return fault.Invalidf("test", "bad option") })
	require.Error(t, err)
	require.Error(t, publish(), "a failed option releases the preset's bus")

	p, err = New(append([]Option{InMemory()}, persist...)...)
	require.NoError(t, err, "a retry with the same data dirs succeeds")
	require.NoError(t, p.Shutdown(context.Background()))
}
