package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/authcontract"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
)

// recordingAuthorizer records the last request it saw and returns a fixed decision (a test double,
// not a mock framework).
type recordingAuthorizer struct {
	last    *auth.Request
	allowed bool
}

func (r *recordingAuthorizer) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	r.last = &req
	return auth.Decision{Allowed: r.allowed, Reason: "recorded"}, nil
}

// scenario: rbac-unaffected — a routing authorizer over the real rbac driver still satisfies the
// full Authorizer port guarantee for coarse control-plane CRUD (the authcontract suite, unchanged).
func TestScenarioRBACUnaffected(t *testing.T) {
	t.Parallel()
	a, err := auth.NewRoutingAuthorizer(rbac.New(), &recordingAuthorizer{allowed: true})
	require.NoError(t, err)
	authcontract.Run(t, a) // the rbac conformance suite must stay green through routing
}

// scenario: routing — an Action-bearing request goes to the cedar driver; a coarse (no-Action)
// request goes to rbac.
func TestScenarioRoutingDispatch(t *testing.T) {
	t.Parallel()
	cedar := &recordingAuthorizer{allowed: true}
	rbacRec := &recordingAuthorizer{allowed: true}
	a, err := auth.NewRoutingAuthorizer(rbacRec, cedar)
	require.NoError(t, err)
	ctx := context.Background()

	// coarse → rbac
	_, err = a.Authorize(ctx, auth.Request{Verb: auth.VerbGet, Kind: v1.KindFunction, Namespace: "default"})
	require.NoError(t, err)
	require.NotNil(t, rbacRec.last, "coarse request routed to rbac")
	require.Nil(t, cedar.last, "coarse request did NOT reach cedar")

	// Action-bearing → cedar
	rbacRec.last, cedar.last = nil, nil
	_, err = a.Authorize(ctx, auth.Request{
		Action:   auth.ActionKVRead,
		Resource: &auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "orders", Path: "customers"},
	})
	require.NoError(t, err)
	require.NotNil(t, cedar.last, "Action-bearing request routed to cedar")
	require.Nil(t, rbacRec.last, "Action-bearing request did NOT reach rbac")
}

// fail-closed: an Action-bearing request with no cedar driver is denied (default-deny preserved).
func TestRoutingFailsClosedWithoutCedar(t *testing.T) {
	t.Parallel()
	a, err := auth.NewRoutingAuthorizer(rbac.New(), nil)
	require.NoError(t, err)
	dec, err := a.Authorize(context.Background(), auth.Request{Action: auth.ActionKVRead})
	require.NoError(t, err)
	require.False(t, dec.Allowed, "no cedar driver ⇒ Action-bearing request denied (fail closed)")
}
