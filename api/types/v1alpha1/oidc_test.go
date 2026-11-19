package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func oidcProvider(mutate func(*OIDCProvider)) *OIDCProvider {
	p := &OIDCProvider{TypeMeta: typeMetaFor(KindOIDCProvider)}
	p.Name, p.Namespace, p.ResourceGroup = "sso", "staging", "rg1"
	p.Spec = OIDCProviderSpec{Issuer: "https://login.example.com/auth/v1", SessionSecret: "sso-session",
		Client: OIDCProviderClient{Registration: &OIDCRegistration{}}}
	if mutate != nil {
		mutate(p)
	}
	return p
}

// ADR-0335 Decision 1 and ADR-0336 Decision 1: the kind's shape.
func TestOIDCProviderValidate(t *testing.T) {
	t.Parallel()
	require.NoError(t, oidcProvider(nil).Validate())
	require.NoError(t, oidcProvider(func(p *OIDCProvider) { p.Spec.Issuer = "http://localhost:8081/auth/v1" }).Validate(),
		"an http issuer is the reconciler's to refuse without addon")
	for why, mutate := range map[string]func(*OIDCProvider){
		"no issuer":            func(p *OIDCProvider) { p.Spec.Issuer = "" },
		"an issuer with query": func(p *OIDCProvider) { p.Spec.Issuer = "https://login.example.com?x=1" },
		"a relative issuer":    func(p *OIDCProvider) { p.Spec.Issuer = "/auth/v1" },
		"an ftp issuer":        func(p *OIDCProvider) { p.Spec.Issuer = "ftp://login.example.com" },
		"both clients":         func(p *OIDCProvider) { p.Spec.Client.CredentialsSecret = "creds" },
		"no client":            func(p *OIDCProvider) { p.Spec.Client.Registration = nil },
		"no session Secret":    func(p *OIDCProvider) { p.Spec.SessionSecret = "" },
		"an empty audience":    func(p *OIDCProvider) { p.Spec.Audiences = []string{" "} },
		"a negative rotate":    func(p *OIDCProvider) { p.Spec.Client.Registration.Rotate = -1 },
		"a bad Secret name":    func(p *OIDCProvider) { p.Spec.Client.Registration.CredentialSecretName = "Not_A_Label" },
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(oidcProvider(mutate).Validate()), why)
	}
	require.Equal(t, ObjectName("sso"), oidcProvider(nil).ClientSecretName())
	require.Equal(t, ObjectName("mine"), oidcProvider(func(p *OIDCProvider) { p.Spec.Client.Registration.CredentialSecretName = "mine" }).ClientSecretName())
	require.Equal(t, ObjectName("creds"), oidcProvider(func(p *OIDCProvider) {
		p.Spec.Client = OIDCProviderClient{CredentialsSecret: "creds"}
	}).ClientSecretName())
}

// ADR-0335 Decisions 2 and 3: provider and unauthenticated belong to oidc, which needs a provider; /.auth is the edge's;
// an oidc stance in default needs a host.
func TestOIDCStanceValidate(t *testing.T) {
	t.Parallel()
	route := func(ns NamespaceName, host string, auth *RouteAuth, path string) *Route {
		r := &Route{TypeMeta: typeMetaFor(KindRoute)}
		r.Name, r.Namespace, r.ResourceGroup = "app", ns, "rg1"
		r.Spec = RouteSpec{Host: host, Auth: auth, Rules: []RouteRule{{Path: path, Backend: RouteBackend{Function: "app"}}}}
		return r
	}
	oidc := &RouteAuth{Mode: AuthOIDC, Provider: "sso", Unauthenticated: Unauthenticated401}
	require.NoError(t, route("staging", "", oidc, "/").Validate())
	require.NoError(t, route("default", "app.example.com", oidc, "/").Validate())
	for why, r := range map[string]*Route{
		"oidc with no provider":     route("staging", "", &RouteAuth{Mode: AuthOIDC}, "/"),
		"a provider with open":      route("staging", "", &RouteAuth{Mode: AuthOpen, Provider: "sso"}, "/"),
		"unauthenticated with open": route("staging", "", &RouteAuth{Mode: AuthOpen, Unauthenticated: UnauthenticatedAllow}, "/"),
		"another unauthenticated":   route("staging", "", &RouteAuth{Mode: AuthOIDC, Provider: "sso", Unauthenticated: "deny"}, "/"),
		"a rule at /.auth":          route("staging", "", nil, "/.auth"),
		"a rule under /.auth":       route("staging", "", nil, "/.auth/x"),
		"oidc in default, no host":  route("default", "", oidc, "/"),
		"an unknown mode":           route("staging", "", &RouteAuth{Mode: "saml"}, "/"),
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(r.Validate()), why)
	}
	rule := route("default", "", nil, "/")
	rule.Spec.Rules[0].Auth = oidc
	require.Equal(t, fault.Invalid, fault.KindOf(rule.Validate()), "a rule's oidc in default with no host")
	require.NoError(t, route("staging", "", nil, "/.authors").Validate())

	fn := &Function{TypeMeta: typeMetaFor(KindFunction)}
	fn.Name, fn.Namespace, fn.ResourceGroup = "app", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	fn.Spec.Ingress = &Ingress{Auth: oidc}
	require.Equal(t, fault.Invalid, fault.KindOf(fn.Validate()), "an ingress.auth oidc in default without ingress.host")
	fn.Spec.Ingress = &Ingress{Host: "app.example.com", Auth: oidc}
	require.NoError(t, fn.Validate())
	fn.Spec.Ingress = &Ingress{Host: "app.example.com", Path: "/.auth/app"}
	require.Equal(t, fault.Invalid, fault.KindOf(fn.Validate()), "an ingress.path under /.auth")

	n := &Namespace{TypeMeta: typeMetaFor(KindNamespace)}
	n.Name = "default"
	n.Spec.EdgeDefaults = &EdgeDefaults{Auth: &EdgeAuth{Mode: AuthOIDC, Provider: "sso"}}
	require.Equal(t, fault.Invalid, fault.KindOf(n.Validate()), "default's edgeDefaults.auth oidc")
	n.Name = "staging"
	require.NoError(t, n.Validate())
	n.Spec.EdgeDefaults.Auth.Provider = ""
	require.Equal(t, fault.Invalid, fault.KindOf(n.Validate()))
}
