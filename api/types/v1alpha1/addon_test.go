package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

const testDigest = "sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809"

func rauthyAddon() *Addon {
	return &Addon{
		TypeMeta:   typeMetaFor(KindAddon),
		ObjectMeta: ObjectMeta{Name: "rauthy"},
		Spec: AddonSpec{
			Kind:         "IdentityProvider",
			Image:        "ghcr.io/pyvvo/funcd-rauthy@" + testDigest,
			Port:         8080,
			BasePath:     "/auth/v1",
			PrivatePaths: []string{"/auth/v1/clients_dyn"},
			Readiness:    AddonReadiness{Path: "/auth/v1/ping"},
			Storage:      &AddonStorage{Path: "/app/data", UID: 10001, GID: 10001},
			Config:       json.RawMessage(`{"type":"object","properties":{"issuer":{"type":"string"}},"additionalProperties":false}`),
			Env:          map[string]string{"PUB_URL": "${{ config.issuer }}"},
			SecretKeys:   []string{"ENC_KEYS"},
		},
	}
}

func TestAddonValidate(t *testing.T) {
	require.NoError(t, rauthyAddon().Validate())

	for name, edit := range map[string]func(s *AddonSpec){
		"a kind in lower case":         func(s *AddonSpec) { s.Kind = "identityProvider" },
		"funcd's own kind":             func(s *AddonSpec) { s.Kind = "Function" },
		"funcd's kind in another case": func(s *AddonSpec) { s.Kind = "Sqldatabase" },
		"an image by tag":              func(s *AddonSpec) { s.Image = "ghcr.io/pyvvo/funcd-rauthy:0.1.1" },
		"a tag beside the digest":      func(s *AddonSpec) { s.Image = "ghcr.io/pyvvo/funcd-rauthy:0.1.1@" + testDigest },
		"a process runtime":            func(s *AddonSpec) { s.Runtimes = []string{"process"} },
		"no port":                      func(s *AddonSpec) { s.Port = 0 },
		"a base path ending in /":      func(s *AddonSpec) { s.BasePath = "/auth/v1/" },
		"a base path with ..":          func(s *AddonSpec) { s.BasePath = "/auth/../v1" },
		"the edge's own path":          func(s *AddonSpec) { s.BasePath = "/.auth" },
		"a base path under /.auth":     func(s *AddonSpec) { s.BasePath = "/.auth/v1" },
		"a private path outside":       func(s *AddonSpec) { s.PrivatePaths = []string{"/admin"} },
		"a private path beside":        func(s *AddonSpec) { s.PrivatePaths = []string{"/auth/v10"} },
		"a private path spelled //":    func(s *AddonSpec) { s.PrivatePaths = []string{"/auth/v1//clients_dyn"} },
		"a relative readiness path":    func(s *AddonSpec) { s.Readiness.Path = "ping" },
		"a relative storage path":      func(s *AddonSpec) { s.Storage.Path = "data" },
		"a config that is no object":   func(s *AddonSpec) { s.Config = json.RawMessage(`true`) },
		"an env key that is no name":   func(s *AddonSpec) { s.Env = map[string]string{"A=B": "x"} },
		"a secret key that is no name": func(s *AddonSpec) { s.SecretKeys = []string{"a-b"} },
	} {
		a := rauthyAddon()
		edit(&a.Spec)
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}

	a := rauthyAddon()
	a.Namespace = "default"
	require.Error(t, a.Validate(), "an Addon is cluster-scoped")

	a = rauthyAddon()
	a.Spec.Runtimes, a.Spec.BasePath, a.Spec.PrivatePaths = []string{AddonRuntimeContainerd}, "", []string{"/x"}
	require.NoError(t, a.Validate(), "an empty basePath is /, under which every private path lies")
	require.Equal(t, "/", a.Spec.AddonBasePath())
	a.Spec.Image = "localhost:5000/funcd-rauthy@" + testDigest
	require.NoError(t, a.Validate(), "a registry's port is no tag")
}

func TestAddonRuntime(t *testing.T) {
	var s AddonSpec
	require.True(t, s.Runtime("containerd"), "no runtimes is containerd")
	require.False(t, s.Runtime("process"))
	s.Runtimes = []string{"containerd"}
	require.True(t, s.Runtime("containerd"))
	require.False(t, s.Runtime("process"))
}

func TestAddonPluralAndKindRules(t *testing.T) {
	require.Equal(t, "identityproviders", AddonPlural("IdentityProvider"))
	require.Equal(t, "echoservices", AddonPlural("EchoService"))
	require.True(t, IsAddonKind("IdentityProvider"))
	require.False(t, IsAddonKind("Lake"), "funcd's kind")
	require.False(t, IsAddonKind("lake"), "funcd's kind, without case, and no kind name")
	require.False(t, IsAddonKind("ADDON"), "funcd's Addon, without case")
	require.False(t, IsAddonKind("S3identity"), "an authorization-only kind")
}

func identityProvider() *AddonObject {
	o := &AddonObject{TypeMeta: TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "IdentityProvider"}}
	o.Name, o.Namespace, o.ResourceGroup = "login", "default", "rg1"
	o.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v1"}`)
	o.Spec.Bindings.Secret = []ObjectName{"rauthy-keys"}
	o.Spec.Ingress = &Ingress{Host: "login.example.com"}
	return o
}

func TestAddonObjectValidate(t *testing.T) {
	o := identityProvider()
	require.NoError(t, o.Validate(), "a client's object, under its add-on's kind")
	require.Equal(t, Kind("IdentityProvider"), o.GroupVersionKind().Kind)
	require.Equal(t, "IdentityProvider", o.AddonKind())

	stored := identityProvider()
	stored.TypeMeta = typeMetaFor(KindAddonObject)
	require.Error(t, stored.Validate(), "a stored object names its add-on's kind")
	stored.Spec.Kind = "IdentityProvider"
	require.NoError(t, stored.Validate())
	require.Equal(t, KindAddonObject, stored.GroupVersionKind().Kind)

	for name, edit := range map[string]func(o *AddonObject){
		"funcd's kind":              func(o *AddonObject) { o.Kind = "Lake" },
		"another spec.kind":         func(o *AddonObject) { o.Spec.Kind = "EchoService" },
		"no namespace":              func(o *AddonObject) { o.Namespace = "" },
		"a config that is no map":   func(o *AddonObject) { o.Spec.Config = json.RawMessage(`[1]`) },
		"a Secret name that is bad": func(o *AddonObject) { o.Spec.Bindings.Secret = []ObjectName{"Bad_Name"} },
		"an ingress path":           func(o *AddonObject) { o.Spec.Ingress.Path = "/auth" },
		"the authenticated mode": func(o *AddonObject) {
			o.Spec.Ingress.Auth = &RouteAuth{Mode: AuthAuthenticated}
		},
		"a bad cpu": func(o *AddonObject) { o.Spec.Resources.CPU = "lots" },
		"an oidc stance with no host in default": func(o *AddonObject) {
			o.Spec.Ingress.Host, o.Spec.Ingress.Auth = "", &RouteAuth{Mode: AuthOIDC, Provider: "sso"}
		},
	} {
		o := identityProvider()
		edit(o)
		require.Error(t, o.Validate(), name)
	}

	o = identityProvider()
	o.Spec.Ingress.Auth = &RouteAuth{Mode: AuthOpen}
	require.NoError(t, o.Validate(), "the open mode")
	o.Spec.Ingress.Auth = &RouteAuth{Mode: AuthOIDC, Provider: "sso"}
	require.NoError(t, o.Validate(), "the oidc mode on a host")
}
