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
		Spec: AddonSpec{Runtimes: []ShippedRuntime{{
			Name: "rauthy", Image: "ghcr.io/pyvvo/funcd-rauthy@" + testDigest, Workloads: []Workload{WorkloadEngine},
			Attributes: &AddonAttributes{
				Kind: "IdentityProvider", Port: 8080, BasePath: "/auth/v1", PrivatePaths: []string{"/auth/v1/clients_dyn"},
				Readiness: AddonReadiness{Path: "/auth/v1/ping"}, Storage: &AddonStorage{Path: "/app/data", UID: 10001, GID: 10001},
				Config: json.RawMessage(`{"type":"object","properties":{"issuer":{"type":"string"}},"additionalProperties":false}`),
				Env:    map[string]string{"PUB_URL": "${{ config.issuer }}"}, SecretKeys: []string{"ENC_KEYS"},
			},
		}}},
	}
}

// engine is the attributes of a's one entry, its engine's.
func engine(a *Addon) *AddonAttributes { return a.Spec.Runtimes[0].Attributes }

// denoAddon ships deno2 for Functions and Jobs, and runs no kind.
func denoAddon() *Addon {
	return &Addon{
		TypeMeta:   typeMetaFor(KindAddon),
		ObjectMeta: ObjectMeta{Name: "deno"},
		Spec: AddonSpec{Runtimes: []ShippedRuntime{{Name: "deno2", Image: "ghcr.io/example/deno@" + testDigest,
			Workloads: []Workload{WorkloadFunction, WorkloadJob}}}},
	}
}

func TestAddonValidate(t *testing.T) {
	require.NoError(t, rauthyAddon().Validate())

	for name, edit := range map[string]func(at *AddonAttributes){
		"a kind in lower case":         func(at *AddonAttributes) { at.Kind = "identityProvider" },
		"funcd's own kind":             func(at *AddonAttributes) { at.Kind = "Function" },
		"funcd's kind in another case": func(at *AddonAttributes) { at.Kind = "Sqldatabase" },
		"no kind":                      func(at *AddonAttributes) { at.Kind = "" },
		"no port":                      func(at *AddonAttributes) { at.Port = 0 },
		"no readiness":                 func(at *AddonAttributes) { at.Readiness = AddonReadiness{} },
		"a base path ending in /":      func(at *AddonAttributes) { at.BasePath = "/auth/v1/" },
		"a base path with ..":          func(at *AddonAttributes) { at.BasePath = "/auth/../v1" },
		"the edge's own path":          func(at *AddonAttributes) { at.BasePath = "/.auth" },
		"a base path under /.auth":     func(at *AddonAttributes) { at.BasePath = "/.auth/v1" },
		"a private path outside":       func(at *AddonAttributes) { at.PrivatePaths = []string{"/admin"} },
		"a private path beside":        func(at *AddonAttributes) { at.PrivatePaths = []string{"/auth/v10"} },
		"a private path spelled //":    func(at *AddonAttributes) { at.PrivatePaths = []string{"/auth/v1//clients_dyn"} },
		"a relative readiness path":    func(at *AddonAttributes) { at.Readiness.Path = "ping" },
		"a relative storage path":      func(at *AddonAttributes) { at.Storage.Path = "data" },
		"a config that is no object":   func(at *AddonAttributes) { at.Config = json.RawMessage(`true`) },
		"an env key that is no name":   func(at *AddonAttributes) { at.Env = map[string]string{"A=B": "x"} },
		"a secret key that is no name": func(at *AddonAttributes) { at.SecretKeys = []string{"a-b"} },
	} {
		a := rauthyAddon()
		edit(engine(a))
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Contains(t, err.Error(), "spec.runtimes[0].attributes", name)
	}

	a := rauthyAddon()
	a.Namespace = "default"
	require.Error(t, a.Validate(), "an Addon is cluster-scoped")

	a = rauthyAddon()
	engine(a).BasePath, engine(a).PrivatePaths = "", []string{"/x"}
	require.NoError(t, a.Validate(), "an empty basePath is /, under which every private path lies")
	require.Equal(t, "/", a.Spec.AddonBasePath())
	a.Spec.Runtimes[0].Image = "localhost:5000/funcd-rauthy@" + testDigest
	require.NoError(t, a.Validate(), "a registry's port is no tag")
}

