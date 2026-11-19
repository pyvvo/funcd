//go:build e2e

package funcd_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/oidc"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0335 scenarios that need the whole platform: the Route, Function and Namespace reconcilers, the aggregator and
// the data plane, over the namespace-host harness. A reached open page reads "<ns>/<name>".

func oidcRoute(ns v1.NamespaceName, name, host, path string, provider v1.ObjectName) *v1.Route {
	r := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec = v1.RouteSpec{Host: host, Auth: &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: provider},
		Rules: []v1.RouteRule{{Path: path, Backend: v1.RouteBackend{Function: v1.ObjectName(name)}}}}
	return r
}

// waitCondition waits until get's condition t has reason.
func waitCondition(t *testing.T, what string, get func() (v1.Conditions, error), ct v1.ConditionType, reason string) {
	t.Helper()
	require.Eventually(t, func() bool {
		cs, err := get()
		if err != nil {
			return false
		}
		c, ok := cs.Get(ct)
		return ok && c.Reason == reason
	}, 15*time.Second, 50*time.Millisecond, "%s reaches %s %s", what, ct, reason)
}

// scenario: oidc-provider-missing
func TestScenarioOidcProviderMissing(t *testing.T) {
	t.Parallel()
	fleet := fnObject("staging", "fleet")
	fleet.Spec.Ingress = &v1.Ingress{Host: "fleet.example.com", Auth: &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "missing"}}
	crew := nsObject("crew", func(n *v1.Namespace) {
		n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthOIDC, Provider: "missing"}}
	})
	h := startNSHost(t, []v1.Object{nsObject("staging", nil), crew, pageRoute("staging", "www", "app.example.com", "/"),
		fnObject("staging", "admin"), oidcRoute("staging", "admin", "app.example.com", "/admin", "missing"), fleet})
	ctx := context.Background()

	h.waitRoute("staging", "admin", "ProviderNotFound")
	require.Equal(t, "staging/www", h.page("app.example.com", "/"), "www serves its own path")
	code, body := h.get(http.MethodGet, "app.example.com", "/admin/x", "")
	require.Equal(t, http.StatusServiceUnavailable, code, "admin keeps /admin and answers 503, not www's page: %s", body)

	waitCondition(t, "Function fleet", func() (v1.Conditions, error) {
		obj, err := h.st.Get(ctx, v1.KindFunction.GVK(), "staging", "fleet")
		if err != nil {
			return nil, err
		}
		return obj.(*v1.Function).Status.Conditions, nil
	}, "IngressReady", "ProviderNotFound")
	waitCondition(t, "Namespace crew", func() (v1.Conditions, error) {
		obj, err := h.st.Get(ctx, v1.KindNamespace.GVK(), "", "crew")
		if err != nil {
			return nil, err
		}
		return obj.(*v1.Namespace).Status.Conditions, nil
	}, oidc.CondEdgeDefaultsReady, "ProviderNotFound")
}

// The platform wires the sign-in: the OIDCProvider reconciler turns sso Ready, and an oidc Route sends a navigation to
// the provider with the host's callback on the data plane's bound port (ADR-0335 Decision 9).
func TestOIDCSignInThroughThePlatform(t *testing.T) {
	t.Parallel()
	idp := oidcprovider.New(t, oidcprovider.Options{})
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	session := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	session.Name, session.Namespace, session.ResourceGroup = "sso-session", "staging", "rg1"
	session.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"SESSION_KEY": key}}
	creds := &v1.Secret{TypeMeta: session.TypeMeta}
	creds.Name, creds.Namespace, creds.ResourceGroup = "sso-client", "staging", "rg1"
	creds.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"CLIENT_ID": []byte("app"), "CLIENT_SECRET": []byte("s3cret")}}
	sso := &v1.OIDCProvider{TypeMeta: v1.TypeMeta{APIVersion: v1.KindOIDCProvider.GVK().APIVersion(), Kind: v1.KindOIDCProvider}}
	sso.Name, sso.Namespace, sso.ResourceGroup = "sso", "staging", "rg1"
	sso.Spec = v1.OIDCProviderSpec{Issuer: idp.Issuer(), SessionSecret: "sso-session", Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}}
	app := pageRoute("staging", "app", "app.example.com", "/")
	app.Spec.Auth = &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}
	h := startNSHost(t, []v1.Object{nsObject("staging", nil), session, creds, sso, app}, funcd.WithOIDCBackChannel(idp.Transport()))
	waitCondition(t, "OIDCProvider sso", func() (v1.Conditions, error) {
		obj, err := h.st.Get(context.Background(), v1.KindOIDCProvider.GVK(), "staging", "sso")
		if err != nil {
			return nil, err
		}
		return obj.(*v1.OIDCProvider).Status.Conditions, nil
	}, "Ready", "ClientReady")
	h.waitRoute("staging", "app", "Programmed")

	req, err := http.NewRequest(http.MethodGet, h.base+"/", nil) //nolint:noctx // test client
	require.NoError(t, err)
	req.Host = "app.example.com"
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	to, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	base, err := url.Parse(h.base)
	require.NoError(t, err)
	require.Equal(t, "http://app.example.com:"+base.Port()+"/.auth/callback", to.Query().Get("redirect_uri"))
	require.Equal(t, "app", to.Query().Get("client_id"))
}
