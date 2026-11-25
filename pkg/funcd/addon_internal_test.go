package funcd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// addonRuntime is a fake add-on engine runtime: every engine is Ready at once, or after bootsIn's passes, and served by one
// httptest engine, which records the path and the body of each call and answers it with that path. It counts the engines it
// starts: a Converge with no engine running, or with another spec than the running engine's, as the real runtime replaces
// one (#1195).
type addonRuntime struct {
	mu        sync.Mutex
	boots     int
	booting   map[provider.ProviderRef]int
	engine    *httptest.Server
	specs     map[provider.ProviderRef]provider.ProviderSpec
	running   map[provider.ProviderRef]bool
	starts    map[provider.ProviderRef]int
	teardowns map[provider.ProviderRef]int
	paths     []string
	bodies    []string
	teardownHold
}

// teardownHold lets a test hold a fake runtime's Teardown, as a slow engine stop would: a hold writes the object's
// status before it stops the engine (#1535).
type teardownHold struct {
	held atomic.Pointer[chan struct{}]
}

// holdTeardowns makes each Teardown wait until release, which the test's cleanup also runs.
func (h *teardownHold) holdTeardowns(t *testing.T) (release func()) {
	t.Helper()
	held := make(chan struct{})
	h.held.Store(&held)
	release = sync.OnceFunc(func() { close(held) })
	t.Cleanup(release)
	return release
}

// waitHold returns once no hold is open.
func (h *teardownHold) waitHold() {
	if held := h.held.Load(); held != nil {
		<-*held
	}
}

func newAddonRuntime(t *testing.T) *addonRuntime {
	r := &addonRuntime{booting: map[provider.ProviderRef]int{}, specs: map[provider.ProviderRef]provider.ProviderSpec{},
		running: map[provider.ProviderRef]bool{}, starts: map[provider.ProviderRef]int{}, teardowns: map[provider.ProviderRef]int{}}
	r.engine = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.paths, r.bodies = append(r.paths, req.Method+" "+req.URL.Path), append(r.bodies, string(body))
		r.mu.Unlock()
		_, _ = io.WriteString(w, req.URL.Path)
	}))
	t.Cleanup(r.engine.Close)
	return r
}

func (r *addonRuntime) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, had := r.specs[spec.Ref]
	if !r.running[spec.Ref] || !had || !reflect.DeepEqual(old, spec) {
		r.starts[spec.Ref]++
		r.booting[spec.Ref] = r.boots
	}
	r.specs[spec.Ref], r.running[spec.Ref] = spec, true
	ready := r.booting[spec.Ref] == 0
	if !ready {
		r.booting[spec.Ref]--
	}
	return provider.ProviderStatus{Running: 1, Ready: ready, Address: strings.TrimPrefix(r.engine.URL, "http://")}, nil
}

func (r *addonRuntime) Teardown(_ context.Context, ref provider.ProviderRef) error {
	r.waitHold()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[ref] {
		r.teardowns[ref]++
	}
	r.running[ref] = false
	return nil
}

// state is ref's last spec, whether its engine runs, and how many engines it started.
func (r *addonRuntime) state(ref provider.ProviderRef) (provider.ProviderSpec, bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.specs[ref], r.running[ref], r.starts[ref]
}

func (r *addonRuntime) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

const (
	addonAdminToken = "addon-test-admin"
	addonDevToken   = "addon-test-developer"
	addonDigest     = "@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809"
)

// addonEnv is a running platform with the fake add-on runtime and kind images, an admin's and a developer's client.
type addonEnv struct {
	ctx        context.Context
	p          *Platform
	rt         *addonRuntime
	images     *kindImages
	admin, dev *sdk.Client
}

func startAddons(t *testing.T, opts ...Option) *addonEnv {
	t.Helper()
	rt := newAddonRuntime(t)
	return startAddonPlatform(t, rt, append([]Option{WithAddonRuntime(rt)}, opts...)...)
}

