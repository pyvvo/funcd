//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// shimPlatform boots an InMemory platform running real functions on the process-driver
// shim (ADR-0030) with the data plane (ADR-0033) live, and returns an SDK client + the
// data-plane base URL. Node-gated.
func shimPlatform(t *testing.T) (*sdk.Client, string, string) {
	t.Helper()
	shim := langmod.NodeShim(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the data-plane node lane")
	}

	p, err := funcd.New(funcd.InMemory(), funcd.WithRuntimeShim(node, shim))
	require.NoError(t, err)
	require.NotEmpty(t, p.DataPlaneAddr())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return c, "http://" + p.DataPlaneAddr(), filepath.Dir(shim)
}

// writeArtifact creates a handler.mjs returning a JSON body and returns its file:// URI.
func writeArtifact(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(p, []byte("export function handle(_, e) { return { echoed: e }; }\n"), 0o600))
	return "file://" + p
}

func applyFn(t *testing.T, c *sdk.Client, name string, scaling v1.Scaling, replicas int, artifact string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = artifact
	fn.Spec.Replicas = replicas
	fn.Spec.Scaling = scaling
	_, err := c.Apply(context.Background(), fn)
	require.NoError(t, err)
}

func phaseOf(t *testing.T, c *sdk.Client, name string) v1.Phase {
	t.Helper()
	got, err := c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return got.(*v1.Function).Status.Phase
}

// scenario: http-invokes-warm-function + control-plane-unaffected (ADR-0033).
func TestScenarioDataPlaneInvokesWarmFunction(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)
	applyFn(t, c, "warm", v1.Scaling{MinReplicas: 1}, 1, writeArtifact(t))

	require.Eventually(t, func() bool { return phaseOf(t, c, "warm") == v1.PhaseReady },
		15*time.Second, 50*time.Millisecond, "a MinReplicas=1 function reconciles to Ready")

	// control-plane-unaffected: the API still works on Addr().
	got, err := c.Get(context.Background(), v1.KindFunction, "default", "warm")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("warm"), got.GetName())

	resp, err := http.Post(dpURL+"/function/warm", "application/json", strings.NewReader(`{"hi":1}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "data-plane invocation reaches the warm function: %s", body)
	require.Contains(t, string(body), "echoed", "the handler ran and returned its body")
}

// scenario: http-wakes-cold-function (ADR-0033) — a scale-to-zero function Idle at zero is
// woken by a data-plane request (the B1 proof: reachable while Idle, no programmed route).
func TestScenarioDataPlaneWakesColdFunction(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)
	applyFn(t, c, "cold", v1.Scaling{MinReplicas: 0, IdleTimeout: time.Hour}, 0, writeArtifact(t))

	// settles scaled to zero (Idle, no running worker).
	require.Eventually(t, func() bool {
		ph := phaseOf(t, c, "cold")
		return ph == v1.PhaseIdle || ph == v1.PhasePending
	}, 10*time.Second, 50*time.Millisecond, "a scale-to-zero function with 0 replicas settles Idle")

	resp, err := http.Post(dpURL+"/function/cold", "application/json", strings.NewReader(`{"wake":true}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the cold function was woken and served: %s", body)
	require.Contains(t, string(body), "echoed")
}

// NOTE (ADR-0108): the former `timer-wakes-cold-function` e2e is removed — a v2 timer EventSource
// PUBLISHES a named event rather than invoking/waking a function directly. The timer → wake-a-cold-function
// behavior is restored end-to-end by ADR-0109 as `timer event → Sensor function: action → wake` (F69).

// scenario: unknown-function-404 (ADR-0033) — a data-plane request to a non-existent
// function is rejected without waking anything.
func TestScenarioDataPlaneUnknownFunction(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)
	_ = c
	resp, err := http.Post(dpURL+"/function/ghost", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "unknown function → 404")
}
