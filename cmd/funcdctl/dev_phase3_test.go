//go:build dev

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// waitFuncsReady blocks until every named function reports Ready — reconcile → materialize → shim boot → readiness
// all passed from the local builds.
func waitFuncsReady(t *testing.T, inst *devInstance, names ...string) {
	t.Helper()
	for _, n := range names {
		waitFunction(t, inst, n)
	}
}

// waitFuncsApplied blocks until each named Function exists; a Workflow step's Function idles until a run needs it.
func waitFuncsApplied(t *testing.T, inst *devInstance, names ...string) {
	t.Helper()
	for _, n := range names {
		require.Eventually(t, func() bool {
			_, err := inst.client.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(n))
			return err == nil
		}, 20*time.Second, 50*time.Millisecond, "%s is applied", n)
	}
}

// workflowDoc is Workflow name with steps (block YAML, indented four spaces).
func workflowDoc(name, steps string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: Workflow\nmetadata:\n  name: " + name + "\nspec:\n  steps:\n" + steps
}

// imageStep is an image step name tagged stem, depending on dependsOn when it is set.
func imageStep(name, stem, dependsOn string) string {
	s := "    - name: " + name + "\n      function:\n        image: oci-layout:///deps/registry:" + stem + "\n"
	if dependsOn != "" {
		s += "      dependsOn:\n        - " + dependsOn + "\n"
	}
	return s
}

// stepBuild is the build and contract file of a step stem returning {step: label}.
func stepBuild(stem, label string) map[string]string {
	return map[string]string{
		stem + ".mjs":           "export function handle(ctx, event) { return { step: '" + label + "', n: (event.data?.n ?? 0) + 1 }; }\n",
		stem + ".contract.yaml": permissiveContract,
	}
}

// runWorkflow applies a WorkflowRun of workflow and waits for it to succeed.
func runWorkflow(t *testing.T, inst *devInstance, workflow, run string) *v1.WorkflowRun {
	t.Helper()
	ctx := context.Background()
	_, err := inst.client.Apply(ctx, &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(run), Namespace: "default", ResourceGroup: "dev"},
		Spec:       v1.WorkflowRunSpec{Workflow: v1.ObjectName(workflow), Input: json.RawMessage(`{"n":1}`)},
	})
	require.NoError(t, err)
	var got *v1.WorkflowRun
	require.Eventually(t, func() bool {
		obj, gerr := inst.client.Get(ctx, v1.KindWorkflowRun, "default", v1.ObjectName(run))
		if gerr != nil {
			return false
		}
		got = obj.(*v1.WorkflowRun)
		return got.Status.Phase == "Succeeded"
	}, 40*time.Second, 100*time.Millisecond, "run %s succeeds", run)
	return got
}

// stepImage is the image step index of Workflow name reads.
func stepImage(t *testing.T, inst *devInstance, name string, index int) string {
	t.Helper()
	obj, err := inst.client.Get(context.Background(), v1.KindWorkflow, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Workflow).Spec.Steps[index].Function.Image
}

// scenario: dev-workflow-steps — a Workflow with an image step tagged ingest and a ref step to a Function of the
// folder: `<workflow>-<step>` runs ingest.mjs through the workflow materializer, the ref step runs that Function, and a
// rebuild of ingest.mjs rolls the step to a new copy, the old one going once the new revision serves (Decision 7).
func TestScenarioDevWorkflowSteps(t *testing.T) {
	requireNode(t)
	dir := devProject(t, merge(
		map[string]string{"workflow.yaml": workflowDoc("pipeline", imageStep("a", "ingest", "")+
			"    - name: b\n      function:\n        ref: greeter\n      dependsOn:\n        - a\n")},
		stepBuild("ingest", "a"),
		nodeFunction("greeter", "", "export function handle(ctx, event) { return { step: 'b', n: (event.data?.n ?? 0) + 1 }; }\n"),
	))
	inst := runDev(t, dir)
	require.Equal(t, []string{"pipeline"}, inst.workflows)
	require.Equal(t, []string{"greeter", "pipeline-a"}, inst.functions)
	waitFuncsReady(t, inst, "greeter")
	waitFuncsApplied(t, inst, "pipeline-a")

	got := runWorkflow(t, inst, "pipeline", "run-01")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "a"), "the image step ran its build")
	require.Equal(t, v1.StepPhase("Succeeded"), runStepPhase(got, "b"), "the ref step ran the folder's Function")

	first := stepImage(t, inst, "pipeline", 0)
	require.True(t, strings.HasPrefix(first, "file://"), first)
	writeProject(t, dir, map[string]string{"ingest.mjs": "export function handle() { return { step: 'a2' }; }\n"})
	require.Eventually(t, func() bool { return stepImage(t, inst, "pipeline", 0) != first }, 20*time.Second, 100*time.Millisecond,
		"a rebuild rolls the step to a new copy")
	firstDir := filepath.Dir(strings.TrimPrefix(first, "file://"))
	require.Eventually(t, func() bool {
		_, err := os.Stat(firstDir)
		return os.IsNotExist(err)
	}, 30*time.Second, 100*time.Millisecond, "the old copy goes once the step serves the new one")
}