// startAddonPlatform starts a platform with opts, whose add-on engines run on the fake runtime rt only when opts say so:
// without WithAddonRuntime they run on a process server's own provider runtime, which runs no image.
func startAddonPlatform(t *testing.T, rt *addonRuntime, opts ...Option) *addonEnv {
	t.Helper()
	ctx := context.Background()
	images := newKindImages()
	p, err := New(append([]Option{InMemory(), WithAddonImages(images),
		WithCredentials(Credential{Token: addonAdminToken, Role: "admin"},
			Credential{Token: addonDevToken, Role: "developer", Namespaces: []string{"default", "team"}}),
		WithAddonRegistries("ghcr.io", "docker.io", downRegistry),
		WithPacing(Pacing{SupervisionPeriod: 100 * time.Millisecond, ReferentPollInterval: 50 * time.Millisecond,
			EnginePollInterval: 50 * time.Millisecond})}, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	admin, err := sdk.New("http://"+p.Addr(), sdk.WithToken(addonAdminToken))
	require.NoError(t, err)
	dev, err := sdk.New("http://"+p.Addr(), sdk.WithToken(addonDevToken))
	require.NoError(t, err)
	return &addonEnv{ctx: ctx, p: p, rt: rt, images: images, admin: admin, dev: dev}
}

// rauthyAddon is rauthy's Addon, as pyvvo/funcd-addons ships it in essence: an IdentityProvider at /auth/v1, whose
// dynamic-client path is private, with an issuer in its config and its keys in a Secret.
func rauthyAddon(name v1.ObjectName) *v1.Addon {
	a := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: name}}
	a.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: v1.RuntimeName(name), Image: "ghcr.io/pyvvo/funcd-rauthy" + addonDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, BasePath: "/auth/v1", PrivatePaths: []string{"/auth/v1/clients_dyn"}, Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"}, Storage: &v1.AddonStorage{Path: "/app/data", UID: 10001, GID: 10001}, Config: json.RawMessage(`{"type":"object","properties":{"issuer":{"type":"string"},"port":{"type":"integer"}},` +
			`"required":["issuer","port"],"additionalProperties":false}`), Env: map[string]string{"PUB_URL": "${{ config.issuer }}", "LISTEN_PORT": "${{ config.port }}", "LOG_LEVEL": "info"}, SecretKeys: []string{"ENC_KEYS"}}}}}
	return a
}

// identityProvider is the IdentityProvider login of ns, exposed at login.example.com, binding secrets.
func identityProvider(ns v1.NamespaceName, secrets ...v1.ObjectName) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "IdentityProvider"}}
	o.Name, o.Namespace, o.ResourceGroup = "login", ns, "rg1"
	o.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v1","port":8080}`)
	o.Spec.Bindings.Secret = secrets
	o.Spec.Ingress = &v1.Ingress{Host: "login.example.com"}
	return o
}

func rauthyKeys(ns v1.NamespaceName, data map[string]string) *v1.Secret {
	s := &v1.Secret{TypeMeta: tmeta(v1.KindSecret)}
	s.Name, s.Namespace, s.ResourceGroup = "rauthy-keys", ns, "rg1"
	s.Spec.Data = map[string][]byte{}
	for k, v := range data {
		s.Spec.Data[k] = []byte(v)
	}
	return s
}

// send writes body with token's credential and returns the status and the problem's detail.
func (e *addonEnv) send(t *testing.T, method, path, token string, body v1.Object) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(e.ctx, method, "http://"+e.p.Addr()+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := httpx.NodeClient(5 * time.Second).Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	var p fault.Problem
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	_ = json.Unmarshal(raw, &p)
	return res.StatusCode, p.Detail
}

func (e *addonEnv) apply(t *testing.T, c *sdk.Client, obj v1.Object) {
	t.Helper()
	_, err := c.Apply(e.ctx, obj)
	require.NoError(t, err)
}

func (e *addonEnv) object(t *testing.T, ns v1.NamespaceName, name v1.ObjectName) (*v1.AddonObject, bool) {
	t.Helper()
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindAddonObject.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.AddonObject), true
}

// waitReason waits until ns/name is in phase with the Ready reason reason ("" for Ready).
func (e *addonEnv) waitReason(t *testing.T, ns v1.NamespaceName, name v1.ObjectName, phase v1.Phase, reason string) *v1.AddonObject {
	t.Helper()
	var last *v1.AddonObject
	require.Eventually(t, func() bool {
		o, ok := e.object(t, ns, name)
		if !ok || o.Status.Phase != phase {
			return false
		}
		c, _ := o.Status.Conditions.Get("Ready")
		last = o
		return c.Reason == reason && o.Status.ObservedGeneration == o.Generation
	}, 5*time.Second, 10*time.Millisecond, "%s/%s is %s with reason %q", ns, name, phase, reason)
	return last
}