// The structural refusals of an Addon's runtimes (ADR-0344 Decisions 2 to 5): each names the field, 400.
func TestAddonShapeRefusals(t *testing.T) {
	both := func() *Addon {
		a := rauthyAddon()
		a.Spec.Runtimes = append(a.Spec.Runtimes, denoAddon().Spec.Runtimes...)
		return a
	}
	require.NoError(t, both().Validate(), "an engine entry beside a runtime for Functions and Jobs")
	require.NoError(t, denoAddon().Validate(), "an Addon that runs no kind")

	for name, c := range map[string]struct {
		edit  func(a *Addon)
		field string
	}{
		"no runtimes": {func(a *Addon) { a.Spec.Runtimes = nil }, "spec.runtimes"},
		"two entries that run a kind": {func(a *Addon) {
			a.Spec.Runtimes = append(a.Spec.Runtimes, a.Spec.Runtimes[0])
			a.Spec.Runtimes[2].Name = "rauthy-copy"
		}, "spec.runtimes[2]"},
		"an entry with no image":   {func(a *Addon) { a.Spec.Runtimes[1].Image = "" }, "spec.runtimes[1].image"},
		"an engine with no image":  {func(a *Addon) { a.Spec.Runtimes[0].Image = "" }, "spec.runtimes[0].image"},
		"an image by tag":          {func(a *Addon) { a.Spec.Runtimes[1].Image = "ghcr.io/example/deno:2" }, "spec.runtimes[1].image"},
		"a tag beside the digest":  {func(a *Addon) { a.Spec.Runtimes[0].Image = "ghcr.io/pyvvo/funcd-rauthy:0.1.1@" + testDigest }, "spec.runtimes[0].image"},
		"Engine beside a workload": {func(a *Addon) { a.Spec.Runtimes[0].Workloads = []Workload{WorkloadEngine, WorkloadJob} }, "spec.runtimes[0].workloads"},
		"no workload":              {func(a *Addon) { a.Spec.Runtimes[1].Workloads = nil }, "spec.runtimes[1].workloads"},
		"a workload twice":         {func(a *Addon) { a.Spec.Runtimes[1].Workloads = []Workload{WorkloadJob, WorkloadJob} }, "spec.runtimes[1].workloads[1]"},
		"an unknown workload":      {func(a *Addon) { a.Spec.Runtimes[1].Workloads = []Workload{"Service"} }, "spec.runtimes[1].workloads[0]"},
		"a port on a Function's":   {func(a *Addon) { a.Spec.Runtimes[1].Attributes = &AddonAttributes{Port: 8080} }, "spec.runtimes[1].attributes.port"},
		"a kind on a Job's":        {func(a *Addon) { a.Spec.Runtimes[1].Attributes = &AddonAttributes{Kind: "Deno"} }, "spec.runtimes[1].attributes.kind"},
		"settings outside an engine": {func(a *Addon) {
			a.Spec.Runtimes[1].Attributes = &AddonAttributes{Settings: []AddonSettings{{Kind: "DenoPolicy"}}}
		}, "spec.runtimes[1].attributes.settings"},
		"an engine with no attributes": {func(a *Addon) { a.Spec.Runtimes[0].Attributes = nil }, "spec.runtimes[0].attributes"},
		"a name that is no label":      {func(a *Addon) { a.Spec.Runtimes[1].Name = "Deno_2" }, "spec.runtimes[1].name"},
		"a name of node's family":      {func(a *Addon) { a.Spec.Runtimes[1].Name = "node24" }, "spec.runtimes[1].name"},
		"a name of funcd's runtime":    {func(a *Addon) { a.Spec.Runtimes[1].Name = "nodejs22" }, "spec.runtimes[1].name"},
		"a name of python's family":    {func(a *Addon) { a.Spec.Runtimes[1].Name = "python-tofu" }, "spec.runtimes[1].name"},
		"two entries of one name":      {func(a *Addon) { a.Spec.Runtimes[1].Name = "rauthy" }, "spec.runtimes[1].name"},
	} {
		a := both()
		c.edit(a)
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Contains(t, err.Error(), c.field, name)
	}
}

// infraStackAddon runs the engineless kind InfraStack, which the Function infrastack-reconciler of each object's
// namespace reconciles.
func infraStackAddon() *Addon {
	return &Addon{
		TypeMeta:   typeMetaFor(KindAddon),
		ObjectMeta: ObjectMeta{Name: "infrastack"},
		Spec: AddonSpec{Runtimes: []ShippedRuntime{{Name: "infrastack", Workloads: []Workload{WorkloadEngineless},
			Attributes: &AddonAttributes{Kind: "InfraStack", Config: json.RawMessage(`{"type":"object"}`),
				Function: &AddonSettingsFunction{Ref: "infrastack-reconciler"}}}}},
	}
}

