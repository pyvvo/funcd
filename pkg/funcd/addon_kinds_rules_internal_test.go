package funcd

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// The rules of ADR-0358 beside its scenarios, on a running platform.

// slowImages answers as kindImages, except that its first read of image waits for release.
type slowImages struct {
	*kindImages
	image   string
	entered chan struct{}
	release chan struct{}
}

func (s *slowImages) Read(ctx context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error) {
	if image == s.image {
		select {
		case s.entered <- struct{}{}:
			select {
			case <-s.release:
			case <-ctx.Done():
			}
		default:
		}
	}
	return s.kindImages.Read(ctx, image)
}

// moved applies login with an edited config, and waits until it pins rev.
func (e *addonEnv) moved(t *testing.T, rev v1.ObjectName) {
	t.Helper()
	edited := identityProvider("default")
	edited.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v2","port":8080}`)
	e.apply(t, e.dev, edited)
	e.pin(t, "default", "login", rev, rauthyAddon("rauthy").Spec.Runtimes[0].Image)
}

// An admin replaces the Addon while its pass waits for a slow registry, and a developer creates login meanwhile: login
// pins the revision that the stored Addon names current, which nothing removes, and runs (ADR-0339 Decision 6).
func TestAddonReplacedWhileItsImageIsRead(t *testing.T) {
	t.Parallel()
	s := &slowImages{kindImages: newKindImages(), image: newerSettingsImage, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.set(newerSettingsImage, settingsContract(agedPolicyCall, clientCall))
	e := startAddons(t, WithAddonImages(s))
	e.register(t, rauthySettingsAddon("rauthy"))
	require.Equal(t, v1.ObjectName("rauthy-1"), storedAddon(t, e, "rauthy").Status.CurrentRevision)

	second := rauthySettingsAddon("rauthy")
	second.Spec.Runtimes[0].Attributes.Settings[0].Function.Image = newerSettingsImage
	e.apply(t, e.admin, second)
	select {
	case <-s.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the Addon's pass never read its new image")
	}
	e.apply(t, e.dev, identityProvider("default"))
	second.Spec.Runtimes[0].Attributes.Settings[0].Resync = v1.Duration(2 * time.Minute)
	e.apply(t, e.admin, second)
	close(s.release)
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.waitCurrent(t, "rauthy-3")
}

// Decision 5: a write's constraints are the revision's that its target pins.
func TestAddonConstraintsOfThePin(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.register(t, rauthySettingsAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.pin(t, "default", "login", "rauthy-1", rauthyAddon("rauthy").Spec.Runtimes[0].Image)
	status, detail := e.create(t, policiesPath, policy("default", "strict", "login", 8, 64))
	require.True(t, created(status), "%d %s", status, detail)
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Constraints.Count.PerTarget = 2
	e.register(t, a)
	e.waitCurrent(t, "rauthy-2")
	status, detail = e.create(t, policiesPath, policy("default", "second", "login", 8, 64))
	require.Equal(t, http.StatusBadRequest, status, "login pins one per target: %s", detail)
	e.moved(t, "rauthy-2")
	status, detail = e.create(t, policiesPath, policy("default", "second", "login", 8, 64))
	require.True(t, created(status), "login moved: %d %s", status, detail)
}

// Decision 5: while the revision that a target pins lacks the kind, the current one judges a write of it.
func TestAddonWriteOfAKindNotInThePin(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings = a.Spec.Runtimes[0].Attributes.Settings[:1]
	e.register(t, a)
	e.apply(t, e.dev, identityProvider("default"))
	e.pin(t, "default", "login", "rauthy-1", rauthyAddon("rauthy").Spec.Runtimes[0].Image)
	e.register(t, rauthySettingsAddon("rauthy"))
	e.waitCurrent(t, "rauthy-2")
	status, detail := e.create(t, clientsPath, settingsObject("RauthyClient", "default", "web", `{"provider":"login","colour":"blue"}`))
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
	status, detail = e.create(t, clientsPath, client("default", "web", "web"))
	require.True(t, created(status), "%d %s", status, detail)
	e.waitReason(t, "default", "web", v1.PhasePending, "FunctionNotFound")
}

// Decision 5: an App's part whose config its kind's contract does not take gets the App's 400, naming the entry and the
// property: a settings kind's part, and an engineless kind's.
func TestAddonPartOffItsContract(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	stacks := infraStackAddon()
	stacks.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: stacksImage}
	e.register(t, rauthySettingsAddon("rauthy"))
	e.register(t, stacks)
	for property, part := range map[string]v1.AppAddon{
		"maxAge": {Name: "web-auth-strict", AddonObjectSpec: v1.AddonObjectSpec{Kind: "RauthyPasswordPolicy",
			Config: agedPolicy("", "web-auth-login").Spec.Config}},
		"zone": {Name: "web-auth-edge", AddonObjectSpec: v1.AddonObjectSpec{Kind: "InfraStack", Config: json.RawMessage(`{"zone":"fra"}`)}},
	} {
		app := loginApp("default", "web-auth")
		app.Spec.Addons = append(app.Spec.Addons, part)
		_, err := e.dev.Apply(e.ctx, app)
		require.ErrorContains(t, err, "spec.addons[1]", property)
		require.ErrorContains(t, err, property)
	}
}

// Decisions 4 and 5: no contract judges a write of an engineless object that names its own Function, nor one of a kind
// whose contract does not compile.
func TestAddonWritesThatNoContractJudges(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	stacks := infraStackAddon()
	stacks.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: stacksImage}
	e.register(t, stacks)
	stack := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	stack.Name, stack.Namespace, stack.ResourceGroup, stack.Spec.Config = "edge", "default", "rg1", json.RawMessage(`{"zone":"fra"}`)
	status, detail := e.create(t, stacksPath, stack)
	require.Equal(t, http.StatusUnprocessableEntity, status, detail)
	stack.Spec.Functions = []v1.AddonObjectFunction{{Kind: "InfraStack", Ref: "my-reconciler"}}
	status, detail = e.create(t, stacksPath, stack)
	require.True(t, created(status), "%d %s", status, detail)

	e.images.set(newerSettingsImage, settingsContract(`{"properties":{"provider":{"type":"string","pattern":"[z-a]"},"lengthMin":{"type":"integer"},`+
		`"lengthMax":{"type":"integer"}},"required":["provider","lengthMin","lengthMax"]}`, clientCall))
	a := rauthySettingsAddon("rauthy")
	a.Spec.Runtimes[0].Attributes.Settings[0].Function.Image = newerSettingsImage
	e.apply(t, e.admin, a)
	e.waitAddon(t, "rauthy", "SettingsInvalid")
	status, detail = e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","colour":"blue"}`))
	require.True(t, created(status), "%d %s", status, detail)
}