// stopped waits until the engine of ns/name stops: a hold writes the object's status before it stops the engine, as a
// Lake's does.
func (e *addonEnv) stopped(t *testing.T, ns v1.NamespaceName, name v1.ObjectName) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, running, _ := e.rt.state(ref(ns, name))
		return !running
	}, 5*time.Second, 10*time.Millisecond, "the engine of %s/%s stops", ns, name)
}

func ref(ns v1.NamespaceName, name v1.ObjectName) provider.ProviderRef {
	return provider.ProviderRef{Namespace: ns, Name: name}
}

// edge calls the data plane at host and path.
func (e *addonEnv) edge(t *testing.T, method, host, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, method, "http://"+e.p.DataPlaneAddr()+path, nil)
	require.NoError(t, err)
	req.Host = host
	res, err := httpx.NodeClient(5 * time.Second).Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, string(b)
}

const addonsPath = "/apis/funcd.io/v1alpha1/addons"

// scenario: addon-register-admin-only — a developer's Addon is refused 403; an admin's registers the kind, whose
// plural funcdctl and the SDK list in every namespace with no code for the kind.
func TestScenarioAddonRegisterAdminOnly(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	status, detail := e.send(t, http.MethodPost, addonsPath, addonDevToken, rauthyAddon("rauthy"))
	require.Equal(t, http.StatusForbidden, status, detail)
	status, _ = e.send(t, http.MethodGet, addonsPath, addonDevToken, nil)
	require.Equal(t, http.StatusForbidden, status, "a developer reads no Addon either")

	e.apply(t, e.admin, rauthyAddon("rauthy"))
	kind, ok := sdk.KindFromToken("identityproviders")
	require.True(t, ok)
	for _, ns := range []v1.NamespaceName{"default", "team", "elsewhere"} {
		items, err := e.admin.List(e.ctx, kind, ns)
		require.NoError(t, err, ns)
		require.Empty(t, items, ns)
	}
	items, err := e.dev.List(e.ctx, kind, "team")
	require.NoError(t, err, "a developer lists the kind in its own namespace")
	require.Empty(t, items)
}

// scenario: addon-image-pinned — an image by tag fails the schema, 422; a pinned image from a registry that
// addons.registries does not list is refused 400 (ADR-0148 Decision 1: Invalid is 400).
func TestScenarioAddonImagePinned(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	tagged := rauthyAddon("rauthy")
	tagged.Spec.Runtimes[0].Image = "ghcr.io/pyvvo/funcd-rauthy:0.1.1"
	status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, tagged)
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
	require.Contains(t, detail, "body.spec.runtimes[0].image")

	elsewhere := rauthyAddon("rauthy")
	elsewhere.Spec.Runtimes[0].Image = "quay.io/pyvvo/funcd-rauthy" + addonDigest
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, elsewhere)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "addons.registries")
	_, err := e.admin.Get(e.ctx, v1.KindAddon, "", "rauthy")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing is stored")
}

// scenario: addon-env-checked — an env expression that reads a property the schema lacks is refused at apply, 400
// (ADR-0148 Decision 1).
func TestScenarioAddonEnvChecked(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := rauthyAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Env["ADMIN_EMAIL"] = "${{ config.adminEmail }}"
	status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "ADMIN_EMAIL")

	a.Spec.Runtimes[0].Attributes.Env["ADMIN_EMAIL"] = "admin@${{ config.issuer }}"
	status, detail = e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
	require.Equal(t, http.StatusBadRequest, status, "a value is a literal or one expression: %s", detail)
}

