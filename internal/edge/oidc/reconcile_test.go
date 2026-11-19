package oidc

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

const appCallback = "https://" + appHost + callbackPath

// withRegistration seeds sso with client.registration and its session Secret, and the Route app on app.example.com
// naming it unless noRoute.
func (h *harness) withRegistration(noRoute bool, mutate func(*v1.OIDCProvider)) {
	h.t.Helper()
	h.seed(secretOf(appSession, map[string][]byte{keySessionKey: sessionKey()}))
	p := providerOf(sso, h.idp.Issuer(), v1.OIDCProviderClient{Registration: &v1.OIDCRegistration{}}, appSession)
	if mutate != nil {
		mutate(p)
	}
	h.seed(p)
	if !noRoute {
		h.seed(routeOf("app", appHost, oidcStance(sso), "/"))
	}
}

// registered runs a pass and requires sso Ready with the Secret holding the client.
func (h *harness) registered() (*v1.OIDCProvider, *v1.Secret) {
	h.t.Helper()
	p := h.pass(sso)
	require.Equal(h.t, v1.ConditionTrue, condition(p, condReady).Status, "%+v", condition(p, condReady))
	require.Equal(h.t, ReasonClientReady, condition(p, condReady).Reason)
	s := h.secret(sso)
	for _, k := range []string{keyClientID, keyClientSecret, keyRegistrationToken, keyRegistrationURI} {
		require.NotEmpty(h.t, s.Spec.Data[k], "the Secret holds %s", k)
	}
	return p, s
}

func (h *harness) raiseRotate() {
	h.t.Helper()
	p := h.provider(sso)
	p.Spec.Client.Registration.Rotate++
	h.update(p)
}

// signsIn signs a fresh browser in at / on app.example.com.
func (h *harness) signsIn() {
	h.t.Helper()
	h.browser.Jar = newCookieJar(h.t)
	resp := h.signIn(appHost, "/")
	require.Equal(h.t, "/", resp.Header.Get("Location"))
	got, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(h.t, http.StatusOK, got.StatusCode, "the signed-in browser reaches app")
}

// scenario: oidc-register
func TestScenarioOidcRegister(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{idp: oidcprovider.Options{Plain: true, Public: "https://login.example.com", Path: "/auth/v1"},
		callback: func(*harness) func(string) string { return Callbacks{Scheme: "https"}.URL }})
	h.addons.set(AddonObject{Name: "login", Address: h.idp.Addr()})
	h.withRegistration(false, func(p *v1.OIDCProvider) { p.Spec.Addon = "login" })

	p, s := h.registered()
	require.Equal(t, []string{appCallback}, p.Status.Callbacks)
	require.Equal(t, string(s.Spec.Data[keyClientID]), p.Status.ClientID)
	c := h.idp.Clients()[p.Status.ClientID]
	require.Equal(t, []string{appCallback}, c.RedirectURIs, "the client's redirect URIs are the hosts' callbacks")
	require.Equal(t, "funcd staging sso", c.Name)
	require.Equal(t, "client_secret_post", c.AuthMethod)
	require.True(t, slices.Contains(c.GrantTypes, "authorization_code"))
	h.signsIn()
}

// scenario: oidc-register-unused
func TestScenarioOidcRegisterUnused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(true, nil)
	p := h.pass(sso)
	require.Equal(t, v1.PhasePending, p.Status.Phase)
	require.Equal(t, ReasonUnused, condition(p, condReady).Reason)
	require.Zero(t, h.idp.Calls(oidcprovider.OpRegister), "nothing is registered")

	route := h.seed(routeOf("app", appHost, oidcStance(sso), "/"))
	_, s := h.registered()
	calls := h.idp.Calls(oidcprovider.OpRead) + h.idp.Calls(oidcprovider.OpUpdate) + h.idp.Calls(oidcprovider.OpRegister)
	require.NoError(t, h.st.Delete(h.ctx, v1.KindRoute.GVK(), staging, route.GetObjectMeta().Name, ""))
	p = h.pass(sso)
	require.Equal(t, calls, h.idp.Calls(oidcprovider.OpRead)+h.idp.Calls(oidcprovider.OpUpdate)+h.idp.Calls(oidcprovider.OpRegister),
		"with no host, no call")
	require.Equal(t, v1.ConditionTrue, condition(p, condReady).Status, "the client is kept")
	require.Equal(t, ReasonUnused, condition(p, v1.ConditionClientUpkeep).Reason)
	require.Equal(t, v1.ConditionFalse, condition(p, v1.ConditionClientUpkeep).Status)
	require.Equal(t, s.Spec.Data, h.secret(sso).Spec.Data)

	h.seed(routeOf("app", appHost, oidcStance(sso), "/"))
	p = h.pass(sso)
	require.Equal(t, v1.ConditionTrue, condition(p, v1.ConditionClientUpkeep).Status, "a host names it again")
}

