//go:build e2e

package funcd_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/artifact"
)

// The ADR-0243 scenarios deploy the http-api example of funcd-typescript (ADR-0141) as Function devices-api, pushed
// without a contract as funcdctl push does for an http Function, in namespace default, the dev token's.

const devicesAPI = "devices-api"

func jsonBody() map[string]string { return map[string]string{"Content-Type": "application/json"} }

type device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// pushDevices pushes the example's bundle under tag, with blob as its contract when it is set.
func (e *gcEnv) pushDevices(t *testing.T, tag string, blob []byte) (ref, digest string) {
	t.Helper()
	ref = "oci-layout://" + e.layout + ":" + tag
	digest, err := artifact.Push(e.ctx, ref, filepath.Join(tsExample(t, "http-api"), "devices.mjs"), blob, "", "")
	require.NoError(t, err)
	return ref, digest
}

// exampleObject reads one of the http-api example's manifests into obj.
func exampleObject(t *testing.T, file string, obj v1.Object) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tsExample(t, "http-api"), file))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, obj), "parse %s", file)
}

// devicesFn is the example's devices.yaml named name, its image at ref.
func devicesFn(t *testing.T, name, ref, digest string) *v1.Function {
	t.Helper()
	var fn v1.Function
	exampleObject(t, "devices.yaml", &fn)
	fn.Name = v1.ObjectName(name)
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	return &fn
}

// startDevices runs funcd with devices-api Ready.
func startDevices(t *testing.T) *gcEnv {
	t.Helper()
	e := startGC(t)
	ref, digest := e.pushDevices(t, "devices", nil)
	e.apply(t, devicesFn(t, devicesAPI, ref, digest))
	waitReady(t, e.c, devicesAPI)
	return e
}

// edge sends a request to the data plane and returns its reply with the body read.
func (e *gcEnv) edge(t *testing.T, method, path string, body []byte, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, method, e.dp+path, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, out
}

// createDevice posts a device named name under prefix and returns it with its Location.
func (e *gcEnv) createDevice(t *testing.T, prefix, name string, header map[string]string) (device, string) {
	t.Helper()
	h := jsonBody()
	for k, v := range header {
		h[k] = v
	}
	resp, body := e.edge(t, http.MethodPost, prefix+"/devices", []byte(`{"name":"`+name+`"}`), h)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	var d device
	require.NoError(t, json.Unmarshal(body, &d))
	return d, resp.Header.Get("Location")
}

// warmFn is an event Function of image that keeps one worker.
func warmFn(name, image string) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", image
	fn.Spec.Scaling.MinReplicas = 1
	return fn
}

// scenario: http-rest — the app answers every method at /function/devices-api with its statuses and headers, and a
// body with top-level data and specversion keys reaches it as sent.
func TestScenarioHTTPRest(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	const base = "/function/" + devicesAPI
	d, location := e.createDevice(t, base, "truck-7", nil)
	require.Equal(t, base+"/devices/"+d.ID, location, "the app is told the prefix the edge stripped")

	resp, body := e.edge(t, http.MethodGet, base+"/devices/"+d.ID, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"id":"`+d.ID+`","name":"truck-7"}`, string(body))

	resp, body = e.edge(t, http.MethodPut, base+"/devices/"+d.ID, []byte(`{"name":"van-2"}`), jsonBody())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"id":"`+d.ID+`","name":"van-2"}`, string(body))

	resp, body = e.edge(t, http.MethodPost, base+"/devices", []byte(`{"name":""}`), jsonBody())
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.JSONEq(t, `{"error":"a device needs a name"}`, string(body))

	// Taken for an envelope, this body would reach the app as {"name":"ghost"}.
	resp, body = e.edge(t, http.MethodPost, base+"/devices", []byte(`{"specversion":"1.0","data":{"name":"ghost"},"name":"crane-1"}`), jsonBody())
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	require.Contains(t, string(body), `"name":"crane-1"`)

	resp, _ = e.edge(t, http.MethodDelete, base+"/devices/"+d.ID, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = e.edge(t, http.MethodGet, base+"/devices/"+d.ID, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// scenario: http-route — through the example's Route api, a request to /api/devices reaches the app as /devices with
// its query, its headers and X-Forwarded-Prefix: /api in place of the client's.
func TestScenarioHTTPRoute(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	var rt v1.Route
	exampleObject(t, "route.yaml", &rt)
	e.apply(t, &rt)
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindRoute, "default", rt.Name)
		if err != nil {
			return false
		}
		c, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue
	}, 15*time.Second, 50*time.Millisecond, "the Route is programmed")

	d, location := e.createDevice(t, "/api", "truck-7", map[string]string{"X-Forwarded-Prefix": "/spoofed"})
	require.Equal(t, "/api/devices/"+d.ID, location)
	e.createDevice(t, "/api", "van-2", nil)
	resp, body := e.edge(t, http.MethodGet, "/api/devices?name=truck-7", nil, map[string]string{"X-Request-Id": "route-42"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "route-42", resp.Header.Get("X-Request-Id"), "the request's headers reach the app")
	require.JSONEq(t, `[{"id":"`+d.ID+`","name":"truck-7"}]`, string(body), "and its query")
}

