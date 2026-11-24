package funcd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// ADR-0340: an Addon declares settings kinds, whose objects run no engine and which funcd checks at admission.

const (
	policiesPath = "/apis/funcd.io/v1alpha1/namespaces/%s/rauthypasswordpolicys"
	clientsPath  = "/apis/funcd.io/v1alpha1/namespaces/%s/rauthyclients"
)

// rauthySettingsAddon is rauthy's Addon with two settings kinds: RauthyPasswordPolicy, one per IdentityProvider,
// whose provider cannot change and whose lengthMin may not exceed lengthMax; and RauthyClient, at most two per
// namespace, whose clientId is unique.
func rauthySettingsAddon(name v1.ObjectName) *v1.Addon {
	a := rauthyAddon(name)
	a.Spec.Runtimes[0].Attributes.Settings = []v1.AddonSettings{{
		Kind: "RauthyPasswordPolicy", Target: "spec.config.provider", Resync: v1.Duration(time.Minute),
		Function: v1.AddonSettingsFunction{Ref: "rauthy-settings"},
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"provider":{"type":"string"},` +
			`"lengthMin":{"type":"integer"},"lengthMax":{"type":"integer"}},"required":["provider","lengthMin","lengthMax"]}`),
		Constraints: v1.AddonConstraints{
			Count:     v1.AddonCount{PerTarget: 1},
			Immutable: []string{"spec.config.provider"},
			Rules: []v1.AddonRule{{Rule: "${{ object.spec.config.lengthMin <= object.spec.config.lengthMax }}",
				Message: "lengthMin must not exceed lengthMax"}},
		},
	}, {
		Kind: "RauthyClient", Target: "spec.config.provider",
		Function: v1.AddonSettingsFunction{Ref: "rauthy-settings"},
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"provider":{"type":"string"},` +
			`"clientId":{"type":"string"}},"required":["provider"]}`),
		Constraints: v1.AddonConstraints{Count: v1.AddonCount{PerNamespace: 2}, Unique: []string{"spec.config.clientId"}},
	}}
	return a
}

func settingsObject(kind string, ns v1.NamespaceName, name v1.ObjectName, config string) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.Kind(kind)}}
	o.Name, o.Namespace, o.ResourceGroup = name, ns, "rg1"
	o.Spec.Config = json.RawMessage(config)
	return o
}

type policyConfig struct {
	Provider  string `json:"provider"`
	LengthMin int    `json:"lengthMin"`
	LengthMax int    `json:"lengthMax"`
}

type clientConfig struct {
	Provider string `json:"provider"`
	ClientID string `json:"clientId,omitempty"`
}

func policy(ns v1.NamespaceName, name v1.ObjectName, provider string, lengthMin, lengthMax int) *v1.AddonObject {
	b, _ := json.Marshal(policyConfig{Provider: provider, LengthMin: lengthMin, LengthMax: lengthMax})
	return settingsObject("RauthyPasswordPolicy", ns, name, string(b))
}

func client(ns v1.NamespaceName, name v1.ObjectName, clientID string) *v1.AddonObject {
	b, _ := json.Marshal(clientConfig{Provider: "login", ClientID: clientID})
	return settingsObject("RauthyClient", ns, name, string(b))
}

// create posts obj at its kind's plural in its namespace with the developer's token.
func (e *addonEnv) create(t *testing.T, path string, obj *v1.AddonObject) (int, string) {
	t.Helper()
	return e.send(t, http.MethodPost, fmt.Sprintf(path, obj.Namespace), addonDevToken, obj)
}

// replace puts obj over the stored object of its name, at the stored resourceVersion: read again after a 409, since
// funcd writes a settings object's status (ADR-0341 Decision 4).
func (e *addonEnv) replace(t *testing.T, path string, obj *v1.AddonObject) (int, string) {
	t.Helper()
	for range 20 {
		cur, ok := e.object(t, obj.Namespace, obj.Name)
		require.True(t, ok, "%s/%s is stored", obj.Namespace, obj.Name)
		obj.ResourceVersion = cur.ResourceVersion
		status, detail := e.send(t, http.MethodPut, fmt.Sprintf(path, obj.Namespace)+"/"+string(obj.Name), addonDevToken, obj)
		if status != http.StatusConflict {
			return status, detail
		}
	}
	require.Failf(t, "replace", "%s/%s conflicted 20 times", obj.Namespace, obj.Name)
	return 0, ""
}

func created(status int) bool { return status/100 == 2 }

// scenario: settings-kind-served — a developer lists a settings kind's plural in a namespace.
func TestScenarioSettingsKindServed(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	status, detail := e.send(t, http.MethodGet, fmt.Sprintf(policiesPath, "default"), addonDevToken, nil)
	require.Equal(t, http.StatusOK, status, detail)
	status, detail = e.create(t, policiesPath, policy("default", "strict", "login", 16, 64))
	require.True(t, created(status), "%d %s", status, detail)
	status, _ = e.send(t, http.MethodGet, fmt.Sprintf(policiesPath, "default")+"/strict", addonDevToken, nil)
	require.Equal(t, http.StatusOK, status)
}

