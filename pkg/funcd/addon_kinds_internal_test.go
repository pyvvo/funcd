package funcd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ADR-0358: an add-on kind's Function comes from an image, and its contract is the kind's schema.

// downRegistry is a registry that refuses connections: a kind's image there is one whose contract the test's kindImages
// says, and whose Function never runs.
const downRegistry = "127.0.0.1:1"

const stacksPath = "/apis/funcd.io/v1alpha1/namespaces/%s/infrastacks"

// The images of rauthy's settings kinds, and of InfraStack.
const (
	settingsImage      = downRegistry + "/pyvvo/rauthy-settings" + addonDigest
	newerSettingsImage = downRegistry + "/pyvvo/rauthy-settings@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	stacksImage        = downRegistry + "/pyvvo/infrastack-reconciler" + addonDigest
)

const (
	policyProperties = `"provider":{"type":"string"},"lengthMin":{"type":"integer"},"lengthMax":{"type":"integer"}`
	policyCall       = `{"properties":{` + policyProperties + `},"required":["provider","lengthMin","lengthMax"]}`
	agedPolicyCall   = `{"properties":{` + policyProperties + `,"maxAge":{"type":"integer"}},"required":["provider","lengthMin","lengthMax"]}`
	clientCall       = `{"properties":{"provider":{"type":"string"},"clientId":{"type":"string"}},"required":["provider"]}`
)

// kindCall is the schema of a call of kind, whose objects' config is config: its properties and what it requires.
func kindCall(kind, config string) string {
	return `{"type":"object","additionalProperties":false,"required":["kind","cause","objects"],"properties":{` +
		`"kind":{"type":"string","enum":["` + kind + `"]},"cause":{"type":"string"},` +
		`"target":{"type":"object","additionalProperties":false,"properties":{"kind":{"type":"string"},"name":{"type":"string"},` +
		`"alias":{"type":"string"}}},"objects":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{` +
		`"name":{"type":"string"},"uid":{"type":"string"},"generation":{"type":"integer"},"deleting":{"type":"boolean"},` +
		`"config":{"type":"object","additionalProperties":false,` + strings.TrimPrefix(config, "{") + `}}}}}`
}

// settingsContract is the contract of a Function that serves both of rauthy's settings kinds: a union of their calls,
// discriminated by kind.
func settingsContract(policy, client string) v1.WorkflowContract {
	return v1.WorkflowContract{Input: json.RawMessage(`{"oneOf":[` + kindCall("RauthyPasswordPolicy", policy) + `,` +
		kindCall("RauthyClient", client) + `],"discriminator":{"propertyName":"kind"}}`)}
}

// kindImages says the contract of each image it holds, and fails the others as a registry that is down.
type kindImages struct {
	mu   sync.Mutex
	says map[string]v1.WorkflowContract
}

func newKindImages() *kindImages {
	return &kindImages{says: map[string]v1.WorkflowContract{
		settingsImage: settingsContract(policyCall, clientCall),
		stacksImage:   {Input: json.RawMessage(kindCall("InfraStack", `{"properties":{"region":{"type":"string"}}}`))},
	}}
}

func (f *kindImages) Read(_ context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.says[image]
	if !ok {
		return "", v1.WorkflowContract{}, fault.Unavailablef("test", "%s is down", downRegistry)
	}
	return "nodejs22", c, nil
}

func (f *kindImages) set(image string, c v1.WorkflowContract) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.says[image] = c
}

// register applies a and waits until funcd read the images of its kinds and its constraints fit them.
func (e *addonEnv) register(t *testing.T, a *v1.Addon) {
	t.Helper()
	e.apply(t, e.admin, a)
	e.waitAddon(t, a.Name, "")
}

// waitAddon waits until the Addon name reports reason at its generation, "" for Ready, and returns it.
func (e *addonEnv) waitAddon(t *testing.T, name v1.ObjectName, reason string) *v1.Addon {
	t.Helper()
	var a *v1.Addon
	require.Eventually(t, func() bool {
		a = storedAddon(t, e, name)
		c, ok := a.Status.Conditions.Get("Ready")
		return ok && c.ObservedGeneration == a.Generation && c.Reason == reason && (reason != "" || c.Status == v1.ConditionTrue)
	}, 5*time.Second, 10*time.Millisecond, "the Addon %s reports %q", name, reason)
	return a
}

