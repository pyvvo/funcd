//go:build e2e

package funcd_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario (e2e): scheduled-workflow-start (F68, ADR-0109) — a REAL `timer:` EventSource + a Sensor
// `workflow:` action bound to its named event. Each tick publishes a named CloudEvent on the Fanout; the
// Sensor creates a WorkflowRun of the target workflow, which the engine drives to Succeeded. Proves the
// whole eventing→sensor→workflow chain end-to-end (F72 + F69 + F68) over the real shim platform.
func TestScenarioE2EScheduledWorkflow(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping the scheduled-workflow lane")
	}
	shim := langmod.NodeShim(t)

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New(nil)),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim("node", shim), funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)

	// A 1-step workflow whose step ignores its input (so the timer's empty event data is accepted).
	src, layout := t.TempDir(), t.TempDir()
	writeStep(t, src, "sc-tick", `export const handle = (ctx, e) => ({ ok: true });`)
	img := pushStepImage(t, layout, src, "sc-tick")
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "sched", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingIsolated, MinReplicas: 1},
			Steps:   []v1.WorkflowStep{{Name: "tick", Function: &v1.FunctionStep{Image: img}}},
		},
	}
	_, err = c.Apply(context.Background(), wf)
	require.NoError(t, err)
	waitMaterializedReady(t, c, "sched-tick")

	// A fast timer EventSource + a Sensor binding its `beat` event to a `workflow:` action.
	es := &v1.EventSource{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		ObjectMeta: v1.ObjectMeta{Name: "clock", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.EventSourceSpec{Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "beat", Interval: 300 * time.Millisecond}}}},
	}
	_, err = c.Apply(context.Background(), es)
	require.NoError(t, err)
	sen := &v1.Sensor{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: "run-sched", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "beat", Source: "clock", Event: "beat"}},
			Do: []v1.Action{{Name: "start", On: "beat", Workflow: "sched"}},
		},
	}
	_, err = c.Apply(context.Background(), sen)
	require.NoError(t, err)

	// A tick fires → the Sensor creates a `sched` WorkflowRun → the engine drives it to Succeeded.
	require.Eventually(t, func() bool {
		objs, lerr := c.List(context.Background(), v1.KindWorkflowRun, "default")
		if lerr != nil {
			return false
		}
		for _, o := range objs {
			run := o.(*v1.WorkflowRun)
			if run.Spec.Workflow == "sched" && run.Status.Phase == "Succeeded" {
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "a timer tick starts a scheduled workflow run that Succeeds")
}
