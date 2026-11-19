//go:build e2e

package funcd_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// principalApp is an http(app) export with no build step: the shim knows it by the global symbol that http() sets
// (ADR-0243). It answers the x-funcd-* headers it gets and env.funcd.principal.
const principalApp = `const app = {
  fetch(request, env) {
    const headers = {};
    for (const [name, value] of request.headers) {
      if (name.startsWith('x-funcd-')) headers[name] = value;
    }
    return Response.json({ headers, principal: env.funcd.principal ?? null });
  },
};
export const handle = Object.assign((request, context) => app.fetch(request, { funcd: context }),
  { [Symbol.for('funcd.http')]: true });
`

// shownPrincipal is ctx.principal as a handler answers it.
type shownPrincipal struct {
	Provider string                     `json:"provider"`
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Claims   map[string]json.RawMessage `json:"claims"`
}

// scenario: oidc-shim-principal — a call signed in at the edge gives an http app (TypeScript) the four principal
// headers, the ID token's only with a session, and gives plain handlers (TypeScript, Python) ctx.principal, through
// the released shims.
func TestScenarioOidcShimPrincipal(t *testing.T) {
	t.Parallel()
	idp := oidcprovider.New(t, oidcprovider.Options{})
	h := newShimRig(t, requirePython(t, false), funcd.WithOIDCBackChannel(idp.Transport()))
	ctx := context.Background()

	h.deploy(t, "plain-ts", nodeFn("export function handle(ctx) { return { principal: ctx.principal ?? null }; }\n"))
	h.deploy(t, "plain-py", pythonFn("def handle(ctx, event):\n    return {\"principal\": ctx.principal}\n"))
	file := filepath.Join(t.TempDir(), "app.mjs")
	require.NoError(t, os.WriteFile(file, []byte(principalApp), 0o600))
	ref := "oci-layout://" + h.layout + ":app-ts"
	digest, err := artifact.Push(ctx, ref, file, nil, "", "")
	require.NoError(t, err)
	app := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	app.Name, app.Namespace, app.ResourceGroup = "app-ts", "default", "rg1"
	app.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: ref, ImageDigest: digest,
		Protocol: v1.ProtocolHTTP, Replicas: 1, Scaling: v1.Scaling{MinReplicas: 1}}

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	secret := func(name v1.ObjectName, data map[string][]byte) *v1.Secret {
		s := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
		s.Name, s.Namespace, s.ResourceGroup = name, "default", "rg1"
		s.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: data}
		return s
	}
	sso := &v1.OIDCProvider{TypeMeta: v1.TypeMeta{APIVersion: v1.KindOIDCProvider.GVK().APIVersion(), Kind: v1.KindOIDCProvider}}
	sso.Name, sso.Namespace, sso.ResourceGroup = "sso", "default", "rg1"
	sso.Spec = v1.OIDCProviderSpec{Issuer: idp.Issuer(), Audiences: []string{"api"}, SessionSecret: "sso-session",
		Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}}
	route := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	route.Name, route.Namespace, route.ResourceGroup = "signed-in", "default", "rg1"
	route.Spec = v1.RouteSpec{Host: "app.example.com", Auth: &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"},
		Rules: []v1.RouteRule{
			{Path: "/plain-ts", Backend: v1.RouteBackend{Function: "plain-ts"}},
			{Path: "/plain-py", Backend: v1.RouteBackend{Function: "plain-py"}},
			{Path: "/app", Backend: v1.RouteBackend{Function: "app-ts"}},
		}}
	for _, obj := range []v1.Object{app, secret("sso-session", map[string][]byte{"SESSION_KEY": key}),
		secret("sso-client", map[string][]byte{"CLIENT_ID": []byte("app"), "CLIENT_SECRET": []byte("s3cret")}), sso, route} {
		_, err := h.c.Apply(ctx, obj)
		require.NoError(t, err)
	}
	waitReady(t, h.c, "plain-ts", "plain-py", "app-ts")
	waitCondition(t, "OIDCProvider sso", func() (v1.Conditions, error) {
		obj, err := h.c.Get(ctx, v1.KindOIDCProvider, "default", "sso")
		if err != nil {
			return nil, err
		}
		return obj.(*v1.OIDCProvider).Status.Conditions, nil
	}, "Ready", "ClientReady")
	waitCondition(t, "Route signed-in", func() (v1.Conditions, error) {
		obj, err := h.c.Get(ctx, v1.KindRoute, "default", "signed-in")
		if err != nil {
			return nil, err
		}
		return obj.(*v1.Route).Status.Conditions, nil
	}, "Ready", "Programmed")

	now := time.Now()
	token := idp.Token(oidcprovider.RS256, oidcprovider.Claims{Issuer: idp.Issuer(), Subject: "user-7",
		Audience: []string{"api"}, IssuedAt: now.Unix(), Expiry: now.Add(10 * time.Minute).Unix(), Name: "Zoë Ng"})
	// call sends a plain handler's CloudEvent, a POST of JSON, or an http app's GET, signed in with the token.
	call := func(method, path string) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, h.dp+path, strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Host = "app.example.com"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", path, body)
		return body
	}
	want := shownPrincipal{Provider: "sso", ID: "user-7", Name: "Zoë Ng"}
	for _, path := range []string{"/plain-ts", "/plain-py"} {
		var got struct {
			Principal *shownPrincipal `json:"principal"`
		}
		require.NoError(t, json.Unmarshal(call(http.MethodPost, path), &got), path)
		require.NotNil(t, got.Principal, "%s reads ctx.principal", path)
		require.Equal(t, want.Provider, got.Principal.Provider, path)
		require.Equal(t, want.ID, got.Principal.ID, path)
		require.Equal(t, want.Name, got.Principal.Name, path)
		require.JSONEq(t, `"user-7"`, string(got.Principal.Claims["sub"]), path)
	}

	var seen struct {
		Headers   map[string]string `json:"headers"`
		Principal *shownPrincipal   `json:"principal"`
	}
	require.NoError(t, json.Unmarshal(call(http.MethodGet, "/app"), &seen))
	require.ElementsMatch(t, []string{"x-funcd-principal", "x-funcd-principal-id", "x-funcd-principal-name"},
		slices.Collect(maps.Keys(seen.Headers)), "a Bearer call has no session, so no ID token header, and no other x-funcd-* header")
	require.Equal(t, "user-7", seen.Headers["x-funcd-principal-id"])
	require.NotNil(t, seen.Principal, "the app reads env.funcd.principal")
	require.Equal(t, want.ID, seen.Principal.ID)
	require.Equal(t, want.Name, seen.Principal.Name)
}