// function is the stored Function ns/name, nil when there is none.
func (e *addonEnv) function(t *testing.T, ns v1.NamespaceName, name v1.ObjectName) *v1.Function {
	t.Helper()
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindFunction.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// waitFunction waits until the Function ns/name is there, or is gone.
func (e *addonEnv) waitFunction(t *testing.T, ns v1.NamespaceName, name v1.ObjectName, there bool) {
	t.Helper()
	require.Eventually(t, func() bool { return (e.function(t, ns, name) != nil) == there }, 10*time.Second, 20*time.Millisecond,
		"the Function %s/%s there: %v", ns, name, there)
}

// agedPolicy is the policy name of provider with a maxAge, which newerSettingsImage's contract alone takes.
func agedPolicy(name v1.ObjectName, provider string) *v1.AddonObject {
	return settingsObject("RauthyPasswordPolicy", "default", name, `{"provider":"`+provider+`","lengthMin":8,"lengthMax":64,"maxAge":30}`)
}

// scenario: addon-contract-is-the-schema — a property that the contract's config lacks, or a value of another type, gets
// 422 naming the property.
func TestScenarioAddonContractIsTheSchema(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	for config, property := range map[string]string{
		`{"provider":"login","lengthMin":8,"lengthMax":64,"maxAge":30}`: "maxAge",
		`{"provider":"login","lengthMin":"eight","lengthMax":64}`:       "lengthMin",
		`{"provider":"login","lengthMin":8}`:                            "lengthMax",
	} {
		status, detail := e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", config))
		require.Equal(t, http.StatusUnprocessableEntity, status, "%s: %s", config, detail)
		require.Contains(t, detail, property, config)
	}
	_, stored := e.object(t, "default", "strict")
	require.False(t, stored)
	status, detail := e.create(t, clientsPath, settingsObject("RauthyClient", "default", "web", `{"provider":"login","lengthMin":8}`))
	require.Equal(t, http.StatusUnprocessableEntity, status, "the other kind's schema is its own: %s", detail)
	status, detail = e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
}

// scenario: addon-contract-of-the-pin — a newer revision's contract adds maxAge: a policy that sets it gets 422 while
// login pins the former one, and is stored once an edit of login moved it; an App's upgrade whose parts are both is
// admitted.
func TestScenarioAddonContractOfThePin(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.pin(t, "default", "login", "rauthy-1", rauthyAddon("rauthy").Spec.Runtimes[0].Image)
	withPolicy := func(o *v1.AddonObject) *v1.App {
		app := loginApp("default", "web-auth")
		app.Spec.Addons = append(app.Spec.Addons, v1.AppAddon{Name: "web-auth-strict", AddonObjectSpec: v1.AddonObjectSpec{
			Kind: "RauthyPasswordPolicy", Config: o.Spec.Config}})
		return app
	}
	e.apply(t, e.dev, withPolicy(policy("default", "", "web-auth-login", 8, 64)))
	e.pin(t, "default", "web-auth-login", "rauthy-1", rauthyAddon("rauthy").Spec.Runtimes[0].Image)

	e.images.set(newerSettingsImage, settingsContract(agedPolicyCall, clientCall))
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Function.Image = newerSettingsImage
	e.register(t, a)
	e.waitCurrent(t, "rauthy-2")
	status, detail := e.create(t, policiesPath, agedPolicy("strict", "login"))
	require.Equal(t, http.StatusUnprocessableEntity, status, "login pins rauthy-1: %s", detail)
	require.Contains(t, detail, "maxAge")
	status, detail = e.replace(t, policiesPath, agedPolicy("web-auth-strict", "web-auth-login"))
	require.Equal(t, http.StatusUnprocessableEntity, status, "web-auth-login pins rauthy-1: %s", detail)

	e.moved(t, "rauthy-2")
	status, detail = e.create(t, policiesPath, agedPolicy("strict", "login"))
	require.True(t, created(status), "login moved: %d %s", status, detail)

	e.apply(t, e.dev, withPolicy(agedPolicy("", "web-auth-login"))) // an upgrade whose parts are both moves the target with the policy
}

// scenario: addon-function-ref — with function.ref funcd creates nothing, and a write is checked against no schema.
func TestScenarioAddonFunctionRef(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := rauthySettingsAddon("rauthy")
	for i := range a.Spec.Runtimes[0].Attributes.Settings {
		st := &a.Spec.Runtimes[0].Attributes.Settings[i]
		st.Function, st.Constraints.Rules = v1.AddonFunction{Ref: "rauthy-settings"}, nil
	}
	e.register(t, a)
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	status, detail := e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","colour":"blue"}`))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitReason(t, "default", "strict", v1.PhasePending, "FunctionNotFound")
	require.Nil(t, e.function(t, "default", "login-rauthypasswordpolicy"), "funcd creates no Function for a ref")
}

// scenario: addon-function-own — login names its own Function in spec.functions: a write of its policy is checked as a
// ref's, against no schema, and funcd's own Function goes.
func TestScenarioAddonFunctionOwnWrite(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", true)

	e.apply(t, e.dev, settingsFn("default", "login"))
	own := identityProvider("default")
	own.Spec.Functions = []v1.AddonObjectFunction{{Kind: "RauthyPasswordPolicy", Ref: "rauthy-settings"}}
	e.apply(t, e.dev, own)
	status, detail = e.replace(t, policiesPath, agedPolicy("strict", "login"))
	require.True(t, created(status), "no schema judges it: %d %s", status, detail)
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", false)
	require.NotNil(t, e.function(t, "default", "rauthy-settings"))
}

// scenario: addon-constraint-unfit — a rule over a field that the contract lacks makes the Addon Ready=False with
// SettingsInvalid, naming it, and a write that the rule judges gets 400; a replace that changes the kind's target gets
// 400.
func TestScenarioAddonConstraintUnfit(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Constraints.Rules[0].Rule = "${{ object.spec.config.maxAge > 1 }}"
	e.apply(t, e.admin, a)
	got := e.waitAddon(t, "rauthy", "SettingsInvalid")
	c, _ := got.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "RauthyPasswordPolicy")
	require.Contains(t, c.Message, "maxAge")
	require.Equal(t, v1.ObjectName("rauthy-1"), got.Status.CurrentRevision, "its revision is current")
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "maxAge")
	status, detail = e.create(t, clientsPath, client("default", "web", "web"))
	require.True(t, created(status), "the other kind is judged by its own constraints: %d %s", status, detail)

	e.register(t, rauthySettingsAddon("rauthy"))
	moved := storedAddon(t, e, "rauthy")
	moved.Spec.Runtimes[0].Attributes.Settings[0].Target = "spec.config.idp"
	status, detail = e.send(t, http.MethodPut, addonsPath+"/rauthy", addonAdminToken, moved)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, `the target of the settings kind "RauthyPasswordPolicy" cannot change`)
}

