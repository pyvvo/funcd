package controlplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// TestIssue718_RunPhasesRoundTrip: a WorkflowRun in any phase the engine writes (ADR-0094) is described by the
// served spec, and a GET-modify-PUT that echoes its status back is accepted.
func TestIssue718_RunPhasesRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store:      st,
		Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		}),
	})
	require.NoError(t, err)

	rec := do(t, srv, http.MethodGet, "/openapi.json", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
	phaseEnum := spec.Components.Schemas["WorkflowRunStatus"].Properties["phase"].Enum

	for _, phase := range []string{"Pending", "Running", "Paused", "Succeeded", "Failed", "Cancelled"} {
		t.Run(phase, func(t *testing.T) {
			assert.Contains(t, phaseEnum, phase, "the spec's WorkflowRunStatus.phase")

			name := "r-" + strings.ToLower(phase)
			obj, _ := v1.NewObject(v1.KindWorkflowRun)
			run := obj.(*v1.WorkflowRun)
			run.Name, run.Namespace, run.ResourceGroup, run.Spec.Workflow = v1.ObjectName(name), "team-a", "g", "wf"
			created, err := st.Create(ctx, run)
			require.NoError(t, err)
			run = created.(*v1.WorkflowRun)
			require.NoError(t, json.Unmarshal(fmt.Appendf(nil, `{"phase":%q}`, phase), &run.Status))
			_, err = st.Update(ctx, run)
			require.NoError(t, err)

			path := kindPath("team-a", "workflowruns") + "/" + name
			rec := do(t, srv, http.MethodGet, path, devToken, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, phase, body["status"].(map[string]interface{})["phase"])
			body["metadata"].(map[string]interface{})["tags"] = map[string]interface{}{"team": "x"}
			put, err := json.Marshal(body)
			require.NoError(t, err)

			rec = do(t, srv, http.MethodPut, path, devToken, put)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}