// scenario: addon-object-runs — with its Addon and its Secret, login turns Ready once its engine answers, and the edge
// serves it at its host with the path kept. Its engine is fenced with no endpoint and admits only funcd.
func TestScenarioAddonObjectRuns(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k1"}))
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	o := e.waitReason(t, "default", "login", v1.PhaseReady, "")
	require.Equal(t, strings.TrimPrefix(e.rt.engine.URL, "http://"), o.Status.Address)

	spec, running, _ := e.rt.state(ref("default", "login"))
	require.True(t, running)
	require.Equal(t, "ghcr.io/pyvvo/funcd-rauthy"+addonDigest, spec.Image)
	require.Equal(t, 8080, spec.Port)
	require.Equal(t, provider.ReadinessProbe{Path: "/auth/v1/ping", ExpectStatus: http.StatusOK}, spec.Readiness)
	require.Equal(t, &provider.StorageSpec{Path: "/app/data", UID: 10001, GID: 10001}, spec.Storage)
	require.Equal(t, 1, spec.Replicas)
	require.NotNil(t, spec.EgressOnly, "the engine is fenced")
	require.Empty(t, spec.EgressOnly, "with no endpoint")
	require.True(t, spec.HostOnly, "and only funcd reaches it")
	require.Equal(t, map[string]string{"PUB_URL": "https://login.example.com/auth/v1", "LISTEN_PORT": "8080",
		"LOG_LEVEL": "info", "ENC_KEYS": "k1"}, spec.Env, "a non-string result is its JSON")

	require.Eventually(t, func() bool {
		status, body := e.edge(t, http.MethodGet, "login.example.com", "/auth/v1/ping")
		return status == http.StatusOK && body == "/auth/v1/ping"
	}, 5*time.Second, 10*time.Millisecond, "the edge serves the engine with its path kept")
	c, _ := e.waitReason(t, "default", "login", v1.PhaseReady, "").Status.Conditions.Get("IngressReady")
	require.Equal(t, v1.ConditionTrue, c.Status)

	got, err := e.dev.Get(e.ctx, "identityproviders", "default", "login")
	require.NoError(t, err)
	require.Equal(t, v1.Kind("IdentityProvider"), got.GroupVersionKind().Kind, "the API shows it under its add-on's kind")
}

// scenario: addon-kind-unregistered — an object of a kind no Addon registers is refused 404.
func TestScenarioAddonKindUnregistered(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	_, err := e.dev.Apply(e.ctx, identityProvider("default"))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)
	_, err = e.dev.List(e.ctx, "identityproviders", "default")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a list of a kind no Addon registers is 404 while nothing is stored")
}

// scenario: addon-config-schema — a config that breaks the Addon's schema is refused 422, naming the property.
func TestScenarioAddonConfigSchema(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	o := identityProvider("default")
	o.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v1","port":8080,"colour":"blue"}`)
	status, detail := e.send(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/identityproviders", addonDevToken, o)
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
	require.Contains(t, detail, "colour")
	require.Contains(t, detail, "body.spec.config")
	_, ok := e.object(t, "default", "login")
	require.False(t, ok, "nothing is stored")
}

// scenario: addon-config-invalid — a schema change that login's config breaks is stored, and login waits Pending,
// ConfigInvalid, with no engine.
//
// On a server login keeps the revision it pins, whose schema it fits (ADR-0339 Decision 2,
// TestScenarioAddonRevisionNoMove); under funcdctl dev's rule it follows the current revision at once (Decision 7), and
// so meets the stricter schema.
func TestScenarioAddonConfigInvalid(t *testing.T) {
	t.Parallel()
	addonConfigInvalid(t, false)
}

// The hold writes ConfigInvalid before it stops the engine (#1535): with the stop held past the status, the scenario's
// check still sees the engine stop.
func TestIssue1535_ConfigInvalidWaitsForTheHoldsStop(t *testing.T) {
	t.Parallel()
	addonConfigInvalid(t, true)
}

// addonConfigInvalid is addon-config-invalid's body; held holds the hold's engine stop until after the status is read.
func addonConfigInvalid(t *testing.T, held bool) {
	t.Helper()
	e := startAddons(t, WithAddonsFollowingCurrent())
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	release := func() {}
	if held {
		release = e.rt.holdTeardowns(t)
	}

	e.apply(t, e.admin, rauthyWithRealm())
	o := e.waitReason(t, "default", "login", v1.PhasePending, "ConfigInvalid")
	c, _ := o.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "realm")
	require.Empty(t, o.Status.Address)
	if held {
		_, running, _ := e.rt.state(ref("default", "login"))
		require.True(t, running, "the stop is held past the status")
		time.AfterFunc(100*time.Millisecond, release)
	}
	e.stopped(t, "default", "login")
}

