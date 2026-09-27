//go:build e2e

package funcd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario (in-process e2e): object-created → workflow-run (ADR-0119, F83) — a running InMemory platform
// with a real `blob:` EventSource watching Bucket `raw` prefix `drop/`, an ADR-0109 Sensor binding its
// `arrived` event to a `workflow:` action projecting `${{ event.data.key }}`, and a builtin-pass Workflow
// (no runtime needed). An object written into the SAME s3BucketFor substrate view the S3 frontend writes to
// is detected by the poll → published on the Fanout → the Sensor starts a WorkflowRun of `ingest` carrying
// the projected key, which the engine drives to Succeeded. Exercises the real seams headlessly, mirroring
// the DLQ's TestScenarioDeadLetterEndToEnd.
func TestScenarioBlobEventSourceEndToEnd(t *testing.T) {
	p, err := New(InMemory(), WithBlobPollInterval(50*time.Millisecond))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	// The Bucket the source watches (existence is what s3BucketFor resolves; no prefixes/owners needed here).
	bkt := &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "raw", Namespace: "default", ResourceGroup: "rg1"},
	}
	_, err = c.Apply(ctx, bkt)
	require.NoError(t, err)

	// A builtin-pass Workflow: it echoes its input (the projected file) as output — no container/runtime.
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "ingest", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{{Name: "echo", Builtin: &v1.BuiltinStep{Pass: `${{ input }}`}}},
		},
	}
	_, err = c.Apply(ctx, wf)
	require.NoError(t, err)

	// The blob EventSource: fire `arrived` on objects under drop/ in Bucket raw.
	es := &v1.EventSource{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		ObjectMeta: v1.ObjectMeta{Name: "drops", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.EventSourceSpec{Blob: &v1.BlobSource{
			Bucket: "raw",
			Events: []v1.BlobEvent{{Name: "arrived", Prefix: "drop/", On: []v1.BlobEventType{v1.BlobCreated}}},
		}},
	}
	_, err = c.Apply(ctx, es)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, gerr := c.Get(ctx, v1.KindEventSource, "default", "drops")
		if gerr != nil {
			return false
		}
		cond, ok := obj.(*v1.EventSource).Status.Conditions.Get("Ready")
		return ok && cond.Status == v1.ConditionTrue
	}, 10*time.Second, 50*time.Millisecond, "the blob EventSource reconciles Ready (bucket resolved, events registered)")

	// The Sensor: on `arrived`, start `ingest` with file = the landed object's key.
	sen := &v1.Sensor{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: "ingest-on-drop", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "dropped", Source: "drops", Event: "arrived"}},
			Do: []v1.Action{{Name: "run-ingest", On: "dropped", Workflow: "ingest", Input: json.RawMessage(`{"file":"${{ event.data.key }}"}`)}},
		},
	}
	_, err = c.Apply(ctx, sen)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		obj, gerr := c.Get(ctx, v1.KindSensor, "default", "ingest-on-drop")
		if gerr != nil {
			return false
		}
		cond, ok := obj.(*v1.Sensor).Status.Conditions.Get("Ready")
		return ok && cond.Status == v1.ConditionTrue
	}, 10*time.Second, 50*time.Millisecond, "the Sensor becomes Ready (subscribed to the Fanout)")

	// Write an object into the SAME substrate view external S3 writes land in (s3/<ns>/<bucket>/<key>).
	require.NoError(t, p.cfg.blob.Put(ctx, "s3/default/raw/drop/a.parquet", []byte("parquet-bytes")))

	// The poll detects it → publishes → the Sensor starts a WorkflowRun of `ingest` with the projected key,
	// which the engine drives to Succeeded.
	var run *v1.WorkflowRun
	require.Eventually(t, func() bool {
		objs, lerr := c.List(ctx, v1.KindWorkflowRun, "default")
		if lerr != nil {
			return false
		}
		for _, o := range objs {
			r := o.(*v1.WorkflowRun)
			if r.Spec.Workflow == "ingest" {
				run = r
				return r.Status.Phase == "Succeeded"
			}
		}
		return false
	}, 30*time.Second, 100*time.Millisecond, "a dropped object starts an ingest WorkflowRun that Succeeds")

	require.Contains(t, string(run.Spec.Input), "drop/a.parquet", "the run carries the projected object key as input.file")

	// dedup: the object is not re-detected — no second ingest run appears after further polls.
	time.Sleep(300 * time.Millisecond)
	objs, err := c.List(ctx, v1.KindWorkflowRun, "default")
	require.NoError(t, err)
	ingestRuns := 0
	for _, o := range objs {
		if r := o.(*v1.WorkflowRun); r.Spec.Workflow == "ingest" && strings.Contains(string(r.Spec.Input), "drop/a.parquet") {
			ingestRuns++
		}
	}
	require.Equal(t, 1, ingestRuns, "the watermark suppresses a re-list — exactly one run for the object")
}
