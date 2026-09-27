// Package chaos is the resource-guard / chaos test lane (ADR-0047): white-box tests on the
// embedded platform that fail when the control loop storms (CPU), leaks goroutines (RAM), or does
// not recover from a fault. It sits OUTSIDE the tests/e2e public-surface (e2e-boundary) rule — it
// may reach into internal/runtime (to kill a worker) — and is node-gated (a real worker runs the shim).
package chaos

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// harness is an embedded funcd platform with an INJECTED process runtime (so a chaos test can find
// a worker PID) + an SDK client + a file:// echo artifact. node-gated.
type harness struct {
	c   *sdk.Client
	rt  runtime.Runtime
	art string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH — the chaos lane needs a real worker")
	}
	shim := langmod.NodeShim(t)
	rt := process.New()
	p, err := funcd.New(funcd.InMemory(), funcd.WithRuntime(rt), funcd.WithRuntimeShim(node, shim))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	waitListening(t, p.Addr())

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)

	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle(_, e){ return { ok: true }; }\n"), 0o600))
	return &harness{c: c, rt: rt, art: "file://" + bundle}
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 10*time.Second, 50*time.Millisecond, "control plane %s did not come up", addr)
}

// deployReady applies a solo, always-warm function (minReplicas:1, no timer — a quiescent-able
// config) and waits for Ready.
func (h *harness) deployReady(t *testing.T, name string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = h.art
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	_, err := h.c.Apply(context.Background(), fn)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return h.phase(t, name) == v1.PhaseReady },
		30*time.Second, 100*time.Millisecond, "function %q did not reach Ready", name)
}

func (h *harness) get(t *testing.T, name string) *v1.Function {
	t.Helper()
	got, err := h.c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return got.(*v1.Function)
}

func (h *harness) phase(t *testing.T, name string) v1.Phase { return h.get(t, name).Status.Phase }

// rv returns the function's metadata.resourceVersion. It advances on every store write, so a
// reconcile storm makes it climb fast; quiescence makes it stop (ADR-0047).
func (h *harness) rv(t *testing.T, name string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(h.get(t, name).GetObjectMeta().ResourceVersion, 10, 64)
	require.NoError(t, err)
	return v
}