// scenario: addon-image-unread — while the image cannot be read, the Addon is Ready=False with ArtifactNotReady, login's
// engine runs, and a policy is stored and Pending with ArtifactNotReady; once it can be, the policy meets the schema.
func TestScenarioAddonImageUnread(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Function.Image = newerSettingsImage
	e.apply(t, e.admin, a)
	got := e.waitAddon(t, "rauthy", "ArtifactNotReady")
	c, _ := got.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "RauthyPasswordPolicy")
	require.Contains(t, c.Message, downRegistry)
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	status, detail := e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","lengthMin":"eight"}`))
	require.True(t, created(status), "no contract judges it yet: %d %s", status, detail)
	e.waitReason(t, "default", "strict", v1.PhasePending, "ArtifactNotReady")

	e.images.set(newerSettingsImage, settingsContract(policyCall, clientCall))
	e.waitAddon(t, "rauthy", "")
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", true)
	status, detail = e.replace(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","lengthMin":"nine"}`))
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
}

// scenario: addon-function-repaired — a created Function, which binds login's Secrets, is back when it is deleted by hand
// while a policy names login, and goes with login.
func TestScenarioAddonFunctionRepaired(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	e.apply(t, e.dev, rauthyKeys("default", map[string]string{"ENC_KEYS": "k"}))
	e.apply(t, e.dev, identityProvider("default", "rauthy-keys"))
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", true)
	fn := e.function(t, "default", "login-rauthypasswordpolicy")
	require.Equal(t, []v1.ObjectName{"rauthy-keys"}, fn.Spec.Bindings.Secret)
	first := fn.UID

	require.NoError(t, e.dev.Delete(e.ctx, v1.KindFunction, "default", "login-rauthypasswordpolicy"))
	require.Eventually(t, func() bool {
		fn := e.function(t, "default", "login-rauthypasswordpolicy")
		return fn != nil && fn.UID != first
	}, 5*time.Second, 20*time.Millisecond, "it is back")

	require.NoError(t, e.dev.Delete(e.ctx, "IdentityProvider", "default", "login"))
	e.waitFunction(t, "default", "login-rauthypasswordpolicy", false)
	_, stored := e.object(t, "default", "strict")
	require.True(t, stored, "the policy stays, and waits for its target")
	e.waitReason(t, "default", "strict", v1.PhasePending, "TargetNotFound")
}