// scenario: http-stream — the export's lines, written 50 ms apart, reach the client as they are written.
func TestScenarioHTTPStream(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	const base = "/function/" + devicesAPI
	for _, name := range []string{"a", "b", "c", "d"} {
		e.createDevice(t, base, name, nil)
	}
	req, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.dp+base+"/devices/export", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	r := bufio.NewReader(resp.Body)
	first, err := r.ReadString('\n')
	require.NoError(t, err)
	firstAt := time.Now()
	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Contains(t, first, `"name":"a"`)
	require.Equal(t, 3, strings.Count(string(rest), "\n"))
	require.GreaterOrEqual(t, time.Since(firstAt), 100*time.Millisecond, "the first line arrives before the last is written")
}

// scenario: http-validate — apply refuses protocol: http with pooling.worker or with a runtime that is not Node.
func TestScenarioHTTPValidate(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	pooled := devicesFn(t, "pooled", "oci-layout://unused:devices", "")
	pooled.Spec.Pooling.Worker = "api"
	pooled.Spec.Replicas, pooled.Spec.Scaling = 0, v1.Scaling{}
	python := devicesFn(t, "python", "oci-layout://unused:devices", "")
	python.Spec.Runtime = "python314"
	for _, fn := range []*v1.Function{pooled, python} {
		_, err := e.c.Apply(e.ctx, fn)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", fn.Name, err)
		require.Contains(t, err.Error(), "protocol", fn.Name)
	}
}

// scenario: http-mismatch — an event export under protocol: http, a delivered contract under http, and http(app) under
// cloudevent each stop the worker before it listens: the Function is ShapeValid=False ShapeInvalid.
func TestScenarioHTTPMismatch(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	writeStep(t, e.src, "event", "export const handle = async () => ({});\n")
	eventRef := "oci-layout://" + e.layout + ":event"
	eventDigest, err := artifact.Push(e.ctx, eventRef, filepath.Join(e.src, "event.mjs"), nil, "", "")
	require.NoError(t, err)
	blob, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	contractRef, contractDigest := e.pushDevices(t, "contract", blob)
	appRef, appDigest := e.pushDevices(t, "devices", nil)
	asEvent := devicesFn(t, "app-as-event", appRef, appDigest)
	asEvent.Spec.Protocol = ""
	for _, fn := range []*v1.Function{
		devicesFn(t, "event-export", eventRef, eventDigest), devicesFn(t, "with-contract", contractRef, contractDigest), asEvent,
	} {
		e.apply(t, fn)
	}
	for _, name := range []v1.ObjectName{"event-export", "with-contract", "app-as-event"} {
		var shape v1.Condition
		require.Eventually(t, func() bool {
			obj, gerr := e.c.Get(e.ctx, v1.KindFunction, "default", name)
			if gerr != nil {
				return false
			}
			var ok bool
			shape, ok = obj.(*v1.Function).Status.Conditions.Get("ShapeValid")
			return ok && shape.Status == v1.ConditionFalse
		}, 30*time.Second, 100*time.Millisecond, "%s is ShapeValid=False", name)
		require.Equal(t, "ShapeInvalid", shape.Reason, name)
		require.Contains(t, shape.Message, "spec.protocol: http", name)
	}
}