// rauthyWithRealm is rauthy's Addon whose config schema also requires a realm, which login's config lacks.
func rauthyWithRealm() *v1.Addon {
	a := rauthyAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Config = json.RawMessage(`{"type":"object","properties":{"issuer":{"type":"string"},"port":{"type":"integer"},` +
		`"realm":{"type":"string"}},"required":["issuer","port","realm"],"additionalProperties":false}`)
	return a
}

// scenario: addon-removed — a deleted Addon stops login's engine and holds it Pending, AddonNotFound, while get and
// delete still serve it; an Addon of its kind, under another name, runs it again, and one of another kind under the old
// name takes no object.
func TestScenarioAddonRemoved(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")

	require.NoError(t, e.admin.Delete(e.ctx, v1.KindAddon, "", "rauthy"))
	e.waitReason(t, "default", "login", v1.PhasePending, "AddonNotFound")
	e.stopped(t, "default", "login")
	got, err := e.dev.Get(e.ctx, "identityproviders", "default", "login")
	require.NoError(t, err, "get still serves it")
	require.Equal(t, v1.ObjectName("login"), got.GetName())
	items, err := e.dev.List(e.ctx, "identityproviders", "default")
	require.NoError(t, err, "list still serves it")
	require.Len(t, items, 1)
	status, _ := e.send(t, http.MethodPut, "/apis/funcd.io/v1alpha1/namespaces/default/identityproviders/login", addonDevToken,
		identityProvider("default"))
	require.Equal(t, http.StatusNotFound, status, "replace gets 404 while no Addon registers the kind")

	other := rauthyAddon("rauthy")
	other.Spec.Runtimes[0].Attributes.Kind = "EchoService"
	e.apply(t, e.admin, other)
	e.p.controller.Enqueue(controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "login"})
	require.True(t, poll.Never(func() bool {
		_, running, _ := e.rt.state(ref("default", "login"))
		return running
	}, 300*time.Millisecond, 10*time.Millisecond), "an Addon of another kind under the old name takes no object")
	e.waitReason(t, "default", "login", v1.PhasePending, "AddonNotFound")

	e.apply(t, e.admin, rauthyAddon("identity"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	_, running, _ := e.rt.state(ref("default", "login"))
	require.True(t, running, "the Addon of its kind runs it, whatever its name")

	require.NoError(t, e.admin.Delete(e.ctx, v1.KindAddon, "", "identity"))
	e.waitReason(t, "default", "login", v1.PhasePending, "AddonNotFound")
	require.NoError(t, e.dev.Delete(e.ctx, "identityproviders", "default", "login"), "delete still serves it")
	require.Eventually(t, func() bool { _, ok := e.object(t, "default", "login"); return !ok }, 5*time.Second, 10*time.Millisecond)
}

// scenario: addon-private-path — a private path is 404 at the edge however it is spelled, while funcd reaches it at the
// engine's own address; a .. segment or a cleaned path outside basePath is 404.
func TestScenarioAddonPrivatePath(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	o := e.waitReason(t, "default", "login", v1.PhaseReady, "")
	require.Eventually(t, func() bool {
		status, _ := e.edge(t, http.MethodGet, "login.example.com", "/auth/v1/ping")
		return status == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)

	for _, p := range []string{"/auth/v1/clients_dyn", "/auth/v1//clients_dyn", "/auth/v1/./clients_dyn",
		"/auth/v1/x/../clients_dyn", "/auth/v1/../admin", "/auth/v1/x/../ping"} {
		status, _ := e.edge(t, http.MethodPost, "login.example.com", p)
		require.Equal(t, http.StatusNotFound, status, p)
	}
	require.NotContains(t, strings.Join(e.rt.calls(), " "), "clients_dyn", "no private call reached the engine")

	req, err := http.NewRequestWithContext(e.ctx, http.MethodPost, "http://"+o.Status.Address+"/auth/v1/clients_dyn", nil)
	require.NoError(t, err)
	res, err := httpx.NodeClient(5 * time.Second).Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode, "funcd reaches the private path at the engine's own address")
}

// scenario: addon-engine-process-server — on a process server, whose provider runtime runs no image, login waits
// Pending, RuntimeUnavailable, naming the process driver, and no engine starts (ADR-0344 Decision 9).
func TestScenarioAddonEngineProcessServer(t *testing.T) {
	t.Parallel()
	rt := newAddonRuntime(t)
	e := startAddonPlatform(t, rt)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	o := e.waitReason(t, "default", "login", v1.PhasePending, "RuntimeUnavailable")
	c, _ := o.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "process driver")
	require.Contains(t, c.Message, "runs no image")
	_, _, starts := rt.state(ref("default", "login"))
	require.Zero(t, starts, "no engine")
}

