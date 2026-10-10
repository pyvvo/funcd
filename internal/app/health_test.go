package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
)

// ADR-0215 Decision 8 and the App half of the health scenarios.

func newFn(name v1.ObjectName) *v1.Function {
	return &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "todo-rg"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://x:1"},
	}
}

// setFunction gives Function name phase and a Ready condition with status, reason and message.
func (h *harness) setFunction(name v1.ObjectName, phase v1.Phase, status v1.ConditionStatus, reason, msg string) {
	h.t.Helper()
	fn := h.get(v1.KindFunction, name)
	setStatus(fn, phase, v1.Condition{Type: "Ready", Status: status, Reason: reason, Message: msg})
	h.update(fn)
}

// scenario: health-workflow-step — once the App's Workflow part is Ready on its own condition, a step Function that
// turns Degraded makes the child Workflow/todo-plan Pending, reason StepNotReady, the App's message naming
// Function/todo-plan-due; it is Ready again with the step.
func TestScenarioHealthWorkflowStep(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	require.Equal(t, v1.AppChild{Kind: v1.KindWorkflow, Name: "todo-plan", State: v1.AppChildReady}, h.child(v1.KindWorkflow, "todo-plan"))

	h.setFunction("todo-plan-due", v1.PhaseDegraded, v1.ConditionFalse, "Restarting", "a replica stopped answering /health/liveness and is being replaced")
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindWorkflow, Name: "todo-plan", State: v1.AppChildPending, Reason: "StepNotReady"},
		h.child(v1.KindWorkflow, "todo-plan"))
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	c := h.ready()
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "Workflow/todo-plan: StepNotReady: Function/todo-plan-due: Restarting", c.Message)

	h.markReady(v1.KindFunction, "todo-plan-due")
	h.reconcile()
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
}

// The step walk judges an image step's materialized Function and a ref step's Function with the Function rule: a
// missing one is Pending StepNotReady, a NotStarted one is settled, and a sub-workflow step is not walked; the walk
// starts only once the Workflow's own Ready passes.
func TestWorkflowStepWalk(t *testing.T) {
	steps := func(a *v1.App) {
		a.Spec = v1.AppSpec{Workflows: []v1.AppWorkflow{{Name: "todo-plan", WorkflowSpec: v1.WorkflowSpec{Steps: []v1.WorkflowStep{
			{Name: "due", Function: &v1.FunctionStep{Image: "oci-layout://todo-due:1"}},
			{Name: "notify", Function: &v1.FunctionStep{Ref: "mailer"}, DependsOn: []v1.ObjectName{"due"}},
			{Name: "archive", Workflow: &v1.WorkflowRef{Ref: "todo-archive"}, DependsOn: []v1.ObjectName{"notify"}},
		}}}}}
	}
	h := newHarness(t, nil)
	h.create(todoApp(steps))
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindWorkflow, Name: "todo-plan", State: v1.AppChildPending, Reason: "Progressing"},
		h.child(v1.KindWorkflow, "todo-plan"), "the Workflow's own Ready comes first")

	wf := h.get(v1.KindWorkflow, "todo-plan")
	setStatus(wf, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", wf.GetObjectMeta().Generation))
	h.update(wf)
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindWorkflow, Name: "todo-plan", State: v1.AppChildPending, Reason: "StepNotReady"},
		h.child(v1.KindWorkflow, "todo-plan"))
	require.Equal(t, "Workflow/todo-plan: StepNotReady: Function/todo-plan-due: not found", h.ready().Message)

	h.readyStep("todo-plan-due")
	h.create(newFn("mailer"))
	h.reconcile()
	require.Equal(t, "Workflow/todo-plan: StepNotReady: Function/mailer: Progressing", h.ready().Message, "the ref step's Function")

	mailer := h.get(v1.KindFunction, "mailer")
	gen := mailer.GetObjectMeta().Generation
	setStatus(mailer, v1.PhaseIdle, cond("RevisionReady", v1.ConditionUnknown, "NotStarted", gen), cond("ShapeValid", v1.ConditionUnknown, "NotStarted", gen))
	h.update(mailer)
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindWorkflow, Name: "todo-plan", State: v1.AppChildReady}, h.child(v1.KindWorkflow, "todo-plan"),
		"a NotStarted step Function is settled and the sub-workflow step is not walked")
	require.Nil(t, h.get(v1.KindWorkflow, "todo-archive"))
}

// MapStepFunction requeues each App of the Function's namespace whose Workflow part steps through it, by image or ref.
func TestMapStepFunction(t *testing.T) {
	h := newHarness(t, nil)
	h.create(todoApp(func(a *v1.App) {
		a.Spec.Workflows[0].Steps = append(a.Spec.Workflows[0].Steps,
			v1.WorkflowStep{Name: "notify", Function: &v1.FunctionStep{Ref: "mailer"}, DependsOn: []v1.ObjectName{"due"}})
	}))
	h.create(todoApp(func(b *v1.App) {
		b.Name = "other"
		b.Spec = v1.AppSpec{KV: []v1.AppKVStore{{Name: "other-store"}}}
	}))
	h.reconcile()
	todo := []controller.Request{{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "todo"}}
	require.Equal(t, todo, h.r.MapStepFunction(h.ctx, newFn("todo-plan-due")), "an image step's Function")
	require.Equal(t, todo, h.r.MapStepFunction(h.ctx, newFn("mailer")), "a ref step's Function")
	require.Empty(t, h.r.MapStepFunction(h.ctx, newFn("todo-api")), "a Function no step dispatches to")
	elsewhere := newFn("todo-plan-due")
	elsewhere.Namespace = "team-b"
	require.Empty(t, h.r.MapStepFunction(h.ctx, elsewhere), "another namespace's Function")
}

// The health reasons reach the App: a Function Degraded on a hung replica or a lost dependency, or a KVStore whose
// storage probe fails, degrades the App naming the part; a Function Ready with one replica reporting keeps it Ready.
func TestHealthReasonsReachTheApp(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(h *harness)
		msg  string
	}{
		{"hung replica", func(h *harness) {
			h.setFunction("todo-api", v1.PhaseDegraded, v1.ConditionFalse, "Restarting", "a replica stopped answering /health/liveness and is being replaced")
		}, "Function/todo-api: Restarting: a replica stopped answering /health/liveness and is being replaced"},
		{"lost dependency", func(h *harness) {
			h.setFunction("todo-api", v1.PhaseDegraded, v1.ConditionFalse, "DependencyNotReady", `kv binding "audit": kv::read is not permitted`)
		}, `Function/todo-api: DependencyNotReady: kv binding "audit": kv::read is not permitted`},
		{"storage down", func(h *harness) {
			ks := h.get(v1.KindKVStore, "todo-store")
			setStatus(ks, v1.PhaseDegraded, v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "StorageUnreachable",
				Message: "badger: closed", ObservedGeneration: ks.GetObjectMeta().Generation})
			h.update(ks)
		}, "KVStore/todo-store: StorageUnreachable: badger: closed"},
		{"one replica of two reporting", func(h *harness) {
			h.setFunction("todo-api", v1.PhaseReady, v1.ConditionTrue, "DependencyNotReady", `replicas 1 ready of 2: socket: timeout`)
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.install(todoApp(nil))
			tc.set(h)
			h.reconcile()
			if tc.msg == "" {
				require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
				require.Equal(t, v1.ConditionTrue, h.ready().Status)
				return
			}
			require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
			c := h.ready()
			require.Equal(t, v1.ConditionFalse, c.Status)
			require.Equal(t, "ChildNotReady", c.Reason)
			require.Equal(t, tc.msg, c.Message)
		})
	}
}