// scenario: http-no-events — a Sensor action, a workflow step and an App hook naming a Function that serves HTTP each
// fail TargetServesHTTP where each records a failed call, and the Sensor is Ready=False TargetServesHTTP.
func TestScenarioHTTPNoEvents(t *testing.T) {
	t.Parallel()
	e := startDevices(t)

	t.Run("sensor", func(t *testing.T) {
		t.Parallel()
		e.apply(t, &v1.EventSource{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
			ObjectMeta: v1.ObjectMeta{Name: "clock", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.EventSourceSpec{Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "nightly", Interval: v1.Duration(500 * time.Millisecond)}}}},
		})
		e.apply(t, &v1.Sensor{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
			ObjectMeta: v1.ObjectMeta{Name: "purge", Namespace: "default", ResourceGroup: "rg1"},
			Spec: v1.SensorSpec{
				On: []v1.Dependency{{Name: "tick", Source: "clock", Event: "nightly"}},
				Do: []v1.Action{{Name: "purge", On: "tick", Function: devicesAPI}},
			},
		})
		require.Eventually(t, func() bool {
			obj, err := e.c.Get(e.ctx, v1.KindSensor, "default", "purge")
			if err != nil {
				return false
			}
			c, ok := obj.(*v1.Sensor).Status.Conditions.Get("Ready")
			return ok && c.Status == v1.ConditionFalse && c.Reason == activator.ReasonTargetServesHTTP
		}, 15*time.Second, 50*time.Millisecond, "the Sensor is Ready=False TargetServesHTTP")
		require.Eventually(t, func() bool {
			items, err := e.c.DeadLetters(e.ctx, "default")
			return err == nil && len(items) > 0 && strings.Contains(items[0].Reason, activator.ReasonTargetServesHTTP)
		}, 30*time.Second, 100*time.Millisecond, "a matched event fails TargetServesHTTP and is dead-lettered")
	})

	t.Run("workflow", func(t *testing.T) {
		t.Parallel()
		e.apply(t, &v1.Workflow{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
			ObjectMeta: v1.ObjectMeta{Name: "purge-all", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "purge", Function: &v1.FunctionStep{Ref: devicesAPI}}}},
		})
		e.apply(t, &v1.WorkflowRun{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
			ObjectMeta: v1.ObjectMeta{Name: "purge-all-1", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.WorkflowRunSpec{Workflow: "purge-all", Input: json.RawMessage(`{}`)},
		})
		require.Eventually(t, func() bool {
			obj, err := e.c.Get(e.ctx, v1.KindWorkflowRun, "default", "purge-all-1")
			return err == nil && obj.(*v1.WorkflowRun).Status.Phase == "Failed"
		}, 30*time.Second, 100*time.Millisecond, "the run fails")
		run := getRun(t, e.c, "purge-all-1")
		require.Len(t, run.Status.Steps, 1)
		require.Contains(t, run.Status.Steps[0].Error, activator.ReasonTargetServesHTTP)
	})

	t.Run("app-hook", func(t *testing.T) {
		t.Parallel()
		ref, _ := e.pushDevices(t, "fleet", nil)
		e.apply(t, &v1.App{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
			ObjectMeta: v1.ObjectMeta{Name: "fleet", Namespace: "default", ResourceGroup: "rg1"},
			Spec: v1.AppSpec{
				Version: "1.0.0",
				Functions: []v1.AppFunction{{Name: "fleet-api", FunctionSpec: v1.FunctionSpec{
					Runtime: "nodejs22", Handler: "handle", Image: ref, Protocol: v1.ProtocolHTTP,
				}}},
				Hooks: &v1.AppHooks{PreApply: []v1.AppHook{{Function: "fleet-api"}}},
			},
		})
		got := e.waitApp(t, "fleet", v1.ConditionFalse, "HookFailed", appWithin)
		require.Contains(t, readyCondition(got).Message, activator.ReasonTargetServesHTTP)
	})
}

// scenario: http-fetch — purge-bridge reaches devices-api with ctx.fetch over its link and gets the app's status,
// headers and body; ctx.invoke on that link names ctx.fetch, and ctx.fetch to an event Function names ctx.invoke.
func TestScenarioHTTPFetch(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	const base = "/function/" + devicesAPI
	e.createDevice(t, base, "truck-7", nil)
	e.createDevice(t, base, "van-2", nil)
	writeStep(t, e.src, "bridge", `export async function handle(ctx) {
  const res = await ctx.fetch('devices', '/devices/purge', { method: 'POST', headers: { 'x-request-id': 'bridge-1' } });
  const out = { status: res.status, requestId: res.headers.get('x-request-id'), body: await res.json() };
  await ctx.invoke('devices', {}).catch((err) => { out.invoke = err.message; });
  await ctx.fetch('events', '/').catch((err) => { out.fetch = err.message; });
  return out;
}
`)
	bridge := warmFn("purge-bridge", pushStepImage(t, e.layout, e.src, "bridge"))
	bridge.Spec.Links = []v1.FunctionLink{{Alias: "devices", Target: devicesAPI}, {Alias: "events", Target: "echo"}}
	e.apply(t, warmFn("echo", e.image(t)))
	e.apply(t, bridge)
	waitReady(t, e.c, "echo", "purge-bridge")

	resp, body := e.edge(t, http.MethodPost, "/function/purge-bridge", []byte(`{}`), jsonBody())
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out struct {
		Status    int             `json:"status"`
		RequestID string          `json:"requestId"`
		Body      json.RawMessage `json:"body"`
		Invoke    string          `json:"invoke"`
		Fetch     string          `json:"fetch"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, http.StatusOK, out.Status)
	require.Equal(t, "bridge-1", out.RequestID)
	require.JSONEq(t, `{"purged":2}`, string(out.Body))
	require.Contains(t, out.Invoke, "context.fetch")
	require.Contains(t, out.Fetch, "context.invoke")
	_, list := e.edge(t, http.MethodGet, base+"/devices", nil, nil)
	require.JSONEq(t, `[]`, string(list))
}

// scenario: http-limits — firmware, a body that is not JSON, reaches the app byte for byte; a body over 1 MiB is
// refused 413 at the edge.
func TestScenarioHTTPLimits(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	const base = "/function/" + devicesAPI
	d, _ := e.createDevice(t, base, "drone-4", nil)
	firmware := base + "/devices/" + d.ID + "/firmware"
	octets := map[string]string{"Content-Type": "application/octet-stream"}
	image := []byte{0x00, 0xff, '\r', '\n', 0x80, '{', 0xfe}
	resp, body := e.edge(t, http.MethodPut, firmware, image, octets)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(body))
	resp, body = e.edge(t, http.MethodGet, firmware, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, image, body)

	resp, _ = e.edge(t, http.MethodPut, firmware, bytes.Repeat([]byte{0xff}, 1<<20+1), octets)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}