// scenario: oidc-callbacks-follow
func TestScenarioOidcCallbacksFollow(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	h.registered()

	h.seed(routeOf("b", "b.example.com", oidcStance(sso), "/"))
	p := h.pass(sso)
	both := []string{appCallback, "https://b.example.com" + callbackPath}
	require.Equal(t, both, p.Status.Callbacks)
	require.ElementsMatch(t, both, h.idp.Clients()[p.Status.ClientID].RedirectURIs)

	n := h.nsObject()
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthOIDC, Provider: sso}}
	h.update(n)
	h.seed(routeOf("c", "c.example.com", nil, "/"))
	p = h.pass(sso)
	all := append(slices.Clone(both), "https://c.example.com"+callbackPath)
	require.Equal(t, all, p.Status.Callbacks, "a host that inherits the Namespace's stance")
	require.ElementsMatch(t, all, h.idp.Clients()[p.Status.ClientID].RedirectURIs)
}

// The check against the read's redirect_uris: a provider that rolls a callback back with its secret kept gets the
// callbacks again (ADR-0336 Design model).
func TestOidcCallbacksFollowTheRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	p, _ := h.registered()
	snapshot := h.idp.Snapshot()
	c := snapshot[p.Status.ClientID]
	c.RedirectURIs = []string{"https://stale.example.com" + callbackPath}
	snapshot[p.Status.ClientID] = c
	h.idp.Restore(snapshot)
	updates := h.idp.Calls(oidcprovider.OpUpdate)
	h.pass(sso)
	require.Equal(t, updates+1, h.idp.Calls(oidcprovider.OpUpdate))
	require.Equal(t, []string{appCallback}, h.idp.Clients()[p.Status.ClientID].RedirectURIs)
}

// scenario: oidc-rotate
//
// oracle: docs/design/oidc/client-rotation.fizz#NoSignInFailsWithTheCurrentClientStored
func TestScenarioOidcRotate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	_, before := h.registered()
	old := string(before.Spec.Data[keyClientSecret])

	callback := h.startSignIn(appHost, "/")
	// The sign-in read the old secret; the rotation lands before its exchange. The hook runs on the provider's
	// goroutine, so it reports its error instead of failing the test there.
	var rotated error
	h.idp.BeforeNextToken(func() {
		obj, err := h.st.Get(h.ctx, v1.KindOIDCProvider.GVK(), staging, sso)
		if rotated = err; err != nil {
			return
		}
		p := obj.(*v1.OIDCProvider)
		p.Spec.Client.Registration.Rotate++
		if _, rotated = h.st.Update(h.ctx, p); rotated == nil {
			_, rotated = h.rec.Reconcile(h.ctx, controller.Request{GVK: v1.KindOIDCProvider.GVK(), Namespace: staging, Name: sso})
		}
	})
	resp, body := h.get(http.MethodGet, callback, nil)
	require.NoError(t, rotated)
	require.Equal(t, http.StatusFound, resp.StatusCode, "the sign-in succeeds on its retry: %s", body)

	p := h.provider(sso)
	stored := string(h.secret(sso).Spec.Data[keyClientSecret])
	require.NotEqual(t, old, stored, "the Secret holds a new secret")
	require.Equal(t, stored, h.idp.Clients()[p.Status.ClientID].Secret)
	require.Equal(t, int64(1), p.Status.ObservedRotate)
	require.Equal(t, v1.ConditionTrue, condition(p, v1.ConditionClientUpkeep).Status)
	require.Equal(t, 2, h.idp.Calls(oidcprovider.OpToken), "the old secret was refused, the new one redeemed the code")
}

// failingSecrets fails the writes of Secrets while fail is above zero.
type failingSecrets struct {
	store.Store
	mu   sync.Mutex
	fail int
}

func (f *failingSecrets) failing(obj v1.Object) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if obj.GroupVersionKind().Kind != v1.KindSecret || f.fail == 0 {
		return false
	}
	f.fail--
	return true
}

func (f *failingSecrets) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if f.failing(obj) {
		return nil, fault.Unavailablef("test", "the store is down")
	}
	return f.Store.Create(ctx, obj)
}

func (f *failingSecrets) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if f.failing(obj) {
		return nil, fault.Unavailablef("test", "the store is down")
	}
	return f.Store.Update(ctx, obj)
}

