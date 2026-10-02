//go:build dev

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// startDevPath boots funcdctl dev over an explicit path (a workflow.yaml or a manifest dir) and tears it
// down at test end. It mirrors runDev but does not assume the path is a directory.
func startDevPath(t *testing.T, path string) *devInstance {
	t.Helper()
	a := &cli{out: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := a.startDev(ctx, path, "", devConfig{})
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		_ = inst.stop()
	})
	return inst
}

// waitFuncsReady blocks until every named function reports Ready — reconcile → materialize → shim boot →
// readiness all passed from source.
func waitFuncsReady(t *testing.T, inst *devInstance, names ...string) {
	t.Helper()
	for _, n := range names {
		name := n
		require.Eventually(t, func() bool {
			got, err := inst.client.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
			if err != nil {
				return false
			}
			return got.(*v1.Function).Status.Phase == v1.PhaseReady
		}, 20*time.Second, 50*time.Millisecond, "the from-source function %q reconciles to Ready", name)
	}
}

// scenario: dev-workflow — `funcdctl dev workflow.yaml` loads a 2-step DAG whose steps resolve by tag stem
// to `<stem>.funcdctl.yaml` (+ handler) and run FROM SOURCE. A WorkflowRun drives the DAG; both steps
// reach Succeeded, proving each step executed from source through the real embedded workflow engine (no
// OCI pull — the steps were rewritten to dispatch to the synthesized Functions by ref). Node-gated.
func TestScenarioDevWorkflow(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"workflow.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Workflow\n" +
			"metadata:\n  name: pipeline\n  namespace: default\n" +
			"spec:\n  steps:\n" +
			"    - name: a\n      function:\n        image: oci-layout:///deps/registry:stepa\n" +
			"    - name: b\n      function:\n        image: registry:stepb\n      dependsOn: [a]\n",
		"stepa.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"stepa.mjs":           "export function handle(ctx, event) { return { step: 'a', n: (event.data?.n ?? 0) + 1 }; }\n",
		"stepb.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"stepb.mjs":           "export function handle(ctx, event) { return { step: 'b', n: (event.data?.n ?? 0) + 1 }; }\n",
	})

	inst := startDevPath(t, filepath.Join(dir, "workflow.yaml"))
	require.Equal(t, "pipeline", inst.workflow, "the Workflow CRD is detected + loaded as the DAG")
	require.ElementsMatch(t, []string{"stepa", "stepb"}, inst.functions,
		"each step's function.image tag stem resolved to a <stem>.funcdctl.yaml run from source")
	waitFuncsReady(t, inst, "stepa", "stepb")

	// Trigger a run (the same surface a Sensor / `funcdctl workflow run` uses) and drive the DAG.
	ctx := context.Background()
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "run-01", Namespace: "default", ResourceGroup: "dev"},
		Spec:       v1.WorkflowRunSpec{Workflow: "pipeline", Input: json.RawMessage(`{"n":1}`)},
	}
	_, err := inst.client.Apply(ctx, run)
	require.NoError(t, err)

	var got *v1.WorkflowRun
	require.Eventually(t, func() bool {
		obj, gerr := inst.client.Get(ctx, v1.KindWorkflowRun, "default", "run-01")
		if gerr != nil {
			return false
		}
		got = obj.(*v1.WorkflowRun)
		return got.Status.Phase == "Succeeded"
	}, 40*time.Second, 100*time.Millisecond, "the DAG runs to Succeeded via real from-source step execution")

	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "a"), "step a executed from source")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "b"), "step b executed from source (after a)")
}

// runStepPhase returns a run's per-step phase from its mirrored status (or "" if absent).
func runStepPhase(run *v1.WorkflowRun, step string) v1.StepPhase {
	for _, s := range run.Status.Steps {
		if s.Name == v1.ObjectName(step) {
			return s.Phase
		}
	}
	return ""
}