// TestIssue428_DevReloadsEditedWorkflow — ADR-0247 Decision 8: an edit to the workflow file re-applies the Workflow
// with no restart, and a step added after boot is not started: dev warns that it needs a restart.
// scenario: funcdctl-dev-logs-like-daemon (ADR-0197) — the warning reaches funcdctl's output in NewLogger's text form.
func TestIssue428_DevReloadsEditedWorkflow(t *testing.T) {
	requireNode(t)
	var logs lockedBuffer
	dir := devProject(t, merge(map[string]string{"workflow.yaml": workflowDoc("pipeline", imageStep("a", "stepa", ""))},
		stepBuild("stepa", "a"), stepBuild("stepc", "c")))
	inst, err := tryStartDevTo(t, dir, &logs)
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline-a"}, inst.functions)

	writeProject(t, dir, map[string]string{"workflow.yaml": workflowDoc("pipeline", imageStep("a", "stepa", "")+imageStep("c", "stepc", "a"))})
	require.Eventually(t, func() bool {
		obj, err := inst.client.Get(context.Background(), v1.KindWorkflow, "default", "pipeline")
		return err == nil && len(obj.(*v1.Workflow).Spec.Steps) == 2
	}, 20*time.Second, 50*time.Millisecond, "the running dev session applies the edited Workflow")
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), `level=WARN msg="restart funcdctl dev to run a Workflow step added after it started" function=pipeline-c`)
	}, 20*time.Second, 50*time.Millisecond, "a step added after boot warns that a restart is needed, in NewLogger's text form: %s", logs.String())
}