// ADR-0345 Decision 1: an Engineless entry runs its kind with no image, stands alone, and takes only its kind, config,
// function, which it requires, and resync, from 1s to 24h, 10m unset; each refusal names the field.
func TestAddonEnginelessRefusals(t *testing.T) {
	a := infraStackAddon()
	require.NoError(t, a.Validate())
	require.True(t, a.Spec.Engineless())
	require.False(t, rauthyAddon().Spec.Engineless())
	require.Equal(t, "InfraStack", a.Spec.Kind())
	require.Equal(t, 10*time.Minute, engine(a).EffectiveResync())
	engine(a).Resync = Duration(time.Hour)
	require.Equal(t, time.Hour, engine(a).EffectiveResync())
	a.Spec.Runtimes = append(a.Spec.Runtimes, denoAddon().Spec.Runtimes...)
	require.NoError(t, a.Validate(), "an engineless kind beside a runtime for Functions and Jobs")

	for name, c := range map[string]struct {
		edit  func(rt *ShippedRuntime)
		field string
	}{
		"an image":              {func(rt *ShippedRuntime) { rt.Image = "ghcr.io/example/stack@" + testDigest }, "spec.runtimes[0].image"},
		"a Function beside it":  {func(rt *ShippedRuntime) { rt.Workloads = append(rt.Workloads, WorkloadFunction) }, "spec.runtimes[0].workloads"},
		"Engine beside it":      {func(rt *ShippedRuntime) { rt.Workloads = append(rt.Workloads, WorkloadEngine) }, "spec.runtimes[0].workloads"},
		"no attributes":         {func(rt *ShippedRuntime) { rt.Attributes = nil }, "spec.runtimes[0].attributes"},
		"no kind":               {func(rt *ShippedRuntime) { rt.Attributes.Kind = "" }, "spec.runtimes[0].attributes.kind"},
		"funcd's own kind":      {func(rt *ShippedRuntime) { rt.Attributes.Kind = "Job" }, "spec.runtimes[0].attributes.kind"},
		"no function":           {func(rt *ShippedRuntime) { rt.Attributes.Function = nil }, "spec.runtimes[0].attributes.function.ref"},
		"an empty function":     {func(rt *ShippedRuntime) { rt.Attributes.Function = &AddonSettingsFunction{} }, "spec.runtimes[0].attributes.function.ref"},
		"a function no name":    {func(rt *ShippedRuntime) { rt.Attributes.Function.Ref = "Infra_Stack" }, "spec.runtimes[0].attributes.function.ref"},
		"a resync under 1s":     {func(rt *ShippedRuntime) { rt.Attributes.Resync = Duration(500 * time.Millisecond) }, "spec.runtimes[0].attributes.resync"},
		"a resync over 24h":     {func(rt *ShippedRuntime) { rt.Attributes.Resync = Duration(25 * time.Hour) }, "spec.runtimes[0].attributes.resync"},
		"a config not a schema": {func(rt *ShippedRuntime) { rt.Attributes.Config = json.RawMessage(`[]`) }, "spec.runtimes[0].attributes.config"},
		"a port":                {func(rt *ShippedRuntime) { rt.Attributes.Port = 8080 }, "spec.runtimes[0].attributes.port"},
		"a readiness":           {func(rt *ShippedRuntime) { rt.Attributes.Readiness.Path = "/ping" }, "spec.runtimes[0].attributes.readiness"},
		"settings": {func(rt *ShippedRuntime) {
			rt.Attributes.Settings = []AddonSettings{{Kind: "StackPolicy"}}
		}, "spec.runtimes[0].attributes.settings"},
	} {
		a := infraStackAddon()
		c.edit(&a.Spec.Runtimes[0])
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Contains(t, err.Error(), c.field, name)
	}

	r := rauthyAddon()
	engine(r).Function = &AddonSettingsFunction{Ref: "infrastack-reconciler"}
	require.ErrorContains(t, r.Validate(), "spec.runtimes[0].attributes.function", "an Engine entry takes no function")
	r = rauthyAddon()
	engine(r).Resync = Duration(time.Hour)
	require.ErrorContains(t, r.Validate(), "spec.runtimes[0].attributes.resync", "nor a resync")
}