// scenario: addon-secret-keys — only the keys secretKeys lists reach the engine, and a changed Secret replaces it.
func TestScenarioAddonSecretKeys(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k1", "BOOTSTRAP_ADMIN_PASSWORD": "p"}))
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	spec, _, starts := e.rt.state(ref("default", "login"))
	require.Equal(t, "k1", spec.Env["ENC_KEYS"])
	require.NotContains(t, spec.Env, "BOOTSTRAP_ADMIN_PASSWORD", "a key secretKeys does not list stays out")

	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k2"}))
	require.Eventually(t, func() bool {
		spec, _, n := e.rt.state(ref("default", "login"))
		return spec.Env["ENC_KEYS"] == "k2" && n == starts+1
	}, 5*time.Second, 10*time.Millisecond, "the changed Secret replaces the engine")
}

// scenario: addon-secret-missing — a bound Secret that does not exist holds login Pending, SecretNotFound, with no
// engine; once applied, login turns Ready.
func TestScenarioAddonSecretMissing(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	e.waitReason(t, "default", "login", v1.PhasePending, "SecretNotFound")
	_, _, starts := e.rt.state(ref("default", "login"))
	require.Zero(t, starts, "no engine")

	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k1"}))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
}

// scenario: addon-second-kind — a second add-on, whoami's EchoService with no storage and no config, runs beside login
// with no code for its kind.
func TestScenarioAddonSecondKind(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	whoami := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "whoami"}}
	whoami.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "echoservice", Image: "docker.io/traefik/whoami" + addonDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "EchoService", Port: 80, Readiness: v1.AddonReadiness{Path: "/health"}}}}}
	e.apply(t, e.admin, whoami)
	e.apply(t, e.dev, identityProvider("default"))
	echo := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "EchoService"}}
	echo.Name, echo.Namespace, echo.ResourceGroup = "echo", "default", "rg1"
	e.apply(t, e.dev, echo)

	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.waitReason(t, "default", "echo", v1.PhaseReady, "")
	spec, _, _ := e.rt.state(ref("default", "echo"))
	require.Equal(t, "docker.io/traefik/whoami"+addonDigest, spec.Image)
	require.Nil(t, spec.Storage)
	items, err := e.dev.List(e.ctx, "echoservices", "default")
	require.NoError(t, err)
	require.Len(t, items, 1, "each kind lists its own objects")
	require.Equal(t, v1.ObjectName("echo"), items[0].GetName())

	clash := identityProvider("default")
	clash.Kind, clash.Spec.Config, clash.Spec.Ingress = "EchoService", nil, nil
	_, err = e.dev.Apply(e.ctx, clash)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "one name is one object per namespace, whatever its kind: %v", err)
}

// ADR-0332 Decision 1: an Addon's kind and plural are funcd's or another Addon's, without case, or its kind changes:
// each is refused, the clash with another Addon 409, the rest 400.
func TestAddonKindRules(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	for kind, want := range map[string]int{
		"Function":         http.StatusBadRequest, // funcd's kind
		"Sqldatabase":      http.StatusBadRequest, // funcd's kind, in another case
		"Volume":           http.StatusBadRequest, // its plural, volumes, is a path funcd serves
		"Deadletter":       http.StatusBadRequest, // and deadletters
		"Identityprovider": http.StatusConflict,   // rauthy's kind, in another case
	} {
		a := rauthyAddon("other")
		a.Spec.Runtimes[0].Attributes.Kind = kind
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
		require.Equal(t, want, status, "%s: %s", kind, detail)
	}
	changed := rauthyAddon("rauthy")
	changed.Spec.Runtimes[0].Attributes.Kind = "Directory"
	status, detail := e.send(t, http.MethodPut, addonsPath+"/rauthy", addonAdminToken, changed)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "cannot change")
}

