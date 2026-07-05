package admission_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
)

func runObj(input string) *v1.WorkflowRun {
	r := &v1.WorkflowRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun}}
	r.Name, r.Namespace, r.ResourceGroup = "run", "default", "rg1"
	r.Spec.Workflow = "wf"
	if input != "" {
		r.Spec.Input = json.RawMessage(input)
	}
	return r
}

// scenario core: workflowrun-payload — a run whose spec.input exceeds the cap is rejected (Invalid);
// one within the cap is admitted; limit ≤ 0 disables the check.
func TestWorkflowRunPayloadAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewWorkflowRunPayloadAdmission(64)
	require.True(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Create))
	require.True(t, a.Handles(v1.KindWorkflowRun.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))

	ctx := context.Background()
	_, err := a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(`{"ok":1}`)})
	require.NoError(t, err, "a small input is admitted")

	big := `{"data":"` + strings.Repeat("x", 200) + `"}`
	_, err = a.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(big)})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an over-cap input ⇒ Invalid")

	off := admission.NewWorkflowRunPayloadAdmission(0)
	_, err = off.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindWorkflowRun.GVK(), Object: runObj(big)})
	require.NoError(t, err, "limit ≤ 0 disables the cap")
}