// scenario: oidc-answer-lost
//
// oracle: docs/design/oidc/client-rotation.fizz#TheSecretConverges
func TestScenarioOidcAnswerLost(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	h.registered()
	writes := &failingSecrets{Store: h.st}
	h.rec.store = writes
	h.raiseRotate()

	writes.mu.Lock()
	writes.fail = 1
	writes.mu.Unlock()
	updates := h.idp.Calls(oidcprovider.OpUpdate)
	p := h.pass(sso) // the PUT is answered, then the Secret write fails
	require.Equal(t, updates+1, h.idp.Calls(oidcprovider.OpUpdate))
	require.Equal(t, v1.ConditionTrue, condition(p, condReady).Status)
	require.Equal(t, ReasonSecretIssueFailed, condition(p, v1.ConditionClientUpkeep).Reason)
	require.Zero(t, p.Status.ObservedRotate, "nothing is recorded before the Secret holds the answer")
	require.NotEqual(t, string(h.secret(sso).Spec.Data[keyClientSecret]), h.idp.Clients()[p.Status.ClientID].Secret,
		"the answer is lost")

	p = h.pass(sso) // the read carries another secret: the PUT goes again and its answer is stored
	require.Equal(t, updates+2, h.idp.Calls(oidcprovider.OpUpdate))
	require.Equal(t, string(h.secret(sso).Spec.Data[keyClientSecret]), h.idp.Clients()[p.Status.ClientID].Secret, "the secret converges")
	require.Equal(t, int64(1), p.Status.ObservedRotate)
	require.Equal(t, []string{appCallback}, p.Status.Callbacks)
	require.Equal(t, v1.ConditionTrue, condition(p, v1.ConditionClientUpkeep).Status)
	h.signsIn()
}

// scenario: oidc-upkeep-fails-sign-ins-go-on
func TestScenarioOidcUpkeepFailsSignInsGoOn(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	h.registered()

	h.idp.Refuse(oidcprovider.OpRead, http.StatusServiceUnavailable)
	p := h.pass(sso)
	require.Equal(t, v1.PhaseReady, p.Status.Phase, "a failed read keeps a usable client Ready")
	require.Equal(t, ReasonRegistrationFailed, condition(p, v1.ConditionClientUpkeep).Reason)
	require.Contains(t, condition(p, v1.ConditionClientUpkeep).Message, "503")
	h.signsIn()

	h.idp.Refuse(oidcprovider.OpRead, 0)
	h.idp.Refuse(oidcprovider.OpUpdate, http.StatusInternalServerError)
	h.raiseRotate()
	p = h.pass(sso)
	require.Equal(t, v1.PhaseReady, p.Status.Phase, "a failed PUT keeps it Ready")
	require.Equal(t, ReasonRegistrationFailed, condition(p, v1.ConditionClientUpkeep).Reason)
	h.signsIn()
}

// scenario: oidc-rotation-refused
func TestScenarioOidcRotationRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	h.registered()
	h.idp.KeepSecretOnUpdate(true)
	h.raiseRotate()

	updates := h.idp.Calls(oidcprovider.OpUpdate)
	p := h.pass(sso)
	require.Equal(t, updates+1, h.idp.Calls(oidcprovider.OpUpdate))
	require.Equal(t, v1.PhaseReady, p.Status.Phase)
	require.Equal(t, ReasonRotationRefused, condition(p, v1.ConditionClientUpkeep).Reason)
	require.Less(t, p.Status.ObservedRotate, p.Spec.Client.Registration.Rotate)

	h.pass(sso)
	require.Equal(t, updates+1, h.idp.Calls(oidcprovider.OpUpdate), "no faster than each runtime.supervisionPeriod")
	h.clock.Advance(h.rec.period)
	p = h.pass(sso)
	require.Equal(t, updates+2, h.idp.Calls(oidcprovider.OpUpdate), "sent again once the period passed")
	require.Equal(t, ReasonRotationRefused, condition(p, v1.ConditionClientUpkeep).Reason)
	h.signsIn()
}

// scenario: oidc-registration-failed
func TestScenarioOidcRegistrationFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	h.idp.Refuse(oidcprovider.OpRegister, http.StatusBadRequest)
	p := h.pass(sso)
	c := condition(p, condReady)
	require.Equal(t, v1.PhasePending, p.Status.Phase)
	require.Equal(t, ReasonRegistrationFailed, c.Reason)
	require.Contains(t, c.Message, "400", "the message names the HTTP status")
	require.NotContains(t, c.Message, "do-not-echo", "and not the answer's body")
	resp, _ := h.get(http.MethodGet, h.appURL(appHost, "/"), navigate())
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "no usable client")
}