// ADR-0332 Decision 2: a create past addons.maxObjectsPerNamespace gets 400, all add-on kinds counted together, and
// another namespace has its own count.
func TestAddonObjectCap(t *testing.T) {
	t.Parallel()
	e := startAddons(t, WithAddonObjectQuota(1))
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	second := identityProvider("default")
	second.Name = "login-2"
	status, detail := e.send(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/identityproviders", addonDevToken, second)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "maximum 1 add-on objects")
	e.apply(t, e.dev, identityProvider("team"))
}

// ADR-0332 Decision 6: the reconciler reads no registry list, so an Addon whose registry the server no longer lists
// keeps its engines, and its next change is refused.
func TestAddonRegistryRemovedStopsNoEngine(t *testing.T) {
	t.Parallel()
	e := startAddons(t, WithAddonRegistries("docker.io"))
	a := rauthyAddon("rauthy")
	a.TypeMeta = tmeta(v1.KindAddon)
	_, err := e.p.cfg.store.Create(e.ctx, a)
	require.NoError(t, err, "stored while ghcr.io was listed")
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")

	changed := rauthyAddon("rauthy")
	changed.Spec.Runtimes[0].Attributes.Env["LOG_LEVEL"] = "debug"
	_, err = e.admin.Apply(e.ctx, changed)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "the next change is refused: %v", err)
	_, running, _ := e.rt.state(ref("default", "login"))
	require.True(t, running, "the engine runs on")
}

// ADR-0332 Decision 4: a Policy that forbids the object, as itself, one of its Secrets holds it Pending,
// BindingResolveFailed, with no engine.
func TestAddonSecretForbidden(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k1"}))
	pol := &v1.Policy{TypeMeta: tmeta(v1.KindPolicy)}
	pol.Name, pol.Namespace, pol.ResourceGroup = "no-keys", "default", "rg1"
	pol.Spec.Cedar = `forbid(principal == AddonObject::"default/login", action == Action::"secret::read", resource == Secret::"default/rauthy-keys");`
	e.apply(t, e.dev, pol)
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	o := e.waitReason(t, "default", "login", v1.PhasePending, "BindingResolveFailed")
	c, _ := o.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "forbidden")
	_, _, starts := e.rt.state(ref("default", "login"))
	require.Zero(t, starts)

	require.NoError(t, e.dev.Delete(e.ctx, v1.KindPolicy, "default", "no-keys"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
}

// ADR-0332 Decision 5: the entry's stance is the object's own ingress.auth, open when unset, so a namespace's
// edgeDefaults.auth does not reach it; ingress.path and the authenticated mode are refused.
func TestAddonEntryStance(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	ns := &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "team"}}
	ns.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	e.apply(t, e.admin, ns)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("team"))
	e.waitReason(t, "team", "login", v1.PhaseReady, "")
	require.Eventually(t, func() bool {
		status, _ := e.edge(t, http.MethodGet, "login.example.com", "/auth/v1/ping")
		return status == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "an anonymous call passes: the namespace default does not reach the entry")

	for name, edit := range map[string]func(*v1.Ingress){
		"a path":                 func(in *v1.Ingress) { in.Path = "/auth" },
		"the authenticated mode": func(in *v1.Ingress) { in.Auth = &v1.RouteAuth{Mode: v1.AuthAuthenticated} },
	} {
		o := identityProvider("team")
		o.Name = "other"
		edit(o.Spec.Ingress)
		status, detail := e.send(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team/identityproviders", addonDevToken, o)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
	}
}

// ADR-0220 for an add-on object: a dry-run create and replace admit the object as the write would store it, under its
// add-on's kind, and store nothing.
func TestAddonObjectDryRun(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	got, err := e.dev.Apply(e.ctx, identityProvider("default"), sdk.DryRun())
	require.NoError(t, err)
	require.Equal(t, v1.Kind("IdentityProvider"), got.GroupVersionKind().Kind)
	_, ok := e.object(t, "default", "login")
	require.False(t, ok, "a dry run stores nothing")

	e.apply(t, e.dev, identityProvider("default"))
	edited := identityProvider("default")
	edited.Spec.Resources.Memory = "512Mi"
	got, err = e.dev.Apply(e.ctx, edited, sdk.DryRun())
	require.NoError(t, err)
	require.Equal(t, "512Mi", got.(*v1.AddonObject).Spec.Resources.Memory)
	stored, _ := e.object(t, "default", "login")
	require.Empty(t, stored.Spec.Resources.Memory, "a dry-run replace stores nothing")
}

// ADR-0335 Decision 10: an add-on object whose ingress.auth names no OIDCProvider of its namespace keeps its claim,
// refused, so its paths answer 503, and shows IngressReady=False, ProviderNotFound, until the provider exists.
func TestAddonObjectProviderNotFound(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	login := identityProvider("default")
	login.Spec.Ingress.Auth = &v1.RouteAuth{Mode: v1.AuthOIDC, Provider: "sso"}
	e.apply(t, e.dev, login)
	ingress := func(reason string) func() bool {
		return func() bool {
			o, ok := e.object(t, "default", "login")
			if !ok {
				return false
			}
			c, _ := o.Status.Conditions.Get("IngressReady")
			return c.Reason == reason
		}
	}
	require.Eventually(t, ingress("ProviderNotFound"), 5*time.Second, 10*time.Millisecond)
	status, _ := e.edge(t, http.MethodGet, "login.example.com", "/auth/v1/ping")
	require.Equal(t, http.StatusServiceUnavailable, status, "the entry keeps its claim, refused")

	sso := &v1.OIDCProvider{TypeMeta: tmeta(v1.KindOIDCProvider)}
	sso.Name, sso.Namespace, sso.ResourceGroup = "sso", "default", "rg1"
	sso.Spec = v1.OIDCProviderSpec{Issuer: "https://login.example.com/auth/v1",
		Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}, SessionSecret: "sso-session"}
	e.apply(t, e.dev, sso)
	require.Eventually(t, ingress("Programmed"), 5*time.Second, 10*time.Millisecond, "a new provider ends the wait")
}