// scenario: dev-workflow (fn-to-fn facet) — a directory of several `<stem>.funcdctl.yaml` single-file
// functions runs ALL of them (Decision 9), and each carries its `spec.links` (Phase 3): the caller's
// `context.invoke("<alias>", …)` resolves in-process via the UDS local API for a DECLARED link, while an
// UNKNOWN alias is Forbidden (the ADR-0064/0075 default-deny, preserved in dev). Node-gated.
func TestScenarioDevFnToFnLinks(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"front.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  links:\n    - alias: greeter\n      target: greeter\n" +
			permissiveContract,
		// front invokes whichever alias the input names: "greeter" (declared → allowed) or an undeclared
		// alias (default-deny → the invoke rejects, caught + reported).
		"front.mjs": "export async function handle(ctx, event) {\n" +
			"  try {\n" +
			"    const reply = await ctx.invoke(event.data.target, { data: { name: event.data.name } });\n" +
			"    return { ok: true, greeting: reply.greeting };\n" +
			"  } catch (e) { return { ok: false, error: String(e) }; }\n" +
			"}\n",
		"greeter.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"greeter.mjs":           "export function handle(ctx, event) { return { greeting: `Hello, ${event.data.name}!` }; }\n",
	})

	inst := startDevPath(t, dir)
	require.ElementsMatch(t, []string{"front", "greeter"}, inst.functions,
		"the dir's <stem>.funcdctl.yaml manifests each loaded as a function (Decision 9)")
	waitFuncsReady(t, inst, "front", "greeter")

	url := inst.gatewayURL + "/function/front"

	// Declared link greeter → the in-process invoke succeeds and greeter's reply flows back.
	resp, body := post(t, url, `{"data":{"target":"greeter","name":"ada"}}`)
	require.Equal(t, 200, resp.StatusCode, "front invoked the declared link: %s", body)
	require.Contains(t, body, `"ok":true`, "the declared link resolved: %s", body)
	require.Contains(t, body, "Hello, ada!", "greeter's reply flowed back through the link")

	// Undeclared alias → Forbidden (default-deny). The handler catches the rejection and reports it.
	deny, denyBody := post(t, url, `{"data":{"target":"ghost","name":"ada"}}`)
	require.Equal(t, 200, deny.StatusCode, "the handler caught the denied invoke: %s", denyBody)
	require.Contains(t, denyBody, `"ok":false`, "the undeclared alias was denied: %s", denyBody)
	require.Contains(t, denyBody, "403", "an unknown link alias is Forbidden (default-deny): %s", denyBody)
}

// --- unit tests (no runtime; deterministic) ---

const unitManifest = "runtime: nodejs22\nhandler: handle\ncontract:\n  input:\n    type: object\n  output:\n    type: object\n"

// TestDetectWorkflow — a Workflow CRD file is detected; a funcdctl.yaml and a directory are not.
func TestDetectWorkflow(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(
		"apiVersion: funcd.io/v1alpha1\nkind: Workflow\nmetadata:\n  name: wf\nspec:\n  steps:\n    - name: a\n      builtin:\n        pass: \"x\"\n"), 0o600))
	fnPath := filepath.Join(dir, "funcdctl.yaml")
	require.NoError(t, os.WriteFile(fnPath, []byte(unitManifest), 0o600))

	wf, isWf, err := detectWorkflow("op", wfPath)
	require.NoError(t, err)
	require.True(t, isWf, "kind: Workflow ⇒ detected as a workflow")
	require.Equal(t, v1.ObjectName("wf"), wf.Name)

	_, isWf, err = detectWorkflow("op", fnPath)
	require.NoError(t, err)
	require.False(t, isWf, "a funcdctl.yaml is not a workflow")

	_, isWf, err = detectWorkflow("op", dir)
	require.NoError(t, err)
	require.False(t, isWf, "a directory is never a workflow")
}

// `funcdctl dev workflow.yaml` decodes the Workflow as `funcdctl apply` does: a bare y/n key stays a
// string key (#63) and an unknown key is rejected, not dropped (#64).
func TestIssue317_DevDecodesWorkflowStrictly(t *testing.T) {
	dir := t.TempDir()
	const head = "apiVersion: funcd.io/v1alpha1\nkind: Workflow\nmetadata:\n  name: w\nspec:\n"
	okPath := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(okPath, []byte(head+
		"  steps:\n    - name: a\n      builtin:\n        pass: \"x\"\n      params:\n        y: 1\n        n: 2\n"), 0o600))
	wf, isWf, err := detectWorkflow("op", okPath)
	require.NoError(t, err)
	require.True(t, isWf)
	require.Len(t, wf.Spec.Steps, 1)
	require.JSONEq(t, `{"y":1,"n":2}`, string(wf.Spec.Steps[0].Params))

	bogusPath := filepath.Join(dir, "bogus.yaml")
	require.NoError(t, os.WriteFile(bogusPath, []byte(head+
		"  bogus: 1\n  steps:\n    - name: a\n      builtin:\n        pass: \"x\"\n"), 0o600))
	_, _, err = detectWorkflow("op", bogusPath)
	require.ErrorContains(t, err, "bogus")
}

// TestTagStem — the step-image tag stem is the segment after the last ':' (registry:ingest → ingest);
// a tag-less or digest-pinned ref has no stem.
func TestTagStem(t *testing.T) {
	cases := []struct {
		image, want string
		ok          bool
	}{
		{"oci-layout:///mnt/funcd-deps/registry:ingest", "ingest", true},
		{"registry:score", "score", true},
		{"ghcr.io/acme/fn:v1", "v1", true},
		{"registry", "", false},           // no tag
		{"repo@sha256:abcdef", "", false}, // digest, not a tag stem
	}
	for _, c := range cases {
		got, err := tagStem("op", c.image)
		if c.ok {
			require.NoError(t, err, "%s", c.image)
			require.Equal(t, c.want, got, "%s", c.image)
		} else {
			require.Error(t, err, "%s should have no resolvable stem", c.image)
		}
	}
}

