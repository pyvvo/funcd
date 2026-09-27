package controlplane_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// spyAdmission records what the admission Request carried — to prove the handler wiring (ADR-0063).
type spyAdmission struct {
	calls         int
	lastOp        admission.Operation
	lastOldNotNil bool
	lastSubject   string
}

func (s *spyAdmission) Name() string           { return "spy" }
func (s *spyAdmission) Phase() admission.Phase { return admission.Validating }
func (s *spyAdmission) Handles(_ v1.GroupVersionKind, _ admission.Operation) bool {
	return true
}

func (s *spyAdmission) Admit(_ context.Context, r admission.Request) (v1.Object, error) {
	s.calls++
	s.lastOp = r.Operation
	s.lastOldNotNil = r.Old != nil
	s.lastSubject = r.Identity.Subject
	return r.Object, nil
}

func newServerWith(t *testing.T, extra ...admission.Admission) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
		Admissions:  extra,
	})
	require.NoError(t, err)
	return h
}

// scenario: authz-precedes-admission — an unauthorized write is denied by the PDP BEFORE admission
// runs (a registered spy admission is never called).
func TestScenarioAuthzPrecedesAdmission(t *testing.T) {
	t.Parallel()
	spy := &spyAdmission{}
	srv := newServerWith(t, spy)
	// A viewer may not create → 403 from authz, before admission.
	rec := do(t, srv, http.MethodPost, fnBase, viewToken, functionBody(t, "team-a", "echo", "rg1"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, spy.calls, "admission must not run when authz denies")
}

// scenario: request-carries-old-and-identity — on an Update, the admission Request carries a non-nil
// Old (the stored object) and the caller Identity — the fields ADR-0064's admissions depend on.
func TestScenarioRequestCarriesOldAndIdentity(t *testing.T) {
	t.Parallel()
	spy := &spyAdmission{}
	srv := newServerWith(t, spy)

	rec := do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "echo", "rg1"))
	require.Less(t, rec.Code, 300, "create: %s", rec.Body.String())
	rec = do(t, srv, http.MethodPut, fnBase+"/echo", devToken, functionBody(t, "team-a", "echo", "rg1"))
	require.Less(t, rec.Code, 300, "update: %s", rec.Body.String())

	require.Equal(t, admission.Update, spy.lastOp, "last write was the update")
	require.True(t, spy.lastOldNotNil, "Update admission Request carries the stored Old object")
	require.Equal(t, "dev", spy.lastSubject, "Update admission Request carries the caller Identity")
}