// The add-on runtime indexes each engine's address once its worker is created and before it starts, and a failed
// index leaves the worker unstarted (ADR-0332 Decision 3).
func TestAddonRuntimeIndexesEachEngineBeforeItStarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	ctrl, err := controller.New(controller.Deps{Store: st})
	require.NoError(t, err)
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	engineState := func() runtime.State {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for _, in := range rt.insts {
			if in.OwnerKind == v1.KindAddonObject {
				return in.State
			}
		}
		return ""
	}
	var seen []runtime.State
	refuse := true
	s := &addonProvider{cfg: &config{store: st, runtime: rt, imageFor: func(string) string { return "image" }}}
	routes := router.NewAggregator(router.New(), nil, nil, "", nil)
	require.NoError(t, s.build(addonDeps{ctrl: ctrl, routes: routes, indexEngines: func(context.Context) error {
		seen = append(seen, engineState())
		if refuse {
			return fault.Unavailablef("test.indexEngines", "the index cannot be read")
		}
		return nil
	}}))
	addon := rauthyAddon("rauthy")
	addon.Spec.Runtimes[0].Attributes.Storage = nil
	login := identityProvider("default")
	login.TypeMeta, login.Spec.Kind = tmeta(v1.KindAddonObject), "IdentityProvider"
	seedObjects(t, st, addon, login)
	_, err = s.reconciler.Revisions().Reconcile(ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "rauthy"})
	require.NoError(t, err, "the Addon's first revision, which login pins")
	req := controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "login"}

	_, err = s.reconciler.Reconcile(ctx, req)
	require.Error(t, err, "a failed index fails the pass")
	require.Equal(t, runtime.StateCreated, engineState(), "and leaves the engine created, not started")
	refuse = false
	_, err = s.reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, []runtime.State{runtime.StateCreated, runtime.StateCreated}, seen, "each index runs before the start")
	require.Equal(t, runtime.StateRunning, engineState(), "the engine starts once it is indexed")
}