// scenario: addon-part-with-function — a revision that drops login, an App's part whose kind has an image, deletes it,
// and its Function with it: the Function that binds it is no user of it.
func TestScenarioAddonPartWithFunction(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	app := loginApp("default", "web-auth")
	e.apply(t, e.dev, app)
	e.waitApp(t, "default", "web-auth")
	status, detail := e.create(t, policiesPath, policy("default", "strict", "web-auth-login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	const name = v1.ObjectName("web-auth-login-rauthypasswordpolicy")
	e.waitFunction(t, "default", name, true)
	require.Equal(t, []v1.FunctionAddon{{Alias: "self", Object: "web-auth-login"}}, e.function(t, "default", name).Spec.Bindings.Addon)

	app = loginApp("default", "web-auth")
	app.Spec.Addons = nil
	app.Spec.ConfigMaps = []v1.ConfigMapEntry{{Name: "web-auth-settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"k": "v"}}}}
	e.apply(t, e.dev, app)
	require.Eventually(t, func() bool {
		_, stored := e.object(t, "default", "web-auth-login")
		_, running, _ := e.rt.state(ref("default", "web-auth-login"))
		return !stored && !running && e.function(t, "default", name) == nil
	}, 10*time.Second, 20*time.Millisecond, "the dropped part, its engine and its Function go")
}

// scenario: addon-function-refused — a function that names none or both of image and ref, an image with no digest or from
// an unlisted registry, or a ref with rules; an object that sets bindings.job and is no engineless one whose kind has an
// image: 400 naming the field.
func TestScenarioAddonFunctionRefused(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	const settingsField = "spec.runtimes[0].attributes.settings[0]"
	for name, c := range map[string]struct {
		edit func(st *v1.AddonSettings)
		want string
	}{
		"none": {func(st *v1.AddonSettings) { st.Function = v1.AddonFunction{} }, settingsField + ".function must name exactly one of image and ref"},
		"both": {func(st *v1.AddonSettings) { st.Function.Ref = "rauthy-settings" }, settingsField + ".function must name exactly one of image and ref"},
		"no digest": {func(st *v1.AddonSettings) { st.Function.Image = downRegistry + "/pyvvo/rauthy-settings:1" },
			settingsField + ".function.image"},
		"an unlisted registry": {func(st *v1.AddonSettings) { st.Function.Image = "quay.io/pyvvo/rauthy-settings" + addonDigest },
			settingsField + `.function.image "quay.io/pyvvo/rauthy-settings` + addonDigest + `" does not come from a registry that addons.registries lists`},
		"a ref with rules": {func(st *v1.AddonSettings) { st.Function = v1.AddonFunction{Ref: "rauthy-settings"} },
			settingsField + ".constraints.rules"},
		"a kind too long": {func(st *v1.AddonSettings) { st.Kind = "R" + strings.Repeat("p", 40) }, settingsField + ".function.image"},
	} {
		a := rauthySettingsAddon("rauthy")
		c.edit(&a.Spec.Runtimes[0].Attributes.Settings[0])
		status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, a)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", name, detail)
		require.Contains(t, detail, c.want, name)
	}
	stacks := infraStackAddon()
	stacks.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: "quay.io/pyvvo/stacks" + addonDigest}
	status, detail := e.send(t, http.MethodPost, addonsPath, addonAdminToken, stacks)
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "spec.runtimes[0].attributes.function.image")

	e.register(t, rauthySettingsAddon("rauthy"))
	e.register(t, infraStackAddon())
	job := v1.AddonObjectBindings{Job: []v1.FunctionJob{{Alias: "stack", Job: "bunny-edge"}}}
	engine := identityProvider("default")
	engine.Spec.Bindings.Job = job.Job
	setting := policy("default", "strict", "login", 8, 64)
	setting.Spec.Bindings = job
	stack := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	stack.Name, stack.Namespace, stack.ResourceGroup, stack.Spec.Bindings = "bunny-edge", "default", "rg1", job
	for path, o := range map[string]*v1.AddonObject{identityProvidersPath: engine, policiesPath: setting, stacksPath: stack} {
		status, detail := e.send(t, http.MethodPost, fmt.Sprintf(path, "default"), addonDevToken, o)
		require.Equal(t, http.StatusBadRequest, status, "%s: %s", path, detail)
		require.Contains(t, detail, "spec.bindings", path)
	}

	imaged := infraStackAddon()
	imaged.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: stacksImage}
	e.register(t, imaged)
	status, detail = e.send(t, http.MethodPost, fmt.Sprintf(stacksPath, "default"), addonDevToken, stack)
	require.True(t, created(status), "an engineless object whose kind has an image binds its Job: %d %s", status, detail)
	e.waitFunction(t, "default", "bunny-edge-infrastack", true)
	require.Equal(t, job.Job, e.function(t, "default", "bunny-edge-infrastack").Spec.Bindings.Job)
	stack.Spec.Config, stack.ResourceVersion = json.RawMessage(`{"job":"bunny-edge"}`), ""
	status, detail = e.replace(t, stacksPath, stack)
	require.Equal(t, http.StatusUnprocessableEntity, status, "its contract is its schema: %s", detail)
	require.Contains(t, detail, "job")
}