// A contract that says nothing of a kind takes any config, and the kind's rules judge each write.
func TestAddonRulesOfASilentContract(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.images.set(newerSettingsImage, v1.WorkflowContract{Input: json.RawMessage(`{}`)})
	a := rauthySettingsAddon("rauthy")
	st := &a.Spec.Runtimes[0].Attributes.Settings[0]
	st.Function.Image, st.Constraints = newerSettingsImage, v1.AddonConstraints{Rules: []v1.AddonRule{{
		Rule: "${{ others.length < 1 }}", Message: "one policy per namespace"}}}
	e.register(t, a)
	status, detail := e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "strict", `{"provider":"login","colour":"blue"}`))
	require.True(t, created(status), "%d %s", status, detail)
	status, detail = e.create(t, policiesPath, settingsObject("RauthyPasswordPolicy", "default", "second", `{"provider":"login"}`))
	require.Equal(t, http.StatusBadRequest, status, detail)
	require.Contains(t, detail, "one policy per namespace")
}

// Decision 4: an object that pins an older revision, whose image is read late, goes on with no other event.
func TestAddonOlderRevisionReadLate(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	a := infraStackAddon()
	a.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: newerSettingsImage} // its registry is down
	e.apply(t, e.admin, a)
	e.waitAddon(t, "infrastack", "ArtifactNotReady")
	stack := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "InfraStack"}}
	stack.Name, stack.Namespace, stack.ResourceGroup, stack.Spec.Config = "site", "default", "rg1", json.RawMessage(`{"region":"fra"}`)
	e.apply(t, e.dev, stack)
	e.waitReason(t, "default", "site", v1.PhasePending, "ArtifactNotReady")
	b := infraStackAddon()
	b.Spec.Runtimes[0].Attributes.Function = &v1.AddonFunction{Image: stacksImage}
	e.register(t, b)
	require.True(t, poll.Never(func() bool { return e.function(t, "default", "site-infrastack") != nil }, 200*time.Millisecond, 20*time.Millisecond),
		"site pins the revision whose image is not read")
	e.images.set(newerSettingsImage, v1.WorkflowContract{Input: json.RawMessage(kindCall("InfraStack", `{"properties":{"region":{"type":"string"}}}`))})
	e.waitFunction(t, "default", "site-infrastack", true)
	require.Equal(t, newerSettingsImage, e.function(t, "default", "site-infrastack").Spec.Image)
}