// scenario: settings-kind-refused — a settings kind that is funcd's, the Addon's own or another Addon's, a schema that
// does not require the target, or a rule over an undeclared field: refused, and nothing is stored.
func TestScenarioSettingsKindRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	whoami := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "whoami"}}
	whoami.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "echoservice", Image: "traefik/whoami" + addonDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "EchoService", Port: 80, Readiness: v1.AddonReadiness{Path: "/health"}}}}}
	e.apply(t, e.admin, whoami)

	for name, c := range map[string]struct {
		mutate func(*v1.Addon)
		status int
		want   string
	}{
		"funcd's kind":     {func(a *v1.Addon) { a.Spec.Runtimes[0].Attributes.Settings[0].Kind = "Function" }, http.StatusBadRequest, "funcd's own"},
		"the Addon's kind": {func(a *v1.Addon) { a.Spec.Runtimes[0].Attributes.Settings[0].Kind = "Identityprovider" }, http.StatusBadRequest, "another kind of the Addon"},
		"another Addon's":  {func(a *v1.Addon) { a.Spec.Runtimes[0].Attributes.Settings[0].Kind = "EchoService" }, http.StatusConflict, "EchoService"},
		"target not required": {func(a *v1.Addon) {
			a.Spec.Runtimes[0].Attributes.Settings[1].Schema = json.RawMessage(`{"type":"object","additionalProperties":false,` +
				`"properties":{"provider":{"type":"string"}}}`)
		}, http.StatusBadRequest, "requires at each level"},
		"rule over an undeclared field": {func(a *v1.Addon) {
			a.Spec.Runtimes[0].Attributes.Settings[0].Constraints.Rules[0].Rule = "${{ object.spec.config.lengthMinimum <= 3 }}"
		}, http.StatusBadRequest, "lengthMinimum"},
	} {
		a := rauthySettingsAddon("rauthy")
		c.mutate(a)
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
		require.Equal(t, c.status, status, "%s: %s", name, detail)
		require.Contains(t, detail, c.want, name)
		_, err := e.admin.Get(e.ctx, v1.KindAddon, "", "rauthy")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s: nothing is stored", name)
	}

	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	w := storedAddon(t, e, "whoami")
	w.Spec.Runtimes[0].Attributes.Settings = rauthySettingsAddon("rauthy").Spec.Runtimes[0].Attributes.Settings[:1]
	status, detail := e.send(t, http.MethodPut, addonsPath+"/whoami", addonAdminToken, w)
	require.Equal(t, http.StatusConflict, status, "an update that adds another Addon's kind: %s", detail)
	require.Contains(t, detail, "RauthyPasswordPolicy")
	require.Empty(t, storedAddon(t, e, "whoami").Spec.Runtimes[0].Attributes.Settings, "the update is not stored")
}

// storedAddon reads a copy of the stored Addon name.
func storedAddon(t *testing.T, e *addonEnv, name v1.ObjectName) *v1.Addon {
	t.Helper()
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindAddon.GVK(), "", name)
	require.NoError(t, err)
	b, err := json.Marshal(obj)
	require.NoError(t, err)
	var a v1.Addon
	require.NoError(t, json.Unmarshal(b, &a))
	return &a
}

// scenario: settings-object-schema — a config that breaks the kind's schema gets 422 naming the property.
func TestScenarioSettingsObjectSchema(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	o := settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","lengthMin":"sixteen","lengthMax":64}`)
	status, detail := e.create(t, policiesPath, o)
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
	_, stored := e.object(t, "default", "strict")
	require.False(t, stored)
}

// scenario: settings-object-no-engine — an engine field is refused with 400; a settings object starts no engine.
func TestScenarioSettingsObjectNoEngine(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	for field, mutate := range map[string]func(*v1.AddonObject){
		"ingress":   func(o *v1.AddonObject) { o.Spec.Ingress = &v1.Ingress{Host: "x.example.com"} },
		"bindings":  func(o *v1.AddonObject) { o.Spec.Bindings.Secret = []v1.ObjectName{"rauthy-keys"} },
		"resources": func(o *v1.AddonObject) { o.Spec.Resources.Memory = "64Mi" },
		"deletion":  func(o *v1.AddonObject) { o.Spec.Deletion = v1.DeletionDelete },
		"functions": func(o *v1.AddonObject) {
			o.Spec.Functions = []v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "rauthy-settings"}}
		},
	} {
		o := policy("default", "strict", "login", 16, 64)
		mutate(o)
		status, detail := e.create(t, policiesPath, o)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", field, detail)
		require.Contains(t, detail, "runs no engine", field)
	}
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 16, 64))
	require.True(t, created(status), "%d %s", status, detail)
	require.True(t, poll.Never(func() bool {
		_, running, _ := e.rt.state(ref("default", "strict"))
		return running
	}, 300*time.Millisecond, 20*time.Millisecond), "no engine runs for a settings object")
}

