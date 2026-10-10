package main

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// fixedPlanner answers plan for every App and keeps the last App it planned.
type fixedPlanner struct {
	mu   sync.Mutex
	plan v1.AppPlan
	last *v1.App
}

func (p *fixedPlanner) PlanApp(_ context.Context, a *v1.App) (v1.AppPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = a
	return p.plan, nil
}

func (p *fixedPlanner) planned() *v1.App {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// newPlanServer is newTestServer whose dry-run App writes are planned by a fixedPlanner answering plan.
func newPlanServer(t *testing.T, plan v1.AppPlan) (*sdk.Client, *fixedPlanner) {
	t.Helper()
	p := &fixedPlanner{plan: plan}
	c, _, _ := newTestServerVia(t, nil, func(d *controlplane.Deps) { d.Planner = p })
	return c, p
}

// todoPlan is a plan with every kind of line.
func todoPlan() v1.AppPlan {
	return v1.AppPlan{Revision: "todo-5", Parts: []v1.PlanPart{
		{Kind: v1.KindFunction, Name: "todo-api", Action: v1.PlanUpdate},
		{Kind: v1.KindRoute, Name: "todo-legacy", Action: v1.PlanPrune},
	}, Hooks: []string{"todo-migrate"}}
}

const todoPlanLines = "would apply App/todo\n  revision todo-5\n  update Function/todo-api\n  prune Route/todo-legacy\n  hook todo-migrate\n"

// ADR-0220 Contracts: the plan lines, a stop with its reason and no change.
func TestCLIPlanLines(t *testing.T) {
	t.Parallel()
	require.Nil(t, planLines(nil))
	require.Equal(t, []string{"no change"}, planLines(&v1.AppPlan{}))
	full := todoPlan()
	require.Equal(t, []string{"revision todo-5", "update Function/todo-api", "prune Route/todo-legacy", "hook todo-migrate"},
		planLines(&full))
	require.Equal(t, []string{"revision todo-1", "stop Secret/todo-key (SecretNotFound)"}, planLines(&v1.AppPlan{
		Revision: "todo-1", Parts: []v1.PlanPart{{Kind: v1.KindSecret, Name: "todo-key", Reason: "SecretNotFound"}},
	}))
	require.Equal(t, []string{"create AppRevision/todo-1 (ChildNotOwned)"}, planLines(&v1.AppPlan{
		Parts: []v1.PlanPart{{Kind: v1.KindAppRevision, Name: "todo-1", Action: v1.PlanCreate, Reason: "ChildNotOwned"}},
	}))
}

const dryRunManifest = `apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: f
  namespace: team-a
  resourceGroup: rg1
spec:
  handler: h1
---
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: team-a
  resourceGroup: rg1
spec:
  functions:
    - name: todo-api
      runtime: nodejs22
      handler: index.handler
      image: oci-layout://todo-api:1
`

// ADR-0220 Decision 7: apply --dry-run prints would apply per document and an App's plan, and stores nothing.
func TestCLIApplyDryRun(t *testing.T) {
	t.Parallel()
	c, p := newPlanServer(t, todoPlan())
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "--dry-run", "-f", writeManifest(t, dryRunManifest)))
	require.Equal(t, "would apply Function/f\n"+todoPlanLines, out.String())
	require.Equal(t, v1.ObjectName("todo-api"), p.planned().Spec.Functions[0].Name)
	for kind, name := range map[v1.Kind]v1.ObjectName{v1.KindFunction: "f", v1.KindApp: "todo"} {
		_, err := c.Get(context.Background(), kind, "team-a", name)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s/%s is not stored", kind, name)
	}
}

// ADR-0220 Decision 7: apply --dry-run stops at the first refusal, naming the document, as apply does.
func TestCLIApplyDryRunStopsAtTheFirstRefusal(t *testing.T) {
	t.Parallel()
	c, _ := newPlanServer(t, v1.AppPlan{})
	manifest := `{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"g","namespace":"team-a","resourceGroup":"rg1"},"spec":{"handler":"h"}}
---
{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"h","namespace":"team-b","resourceGroup":"rg1"},"spec":{"handler":"h"}}
---
{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"i","namespace":"team-a","resourceGroup":"rg1"},"spec":{"handler":"h"}}`
	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "--dry-run", "-f", writeManifest(t, manifest))
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `document 2 (Function "h")`)
	require.Equal(t, "would apply Function/g\n", out.String(), "the documents after the refusal are not sent")
}

// ADR-0220 Decision 7: app rollback --dry-run sends the earlier revision's spec as a dry run and prints the plan; an
// equal spec still reports no change. Nothing is written.
func TestCLIAppRollbackDryRun(t *testing.T) {
	t.Parallel()
	p := &fixedPlanner{plan: todoPlan()}
	c, _, st := newTestServerVia(t, nil, func(d *controlplane.Deps) { d.Planner = p })
	app := applyTodo(t, c, todoSpec("2.0.0"))
	seedAppRevision(t, st, "todo", app.UID, 1, todoSpec("1.0.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo", app.UID, 2, todoSpec("2.0.0"), v1.PhaseReady)
	rv := resourceVersion(t, c, v1.KindApp, "todo")

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "1", "-n", "team-a", "--dry-run"))
	require.Equal(t, todoPlanLines, out.String())
	require.Equal(t, todoSpec("1.0.0"), p.planned().Spec, "the dry run sends revision 1's spec")
	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "2", "-n", "team-a", "--dry-run"))
	require.Equal(t, "no change: App todo already has the spec of todo-2\n", out.String())
	require.Equal(t, rv, resourceVersion(t, c, v1.KindApp, "todo"), "nothing is written")
}

// ADR-0220 Decision 7: app deploy --dry-run renders and fills the App, keeping the stored spec.paused, sends it as a
// dry run even when the spec is equal, prints the plan and waits for nothing; a refusal fails it. Nothing is written.
func TestCLIAppDeployDryRun(t *testing.T) {
	t.Parallel()
	c, p := newPlanServer(t, todoPlan())
	dir := deployTemplate(t, "1.0.0")
	var out bytes.Buffer
	require.NoError(t, execApp(&out, c, "app", "deploy", dir, "-n", "team-a", "--dry-run"))
	require.Equal(t, todoPlanLines, out.String())
	_, err := c.Get(context.Background(), v1.KindApp, "team-a", "todo")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing is written")

	require.NoError(t, execApp(&bytes.Buffer{}, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	waitApplied(t, c, "1.0.0")
	require.NoError(t, execApp(&bytes.Buffer{}, c, "app", "pause", "todo", "-n", "team-a"))
	rv := resourceVersion(t, c, v1.KindApp, "todo")
	out.Reset()
	require.NoError(t, execApp(&out, c, "app", "deploy", dir, "-n", "team-a", "--dry-run"))
	require.Equal(t, todoPlanLines, out.String(), "an equal spec is sent too")
	require.True(t, p.planned().Spec.Paused, "the stored spec.paused is kept")
	require.Equal(t, renderedSpec(t, "1.0.0").Functions, p.planned().Spec.Functions)
	require.Equal(t, rv, resourceVersion(t, c, v1.KindApp, "todo"), "nothing is written")

	out.Reset()
	err = execApp(&out, c, "app", "deploy", dir, "-n", "team-b", "--dry-run")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a refusal fails the dry run")
	require.Empty(t, out.String())
}