// TestResolveDevFunctionsEnumeratesAllStems — a dir of <stem>.funcdctl.yaml manifests loads ALL of them
// (Decision 9), each an isolated single-file function named by its stem.
func TestResolveDevFunctionsEnumeratesAllStems(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "front.funcdctl.yaml"), []byte(unitManifest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "greeter.funcdctl.yaml"), []byte(unitManifest), 0o600))

	pfs, err := resolveDevFunctions("op", dir, "", "")
	require.NoError(t, err)
	require.Len(t, pfs, 2)
	names := []string{string(pfs[0].name), string(pfs[1].name)}
	require.ElementsMatch(t, []string{"front", "greeter"}, names)
	for _, pf := range pfs {
		require.True(t, pf.isolate, "a multi-function stem manifest is isolated in its own bundle")
	}
	require.Equal(t, "front.mjs", pfs[0].entry, "entry defaults to <stem>.mjs (nodejs)")
}

// TestResolveDevFunctionsSelectsByStem — `funcdctl dev <stem>` selects the one manifest whose stem matches.
func TestResolveDevFunctionsSelectsByStem(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "front.funcdctl.yaml"), []byte(unitManifest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "greeter.funcdctl.yaml"), []byte(unitManifest), 0o600))

	pfs, err := resolveDevFunctions("op", filepath.Join(dir, "front"), "", "") // a non-existent path ⇒ a stem selector
	require.NoError(t, err)
	require.Len(t, pfs, 1)
	require.Equal(t, v1.ObjectName("front"), pfs[0].name)
	require.True(t, pfs[0].isolate)
}

// TestResolveDevFunctionsGenericInPlace — a dir with only the generic funcdctl.yaml is the single in-place
// bundle named after its dir (the Phase-1/2 path, unchanged).
func TestResolveDevFunctionsGenericInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myfunc")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte(unitManifest), 0o600))

	pfs, err := resolveDevFunctions("op", dir, "", "")
	require.NoError(t, err)
	require.Len(t, pfs, 1)
	require.Equal(t, v1.ObjectName("myfunc"), pfs[0].name, "the generic bundle takes its dir's name")
	require.False(t, pfs[0].isolate, "the generic bundle runs in place")
	require.Equal(t, "handler.mjs", pfs[0].entry)
}

// TestSynthesizeFunctionCarriesLinks — the synthesized Function carries spec.links so context.invoke
// resolves (Phase 3 restored what Phase 1 dropped).
func TestSynthesizeFunctionCarriesLinks(t *testing.T) {
	m := &sdk.Manifest{
		Runtime: "nodejs22",
		Handler: "handle",
		Bindings: sdk.Bindings{
			Links: []v1.FunctionLink{{Alias: "greeter", Target: "greeter"}},
		},
	}
	fn := synthesizeFunction(plannedFunc{m: m, name: "front"}, "/tmp/front.mjs")
	require.Equal(t, []v1.FunctionLink{{Alias: "greeter", Target: "greeter"}}, fn.Spec.Links,
		"spec.links are carried into the synthesized Function")
	require.Equal(t, "file:///tmp/front.mjs", string(fn.Spec.Image))
	require.Equal(t, 1, fn.Spec.Scaling.MinReplicas, "MinReplicas=1 so it serves immediately")
}

// TestResolveDevFunctionsNameFlagOverridesGeneric — --name names the single generic function (the
// manifest-stem-else-flag rule); a <stem>.funcdctl.yaml still names by stem and ignores --name.
func TestResolveDevFunctionsNameFlagOverridesGeneric(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myfunc")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte(unitManifest), 0o600))

	pfs, err := resolveDevFunctions("op", dir, "", "billing")
	require.NoError(t, err)
	require.Len(t, pfs, 1)
	require.Equal(t, v1.ObjectName("billing"), pfs[0].name, "--name overrides the generic function name")
}

// TestResolveDevFunctionsGenericDotResolvesToDir — running `funcdctl dev` inside the dir (path ".")
// names the generic function after the REAL directory, not the degenerate basename of "." (the
// abs-resolve fix; previously this yielded the fallback "dev").
func TestResolveDevFunctionsGenericDotResolvesToDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "orders")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte(unitManifest), 0o600))
	t.Chdir(dir)

	pfs, err := resolveDevFunctions("op", ".", "", "")
	require.NoError(t, err)
	require.Len(t, pfs, 1)
	require.Equal(t, v1.ObjectName("orders"), pfs[0].name, `"." resolves to the real dir name, not "dev"`)
}