// ADR-0344 Decision 4: a Function on an Addon's runtime is a cloudevent one. No Addon ships a name of the node family,
// and protocol http needs one, so an http Function on deno2 is refused 400.
func TestAddonRuntimeFunctionIsCloudEvent(t *testing.T) {
	fn := &Function{TypeMeta: typeMetaFor(KindFunction), ObjectMeta: ObjectMeta{Name: "web", Namespace: "default", ResourceGroup: "rg1"}}
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image, fn.Spec.Protocol = "deno2", "handle", "registry.example.com/web:1", ProtocolHTTP
	err := fn.Validate()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "needs a node runtime")
	fn.Spec.Protocol = ""
	require.NoError(t, fn.Validate())
}

// The accessors read through the entry that runs the kind (ADR-0344 Contracts): an Addon that runs none adds no kind.
func TestAddonKindRuntime(t *testing.T) {
	a := rauthyAddon()
	a.Spec.Runtimes = append(denoAddon().Spec.Runtimes, a.Spec.Runtimes...)
	rt, ok := a.Spec.KindRuntime()
	require.True(t, ok)
	require.Equal(t, RuntimeName("rauthy"), rt.Name)
	require.Equal(t, "IdentityProvider", a.Spec.Kind())
	require.Equal(t, []string{"IdentityProvider"}, a.Spec.Kinds())
	require.Equal(t, "/auth/v1", a.Spec.AddonBasePath())
	deno, ok := a.Spec.Shipped("deno2")
	require.True(t, ok)
	require.True(t, deno.Serves(WorkloadJob))
	require.False(t, deno.Serves(WorkloadEngine))
	_, ok = a.Spec.Shipped("ruby3")
	require.False(t, ok)

	d := denoAddon()
	_, ok = d.Spec.KindRuntime()
	require.False(t, ok)
	require.Empty(t, d.Spec.Kind())
	require.Empty(t, d.Spec.Kinds())
	require.Equal(t, "/", d.Spec.AddonBasePath())
	require.Nil(t, d.Spec.SettingsKind("RauthyPasswordPolicy"))
	require.Equal(t, WorkloadFunction, WorkloadOf(KindFunction))
	require.Equal(t, WorkloadJob, WorkloadOf(KindJob))
	require.Empty(t, WorkloadOf(KindLake))
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

// rauthyCopies is rauthy's Addon with hiqlite's snapshot and restore folders (ADR-0333 Decision 1).
func rauthyCopies() *Addon {
	a := rauthyAddon()
	engine(a).Snapshot = &AddonSnapshot{Path: "/app/data/state_machine/backups"}
	engine(a).Restore = &AddonRestore{Path: "/app/data/restore", Env: map[string]string{"HQL_BACKUP_RESTORE": `${{ "file:" + restore.file }}`}}
	return a
}

// The path rules of snapshot and restore (ADR-0333 Decision 1): each a folder under storage.path, kept apart, and a
// restore.timeout from 1 s to 1 h, 10 min unset.
func TestAddonValidateCopies(t *testing.T) {
	a := rauthyCopies()
	require.NoError(t, a.Validate())
	at := engine(a)
	require.Equal(t, "state_machine/backups", at.StorageDir(at.Snapshot.Path))
	require.Equal(t, "restore", at.StorageDir(at.Restore.Path))
	require.Equal(t, 10*time.Minute, at.Restore.EffectiveTimeout())
	for _, d := range []Duration{MinAddonRestoreTimeout, MaxAddonRestoreTimeout} {
		at.Restore.Timeout = d
		require.NoError(t, a.Validate(), "%s is a bound", d)
		require.Equal(t, time.Duration(d), at.Restore.EffectiveTimeout())
	}

	for name, edit := range map[string]func(s *AddonAttributes){
		"a snapshot without storage":     func(s *AddonAttributes) { s.Storage, s.Restore = nil, nil },
		"a restore without storage":      func(s *AddonAttributes) { s.Storage, s.Snapshot = nil, nil },
		"a snapshot at storage.path":     func(s *AddonAttributes) { s.Snapshot.Path = "/app/data" },
		"a snapshot outside storage":     func(s *AddonAttributes) { s.Snapshot.Path = "/var/backups" },
		"a snapshot beside storage":      func(s *AddonAttributes) { s.Snapshot.Path = "/app/data2/backups" },
		"a snapshot ending in /":         func(s *AddonAttributes) { s.Snapshot.Path = "/app/data/backups/" },
		"a relative snapshot":            func(s *AddonAttributes) { s.Snapshot.Path = "backups" },
		"a restore with ..":              func(s *AddonAttributes) { s.Restore.Path = "/app/data/../restore" },
		"one folder for both":            func(s *AddonAttributes) { s.Restore.Path = s.Snapshot.Path },
		"a restore inside the snapshot":  func(s *AddonAttributes) { s.Restore.Path = s.Snapshot.Path + "/restore" },
		"a snapshot inside the restore":  func(s *AddonAttributes) { s.Snapshot.Path = s.Restore.Path + "/backups" },
		"a restore.env key that is bad":  func(s *AddonAttributes) { s.Restore.Env = map[string]string{"A=B": "x"} },
		"a restore.timeout under 1s":     func(s *AddonAttributes) { s.Restore.Timeout = Duration(500 * time.Millisecond) },
		"a restore.timeout over 1h":      func(s *AddonAttributes) { s.Restore.Timeout = Duration(2 * time.Hour) },
		"a negative restore.timeout":     func(s *AddonAttributes) { s.Restore.Timeout = -Duration(time.Second) },
		"a storage.path a copy is under": func(s *AddonAttributes) { s.Storage.Path = "/app/data/state_machine" },
	} {
		a := rauthyCopies()
		edit(engine(a))
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}

	a = rauthyCopies()
	engine(a).Storage.Path = "/app/data/"
	require.NoError(t, a.Validate(), "a storage.path ending in / holds the same folders")
	require.Equal(t, "restore", engine(a).StorageDir(engine(a).Restore.Path))
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

// ADR-0340 Decision 1: a settings kind's name, Function, resync, schema and constraint paths.
func TestAddonValidateSettings(t *testing.T) {
	withSettings := func() *Addon {
		a := rauthyAddon()
		engine(a).Settings = []AddonSettings{{
			Kind: "RauthyPasswordPolicy", Target: "spec.config.provider", Resync: Duration(time.Minute),
			Function: AddonSettingsFunction{Ref: "rauthy-settings"},
			Schema:   json.RawMessage(`{"type":"object","properties":{"provider":{"type":"string"}},"required":["provider"]}`),
			Constraints: AddonConstraints{Count: AddonCount{PerTarget: 1}, Immutable: []string{"spec.config.provider"},
				Unique: []string{"spec.config.provider"}, Rules: []AddonRule{{Rule: "${{ true }}", Message: "always"}}},
		}}
		return a
	}
	require.NoError(t, withSettings().Validate())
	require.Equal(t, []string{"IdentityProvider", "RauthyPasswordPolicy"}, withSettings().Spec.Kinds())
	require.NotNil(t, withSettings().Spec.SettingsKind("rauthypasswordpolicy"), "a kind is found by its plural, whatever the case")
	require.Equal(t, time.Duration(DefaultAddonResync), (&AddonSettings{}).EffectiveResync())

	for name, edit := range map[string]func(s *AddonSettings){
		"a kind that is no name":         func(s *AddonSettings) { s.Kind = "rauthy-policy" },
		"funcd's own kind":               func(s *AddonSettings) { s.Kind = "Secret" },
		"the Addon's own kind":           func(s *AddonSettings) { s.Kind = "Identityprovider" },
		"no Function":                    func(s *AddonSettings) { s.Function.Ref = "" },
		"a Function that is no name":     func(s *AddonSettings) { s.Function.Ref = "Rauthy_Settings" },
		"no schema":                      func(s *AddonSettings) { s.Schema = nil },
		"a schema that is no object":     func(s *AddonSettings) { s.Schema = json.RawMessage(`[]`) },
		"a resync under 1s":              func(s *AddonSettings) { s.Resync = Duration(time.Millisecond) },
		"a resync over 24h":              func(s *AddonSettings) { s.Resync = Duration(25 * time.Hour) },
		"a target outside spec.config":   func(s *AddonSettings) { s.Target = "metadata.name" },
		"a target with an empty segment": func(s *AddonSettings) { s.Target = "spec.config..provider" },
		"an immutable path outside":      func(s *AddonSettings) { s.Constraints.Immutable = []string{"spec.provider"} },
		"a negative count":               func(s *AddonSettings) { s.Constraints.Count.PerNamespace = -1 },
		"a rule with no message":         func(s *AddonSettings) { s.Constraints.Rules[0].Message = "" },
	} {
		a := withSettings()
		edit(&engine(a).Settings[0])
		err := a.Validate()
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}

	a := withSettings()
	engine(a).Settings = append(engine(a).Settings, engine(a).Settings[0])
	engine(a).Settings[1].Kind = "RAUTHYPASSWORDPOLICY"
	require.Error(t, a.Validate(), "two settings kinds of one plural")
}
