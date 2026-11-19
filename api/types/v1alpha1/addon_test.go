package v1alpha1

import (
	"encoding/json"
	"testing"
	"time"

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
	} {
		o := identityProvider()
		edit(o)
		require.Error(t, o.Validate(), name)
	}

	o = identityProvider()
	o.Spec.Ingress.Auth = &RouteAuth{Mode: AuthOpen}
	require.NoError(t, o.Validate(), "the open mode")
}

// rauthyCopies is rauthy's Addon with hiqlite's snapshot and restore folders (ADR-0333 Decision 1).
func rauthyCopies() *Addon {
	a := rauthyAddon()
	a.Spec.Snapshot = &AddonSnapshot{Path: "/app/data/state_machine/backups"}
	a.Spec.Restore = &AddonRestore{Path: "/app/data/restore", Env: map[string]string{"HQL_BACKUP_RESTORE": `${{ "file:" + restore.file }}`}}
	return a
}

// The path rules of snapshot and restore (ADR-0333 Decision 1): each a folder under storage.path, kept apart, and a
// restore.timeout from 1 s to 1 h, 10 min unset.
func TestAddonValidateCopies(t *testing.T) {
	a := rauthyCopies()
	require.NoError(t, a.Validate())
	require.Equal(t, "state_machine/backups", a.Spec.StorageDir(a.Spec.Snapshot.Path))
	require.Equal(t, "restore", a.Spec.StorageDir(a.Spec.Restore.Path))
	require.Equal(t, 10*time.Minute, a.Spec.Restore.EffectiveTimeout())
	for _, d := range []Duration{MinAddonRestoreTimeout, MaxAddonRestoreTimeout} {
		a.Spec.Restore.Timeout = d
		require.NoError(t, a.Validate(), "%s is a bound", d)
		require.Equal(t, time.Duration(d), a.Spec.Restore.EffectiveTimeout())
	}

	for name, edit := range map[string]func(s *AddonSpec){
		"a snapshot without storage":     func(s *AddonSpec) { s.Storage, s.Restore = nil, nil },
		"a restore without storage":      func(s *AddonSpec) { s.Storage, s.Snapshot = nil, nil },
		"a snapshot at storage.path":     func(s *AddonSpec) { s.Snapshot.Path = "/app/data" },
		"a snapshot outside storage":     func(s *AddonSpec) { s.Snapshot.Path = "/var/backups" },
		"a snapshot beside storage":      func(s *AddonSpec) { s.Snapshot.Path = "/app/data2/backups" },
		"a snapshot ending in /":         func(s *AddonSpec) { s.Snapshot.Path = "/app/data/backups/" },
		"a relative snapshot":            func(s *AddonSpec) { s.Snapshot.Path = "backups" },
		"a restore with ..":              func(s *AddonSpec) { s.Restore.Path = "/app/data/../restore" },
		"one folder for both":            func(s *AddonSpec) { s.Restore.Path = s.Snapshot.Path },
		"a restore inside the snapshot":  func(s *AddonSpec) { s.Restore.Path = s.Snapshot.Path + "/restore" },
		"a snapshot inside the restore":  func(s *AddonSpec) { s.Snapshot.Path = s.Restore.Path + "/backups" },
		"a restore.env key that is bad":  func(s *AddonSpec) { s.Restore.Env = map[string]string{"A=B": "x"} },
		"a restore.timeout under 1s":     func(s *AddonSpec) { s.Restore.Timeout = Duration(500 * time.Millisecond) },
		"a restore.timeout over 1h":      func(s *AddonSpec) { s.Restore.Timeout = Duration(2 * time.Hour) },
		"a negative restore.timeout":     func(s *AddonSpec) { s.Restore.Timeout = -Duration(time.Second) },
		"a storage.path a copy is under": func(s *AddonSpec) { s.Storage.Path = "/app/data/state_machine" },
	} {
		a := rauthyCopies()
		edit(&a.Spec)
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}

	a = rauthyCopies()
	a.Spec.Storage.Path = "/app/data/"
	require.NoError(t, a.Validate(), "a storage.path ending in / holds the same folders")
	require.Equal(t, "restore", a.Spec.StorageDir(a.Spec.Restore.Path))
}

func TestAddonObjectDeletion(t *testing.T) {
	for _, d := range []DeletionPolicy{"", DeletionRetain, DeletionDelete} {
		o := identityProvider()
		o.Spec.Deletion = d
		require.NoError(t, o.Validate(), "%q", d)
	}
	o := identityProvider()
	o.Spec.Deletion = "keep"
	require.Equal(t, fault.Invalid, fault.KindOf(o.Validate()))
}

// A snapshot's or a copy's file name is one path segment (ADR-0333 Decision 6).
func TestIsAddonFile(t *testing.T) {
	for _, ok := range []string{"backup_node_1_1700000000.sqlite", "a", "0.db", "x-y_z.1"} {
		require.True(t, IsAddonFile(ok), ok)
	}
	for _, bad := range []string{"", ".hidden", "..", "a/b", "-x", "_x", "a b", "a\x00", "été"} {
		require.False(t, IsAddonFile(bad), bad)
	}
}
