package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/edge/authn"
)

// fakeCreds resolves a fixed set of tokens to identities.
type fakeCreds map[string]auth.Identity

func (f fakeCreds) Lookup(_ context.Context, token string) (auth.Identity, error) {
	if id, ok := f[token]; ok {
		return id, nil
	}
	return auth.Identity{}, fault.Unauthorizedf("fakeCreds", "unknown token")
}

func enforcer(t *testing.T, creds fakeCreds) *authn.Enforcer {
	t.Helper()
	e, err := authn.New(authn.Deps{Creds: creds, Authz: rbac.New()})
	require.NoError(t, err)
	return e
}

func req(bearer string) *http.Request {
	r := httptest.NewRequest("POST", "http://x/function/f", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func target() activator.FunctionRef { return activator.FunctionRef{Namespace: "team", Name: "f"} }

// scenario: open-route-anonymous
func TestScenarioOpenRouteAnonymous(t *testing.T) {
	e := enforcer(t, fakeCreds{})
	require.NoError(t, e.Enforce(context.Background(), req(""), target(), v1.AuthOpen), "open stance: anonymous OK")
	require.NoError(t, e.Enforce(context.Background(), req(""), target(), ""), "unset stance normalizes to open")
}

// scenario: authn-required-401 (no token) — the unit proves the 401; the e2e proves no-wake.
func TestScenarioAuthnRequired401(t *testing.T) {
	e := enforcer(t, fakeCreds{})
	err := e.Enforce(context.Background(), req(""), target(), v1.AuthAuthenticated)
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "authenticated + no bearer ⇒ 401")
}

// scenario: invalid-token-401
func TestScenarioInvalidToken401(t *testing.T) {
	e := enforcer(t, fakeCreds{"good": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team"}}})
	err := e.Enforce(context.Background(), req("bad"), target(), v1.AuthAuthenticated)
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "authenticated + bad bearer ⇒ 401")
}

// scenario: valid-token-invokes
func TestScenarioValidTokenInvokes(t *testing.T) {
	e := enforcer(t, fakeCreds{"good": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team"}}})
	require.NoError(t, e.Enforce(context.Background(), req("good"), target(), v1.AuthAuthenticated),
		"a valid token scoped to the target namespace is authorized")
}

// scenario: authed-but-unauthorized-403
func TestScenarioAuthedButUnauthorized403(t *testing.T) {
	// A valid developer token scoped to a DIFFERENT namespace → the PDP denies (403).
	e := enforcer(t, fakeCreds{"good": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"other"}}})
	err := e.Enforce(context.Background(), req("good"), target(), v1.AuthAuthenticated)
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "authenticated but not scoped to the target ns ⇒ 403")
}

// An admin token is authorized cluster-wide (delegated decision, not the PEP's).
func TestAdminAuthorizedAnywhere(t *testing.T) {
	e := enforcer(t, fakeCreds{"admin": {Subject: "root", Role: auth.RoleAdmin}})
	require.NoError(t, e.Enforce(context.Background(), req("admin"), target(), v1.AuthAuthenticated))
}

func TestNewRejectsMissingDeps(t *testing.T) {
	_, err := authn.New(authn.Deps{Authz: rbac.New()})
	require.Error(t, err, "credentials required")
	_, err = authn.New(authn.Deps{Creds: fakeCreds{}})
	require.Error(t, err, "authorizer required")
}