// scenario: settings-count-per-target — a second policy for one IdentityProvider is refused with 400 naming the first.
func TestScenarioSettingsCountPerTarget(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 16, 64))
	require.True(t, created(status), "%d %s", status, detail)
	status, detail = e.create(t, policiesPath, policy("default", "second", "login", 12, 64))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "count.perTarget")
	require.Contains(t, detail, "strict")
	_, stored := e.object(t, "default", "second")
	require.False(t, stored)
	status, detail = e.create(t, policiesPath, policy("default", "other", "admin-login", 12, 64))
	require.True(t, created(status), "another target: %d %s", status, detail)
}

// scenario: settings-count-per-namespace — the third RauthyClient of a namespace is refused; another namespace takes it.
func TestScenarioSettingsCountPerNamespace(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	for _, n := range []v1.ObjectName{"a", "b"} {
		status, detail := e.create(t, clientsPath, client("team", n, string(n)))
		require.True(t, created(status), "%d %s", status, detail)
	}
	status, detail := e.create(t, clientsPath, client("team", "c", "c"))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "count.perNamespace")
	status, detail = e.create(t, clientsPath, client("default", "c", "c"))
	require.True(t, created(status), "another namespace: %d %s", status, detail)
}

// scenario: settings-unique — a clientId another RauthyClient of the namespace holds is refused; none is admitted.
func TestScenarioSettingsUnique(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	status, detail := e.create(t, clientsPath, client("default", "web-client", "web"))
	require.True(t, created(status), "%d %s", status, detail)
	status, detail = e.create(t, clientsPath, client("default", "web-2", "web"))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "unique")
	require.Contains(t, detail, "web-client")
	status, detail = e.create(t, clientsPath, client("default", "no-id", ""))
	require.True(t, created(status), "an object with no clientId: %d %s", status, detail)
}

// scenario: settings-immutable — a replace that changes the target field is refused with 400 naming it and its value.
func TestScenarioSettingsImmutable(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 16, 64))
	require.True(t, created(status), "%d %s", status, detail)
	status, detail = e.replace(t, policiesPath, policy("default", "strict", "other", 16, 64))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "spec.config.provider")
	require.Contains(t, detail, `"login"`)
	status, detail = e.replace(t, policiesPath, policy("default", "strict", "login", 18, 64))
	require.True(t, created(status), "another field changes: %d %s", status, detail)
}

// scenario: settings-rule — a write that breaks a rule gets 400 with the rule's message.
func TestScenarioSettingsRule(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 40, 20))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "lengthMin must not exceed lengthMax")
	_, stored := e.object(t, "default", "strict")
	require.False(t, stored)
}

// scenario: settings-concurrent-creates — of two policies for one target created at once, one is stored.
func TestScenarioSettingsConcurrentCreates(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	var mu sync.Mutex
	var statuses []int
	var wg sync.WaitGroup
	for _, n := range []v1.ObjectName{"race-a", "race-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := e.create(t, policiesPath, policy("default", n, "login", 16, 64))
			mu.Lock()
			statuses = append(statuses, status)
			mu.Unlock()
		}()
	}
	wg.Wait()
	ok := 0
	for _, s := range statuses {
		if created(s) {
			ok++
		}
	}
	require.Equal(t, 1, ok, "statuses %v", statuses)
	require.Contains(t, statuses, http.StatusBadRequest)
}

// scenario: settings-object-cap — settings objects count against addons.maxSettingsPerNamespace only.
func TestScenarioSettingsObjectCap(t *testing.T) {
	t.Parallel()
	e := startAddons(t, WithAddonObjectQuota(1), WithAddonSettingsQuota(2))
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	for _, n := range []v1.ObjectName{"p1", "p2"} {
		status, detail := e.create(t, policiesPath, policy("default", n, string(n)+"-login", 16, 64))
		require.True(t, created(status), "%d %s", status, detail)
	}
	status, detail := e.create(t, policiesPath, policy("default", "p3", "p3-login", 16, 64))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "addons.maxSettingsPerNamespace")

	e.apply(t, e.dev, identityProvider("default"))
	_, stored := e.object(t, "default", "login")
	require.True(t, stored, "an IdentityProvider can still be created")

	status, detail = e.send(t, http.MethodDelete, fmt.Sprintf(policiesPath, "default")+"/p1", addonDevToken, nil)
	require.True(t, created(status), "%d %s", status, detail)
	status, detail = e.create(t, policiesPath, policy("default", "p4", "p4-login", 16, 64))
	require.True(t, created(status), "the engines' cap reached, a settings object is still admitted: %d %s", status, detail)
}

// An Addon update that changes only spec.settings replaces no engine (ADR-0340, the row "An Addon changed or deleted").
func TestAddonSettingsChangeReplacesNoEngine(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, rauthySettingsAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	_, _, starts := e.rt.state(ref("default", "login"))

	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Resync = v1.Duration(2 * time.Minute)
	a.Spec.Runtimes[0].Attributes.Settings = a.Spec.Runtimes[0].Attributes.Settings[:1]
	e.apply(t, e.admin, a)
	require.True(t, poll.Never(func() bool {
		_, _, n := e.rt.state(ref("default", "login"))
		return n != starts
	}, 500*time.Millisecond, 20*time.Millisecond), "the engine is not replaced")
}