// TestIssue501_DevReloadDeletesRenamedWorkflowAndRemovedStep — ADR-0247 Decision 8: a reload that renames the Workflow
// deletes the Workflow of the old name, and one that removes a step leaves no Function of it.
func TestIssue501_DevReloadDeletesRenamedWorkflowAndRemovedStep(t *testing.T) {
	requireNode(t)
	dir := devProject(t, merge(map[string]string{"workflow.yaml": workflowDoc("pipeline", imageStep("a", "stepa", "")+imageStep("b", "stepb", "a"))},
		stepBuild("stepa", "a"), stepBuild("stepb", "b")))
	inst := runDev(t, dir)
	ctx := context.Background()
	gone := func(kind v1.Kind, name v1.ObjectName) bool {
		_, err := inst.client.Get(ctx, kind, "default", name)
		return fault.KindOf(err) == fault.NotFound
	}
	waitFuncsApplied(t, inst, "pipeline-a", "pipeline-b")

	writeProject(t, dir, map[string]string{"workflow.yaml": workflowDoc("pipeline2", imageStep("a", "stepa", ""))})
	require.Eventually(t, func() bool { return !gone(v1.KindWorkflow, "pipeline2") && gone(v1.KindWorkflow, "pipeline") },
		20*time.Second, 50*time.Millisecond, "the reload applies the renamed Workflow and deletes the old one")
	require.Eventually(t, func() bool { return gone(v1.KindFunction, "pipeline-b") }, 20*time.Second, 50*time.Millisecond,
		"no Function of the removed step is left")
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

// scenario: dev-runs-resources (fn-to-fn facet) — the folder's Functions run together, and a caller's
// `context.invoke("<alias>", …)` resolves in-process for a declared link, while an undeclared alias is Forbidden (the
// ADR-0064/0075 default-deny, preserved in dev).
func TestScenarioDevFnToFnLinks(t *testing.T) {
	requireNode(t)
	dir := devProject(t, merge(
		nodeFunction("front", "  bindings:\n    links:\n      - alias: greeter\n        target: greeter\n",
			"export async function handle(ctx, event) {\n"+
				"  try {\n"+
				"    const reply = await ctx.invoke(event.data.target, { data: { name: event.data.name } });\n"+
				"    return { ok: true, greeting: reply.greeting };\n"+
				"  } catch (e) { return { ok: false, error: String(e) }; }\n"+
				"}\n"),
		nodeFunction("greeter", "", "export function handle(ctx, event) { return { greeting: `Hello, ${event.data.name}!` }; }\n"),
	))
	inst := runDev(t, dir)
	require.ElementsMatch(t, []string{"front", "greeter"}, inst.functions)
	waitFuncsReady(t, inst, "front", "greeter")
	url := inst.gatewayURL + "/function/front"

	resp, body := post(t, url, `{"data":{"target":"greeter","name":"ada"}}`)
	require.Equal(t, 200, resp.StatusCode, body)
	require.Contains(t, body, `"ok":true`, body)
	require.Contains(t, body, "Hello, ada!")

	deny, denyBody := post(t, url, `{"data":{"target":"ghost","name":"ada"}}`)
	require.Equal(t, 200, deny.StatusCode, denyBody)
	require.Contains(t, denyBody, `"ok":false`, denyBody)
	require.Contains(t, denyBody, "403", "an unknown link alias is Forbidden (default-deny): %s", denyBody)
}

// Dev decodes each document as `funcdctl apply` does: a bare y/n key stays a string key (#63) and an unknown key is
// refused, naming it, not dropped (#64, #317).
func TestIssue317_DevDecodesDocumentsStrictly(t *testing.T) {
	t.Parallel()
	const steps = "    - name: a\n      builtin:\n        pass: \"x\"\n      params:\n        y: 1\n        n: 2\n"
	f, err := loadDevFolder("test", devProject(t, map[string]string{"w.yaml": workflowDoc("w", steps)}))
	require.NoError(t, err)
	require.JSONEq(t, `{"y":1,"n":2}`, string(f.resources[0].obj.(*v1.Workflow).Spec.Steps[0].Params))

	_, err = loadDevFolder("test", devProject(t, map[string]string{"w.yaml": strings.Replace(workflowDoc("w", steps), "spec:\n", "spec:\n  bogus: 1\n", 1)}))
	require.ErrorContains(t, err, "bogus")
}

// TestTagStem — the step-image tag stem is the segment after the last ':' (registry:ingest → ingest); a tag-less or
// digest-pinned ref names no build.
func TestTagStem(t *testing.T) {
	for _, c := range []struct {
		image, want string
		ok          bool
	}{
		{"oci-layout:///mnt/funcd-deps/registry:ingest", "ingest", true},
		{"registry:score", "score", true},
		{"ghcr.io/acme/fn:v1", "v1", true},
		{"registry", "", false},
		{"repo@sha256:abcdef", "", false},
	} {
		got, err := tagStem("op", c.image)
		if c.ok {
			require.NoError(t, err, "%s", c.image)
			require.Equal(t, c.want, got, "%s", c.image)
		} else {
			require.ErrorContains(t, err, "naming a build", "%s", c.image)
		}
	}
}

// scenario: http-validate — funcdctl dev serves an http Function: its contract file says http, so its bundle gets no
// contract and one left from an earlier run is removed (ADR-0243 Decision 7).
func TestDevServesAnHTTPFunction(t *testing.T) {
	t.Parallel()
	f, err := loadDevFolder("test", devProject(t, map[string]string{
		"api.yaml":          fnDoc("api", "nodejs22", "  protocol: http\n"),
		"api.mjs":           "export const handle = {};\n",
		"api.contract.yaml": "runtime: nodejs22\nprotocol: http\n",
	}))
	require.NoError(t, err)
	b := f.functions[0].build
	blob, err := b.contractBlob()
	require.NoError(t, err)
	require.Nil(t, blob)
	bundle := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bundle, devContractFile), []byte(`{}`), 0o600))
	require.NoError(t, deliverBuild("op", b, bundle, f.dir))
	require.NoFileExists(t, filepath.Join(bundle, devContractFile))
	require.FileExists(t, filepath.Join(bundle, "api.mjs"))
}