// scenario: oidc-client-gone
func TestScenarioOidcClientGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	first, _ := h.registered()
	h.idp.Purge()
	res, err := h.rec.Reconcile(h.ctx, controller.Request{GVK: v1.KindOIDCProvider.GVK(), Namespace: staging, Name: sso})
	require.NoError(t, err)
	require.Equal(t, h.rec.period, res.RequeueAfter, "a pass runs each runtime.supervisionPeriod")
	p := h.provider(sso)
	require.NotEqual(t, first.Status.ClientID, p.Status.ClientID, "a new client is registered")
	require.Equal(t, string(h.secret(sso).Spec.Data[keyClientID]), p.Status.ClientID)
	require.Equal(t, ReasonClientReady, condition(p, condReady).Reason)
	h.signsIn()
}

// heldGate holds the namespace while held is true.
type heldGate struct {
	mu   sync.Mutex
	held bool
}

func (g *heldGate) set(held bool) { g.mu.Lock(); g.held = held; g.mu.Unlock() }

func (g *heldGate) HeldIn(v1.NamespaceName) bool { g.mu.Lock(); defer g.mu.Unlock(); return g.held }

func (g *heldGate) ReleasedAtIn(v1.NamespaceName) time.Time { return time.Time{} }

// scenario: oidc-after-restore
func TestScenarioOidcAfterRestore(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.withRegistration(false, nil)
	p, _ := h.registered()
	backup := h.idp.Snapshot()
	restored := backup[p.Status.ClientID].Secret
	h.raiseRotate()
	h.pass(sso)
	require.NotEqual(t, restored, string(h.secret(sso).Spec.Data[keyClientSecret]), "a rotation after the Backup")

	held := &heldGate{}
	h.rec.held = held
	held.set(true)
	h.idp.Restore(backup)
	calls := h.idp.Calls(oidcprovider.OpRead) + h.idp.Calls(oidcprovider.OpUpdate) + h.idp.Calls(oidcprovider.OpRegister)
	h.pass(sso)
	require.Equal(t, calls, h.idp.Calls(oidcprovider.OpRead)+h.idp.Calls(oidcprovider.OpUpdate)+h.idp.Calls(oidcprovider.OpRegister),
		"no call while the namespace is held")

	held.set(false)
	p = h.pass(sso)
	stored := string(h.secret(sso).Spec.Data[keyClientSecret])
	require.NotEqual(t, restored, stored, "the restored secret is rotated away")
	require.Equal(t, stored, h.idp.Clients()[p.Status.ClientID].Secret, "the provider refuses the restored one")
	h.signsIn()
}

// scenario: oidc-registration-private
func TestScenarioOidcRegistrationPrivate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{idp: oidcprovider.Options{Plain: true, Public: "https://login.example.com", Path: "/auth/v1"}})
	h.addons.set(AddonObject{Name: "login", Address: h.idp.Addr()})
	h.withRegistration(false, func(p *v1.OIDCProvider) { p.Spec.Addon = "login" })

	_, s := h.registered()
	require.True(t, strings.HasPrefix(string(s.Spec.Data[keyRegistrationURI]), "https://login.example.com/auth/v1/register/"),
		"the provider names the client after its public host")
	h.seed(routeOf("b", "b.example.com", oidcStance(sso), "/"))
	p := h.pass(sso)
	require.Equal(t, 1, h.idp.Calls(oidcprovider.OpUpdate), "kept at the engine's own address")
	require.Len(t, p.Status.Callbacks, 2)
	require.Equal(t, v1.ConditionTrue, condition(p, v1.ConditionClientUpkeep).Status)
}

// scenario: oidc-secret-not-owned
func TestScenarioOidcSecretNotOwned(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	mine := h.seed(secretOf(sso, map[string][]byte{"note": []byte("written by hand")})).(*v1.Secret)
	h.withRegistration(false, nil)
	p := h.pass(sso)
	require.Equal(t, v1.PhasePending, p.Status.Phase)
	require.Equal(t, ReasonSecretNotOwned, condition(p, condReady).Reason)
	left := h.secret(sso)
	require.Equal(t, mine.ResourceVersion, left.ResourceVersion, "the Secret is left alone")
	require.Equal(t, mine.Spec.Data, left.Spec.Data)
	require.Zero(t, h.idp.Calls(oidcprovider.OpRegister))
}

func (h *harness) nsObject() *v1.Namespace {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(staging))
	require.NoError(h.t, err)
	return obj.(*v1.Namespace)
}
