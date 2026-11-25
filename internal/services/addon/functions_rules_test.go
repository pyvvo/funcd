package addon_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// The rules of ADR-0358 beside its scenarios.

// revision is the AddonRevision number of rauthy, nil when there is none.
func (g *settingsRig) revision(number int64) *v1.AddonRevision {
	g.t.Helper()
	obj, err := g.st.Get(g.ctx, v1.KindAddonRevision.GVK(), "", v1.AddonRevisionName("rauthy", number))
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(g.t, err)
	return obj.(*v1.AddonRevision)
}

// writeAddon edits the Addon's settings kinds as an admin does, and runs no pass.
func (g *settingsRig) writeAddon(edit func(at *v1.AddonAttributes)) {
	g.t.Helper()
	a := g.addon()
	edit(a.Spec.Runtimes[0].Attributes)
	_, err := g.st.Update(g.ctx, a)
	require.NoError(g.t, err)
}

// moveLogin edits login, so that its next pass pins the current revision.
func (g *settingsRig) moveLogin() {
	g.t.Helper()
	g.editLogin(func(s *v1.AddonObjectSpec) { s.Resources.Memory += "6" })
}

func settingsReq(name v1.ObjectName) controller.Request {
	return controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: name}
}

// An admin's write of the Addon while its pass reads an image ends that pass: the stored Addon still names the former
// revision current, so nothing removes it, and an object created meanwhile runs it (ADR-0339 Decision 6).
// oracle: docs/design/addon/revisions.fizz#PinsExist
func TestAddonWrittenDuringAReadKeepsItsCurrentRevision(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	g.writeAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.images.hook = func() {
		g.writeAddon(func(at *v1.AddonAttributes) { at.Settings[0].Resync = v1.Duration(2 * time.Minute) })
	}
	g.addonPass()
	require.Equal(t, v1.AddonRevisionName("rauthy", 1), g.addon().Status.CurrentRevision, "the pass stored nothing")
	require.NotNil(t, g.revision(1), "what the stored Addon names current is there")
	g.pass("login")
	require.Equal(t, v1.PhaseReady, g.object("login").Status.Phase)
	require.Equal(t, v1.AddonRevisionName("rauthy", 1), g.object("login").Status.AddonRevision)

	g.addonPass()
	require.Equal(t, v1.AddonRevisionName("rauthy", 3), g.addon().Status.CurrentRevision, "the write's event brings the next pass")
	require.NotNil(t, g.revision(1), "login pins it")
	require.Nil(t, g.revision(2), "nothing pins it")
}

// A revision written while its images are read is read again: no event of it brings the Addon's pass.
func TestAddonRevisionWrittenDuringAReadIsReadAgain(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	g.writeAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.images.hook = func() {
		rev := g.revision(2)
		rev.Tags = v1.Tags{"note": "written by another"}
		_, err := g.st.Update(g.ctx, rev)
		require.NoError(t, err)
	}
	_, err := g.r.Revisions().Reconcile(g.ctx, controller.Request{GVK: v1.KindAddon.GVK(), Name: "rauthy"})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "the controller tries it again: %v", err)
	g.addonPass()
	require.NotNil(t, g.revision(2).ContractOf("RauthyPasswordPolicy"))
	require.Equal(t, v1.AddonRevisionName("rauthy", 2), g.addon().Status.CurrentRevision)
}

// Decision 4: a revision's images are read before it is current, so a deploy that waits for it finds its contracts.
func TestAddonImagesAreReadBeforeTheRevisionIsCurrent(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	var during v1.ObjectName
	g.images.hook = func() { during = g.addon().Status.CurrentRevision }
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	require.Equal(t, v1.AddonRevisionName("rauthy", 1), during)
	require.Equal(t, v1.AddonRevisionName("rauthy", 2), g.addon().Status.CurrentRevision)
}

// Decision 4: an image that cannot be read keeps no other from being read, and the Addon is Pending until each is.
func TestAddonOneUnreadImageOfTwo(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	require.Equal(t, v1.PhaseReady, g.addon().Status.Phase)
	g.editAddon(func(at *v1.AddonAttributes) {
		at.Settings = append(at.Settings, v1.AddonSettings{Kind: "RauthyClient", Target: "spec.config.provider", Function: v1.AddonFunction{Image: settings2}})
	})
	require.NotNil(t, g.revision(2).ContractOf("RauthyPasswordPolicy"))
	require.Nil(t, g.revision(2).ContractOf("RauthyClient"))
	require.Equal(t, v1.PhasePending, g.addon().Status.Phase)
	c, _ := g.addon().Status.Conditions.Get("Ready")
	require.Equal(t, "ArtifactNotReady", c.Reason)
	require.Contains(t, c.Message, "RauthyClient")
}

// Decision 5: a Function that funcd creates is called once a revision of its spec is stamped, or while one serves, so
// that the call meets the contract of the code that answers it: not as it is created, nor while it sleeps and its image
// has just changed.
func TestAddonFunctionIsCalledOnceItsRevisionIsStamped(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.pass("login")
	require.NotNil(t, g.created(policyFunction))
	g.noCall("no revision of it is stamped")
	stampFunctions(t, g.st)
	require.ElementsMatch(t, []controller.Request{settingsReq("login"), settingsReq("strict")},
		g.r.MapFunction(g.ctx, g.created(policyFunction)), "its stamp brings the next pass")
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "start", g.lastCall().call.Cause)

	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties+`,"maxAge":{"type":"integer"}`))
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.moveLogin()
	g.advance(time.Hour)
	g.pass("login")
	require.Equal(t, settings2, g.created(policyFunction).Spec.Image)
	g.noCall("it sleeps, and a call would wake the new image")
	stampFunctions(t, g.st)
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "change", g.lastCall().call.Cause)

	fn := g.created(policyFunction)
	fn.Status.ServingRevision = fn.Status.CurrentRevision
	_, err := g.st.Update(g.ctx, fn)
	require.NoError(t, err)
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings1 })
	g.moveLogin()
	g.advance(time.Hour)
	g.pass("login")
	require.Equal(t, settings1, g.created(policyFunction).Spec.Image)
	g.callEnds("login")
	require.Equal(t, "resync", g.lastCall().call.Cause, "the revision that serves answers it")
}

// Decision 5: so is an engineless object's Function.
func TestEnginelessFunctionIsCalledOnceItsRevisionIsStamped(t *testing.T) {
	t.Parallel()
	g := newImageStacks(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.pass("site")
	obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "site-infrastack")
	require.NoError(t, err)
	require.True(t, poll.Never(func() bool { return len(g.caller.got()) > 0 }, 50*time.Millisecond, 5*time.Millisecond),
		"no revision of it is stamped")
	g.requireReason("site", "Progressing")
	stampFunctions(t, g.st)
	require.Equal(t, []controller.Request{settingsReq("site")}, g.r.MapEnginelessFunction(g.ctx, obj), "its stamp brings the next pass")
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, "start", g.lastCall().call.Cause)
}

// A ref's Function is called as it is: the call of one that has no revision says why it cannot answer.
func TestRefWithNoRevisionIsCalled(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.revise("rauthy-settings", "")
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")

	s := newEnginelessRig(t)
	obj, err := s.st.Get(s.ctx, v1.KindFunction.GVK(), "default", "infrastack-reconciler")
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	fn.Status.CurrentRevision = ""
	_, err = s.st.Update(s.ctx, fn)
	require.NoError(t, err)
	s.stack("site", v1.DeletionRetain, "")
	s.marked("site")
	s.pass("site")
	s.callEnds("site")
}

// Decision 2: a user's Function of the name that a created one would have, with no object that needs one, is neither
// called nor removed.
func TestAddonUserFunctionWithNoObject(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.function(policyFunction, map[string]v1.ObjectName{"idp": "login"})
	for range 3 {
		g.pass("login")
	}
	require.NotNil(t, g.created(policyFunction), "left as it is")
	g.noCall("it is another's")
}

// Decision 1: a ref's Function is the user's, so it stays once the empty list was applied, and gets its resyncs.
func TestSettingsRefStaysAfterItsEmptyList(t *testing.T) {
	t.Parallel()
	g := newSettingsRig(t, time.Second)
	g.policy("strict", "login", 16)
	g.pass("login")
	g.callEnds("login")
	g.remove(v1.KindAddonObject, "strict")
	g.pass("login")
	g.callEnds("login")
	for range 3 {
		g.pass("login")
	}
	require.NotNil(t, g.created("rauthy-settings"))
	g.advance(time.Minute)
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "resync", g.lastCall().call.Cause)
}

// Decision 5: the resync is the pinned revision's, not the Addon's as it is now.
func TestAddonResyncOfThePin(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Resync = v1.Duration(time.Hour) })
	g.advance(time.Minute)
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "resync", g.lastCall().call.Cause, "login pins the resync of 1m")
	g.moveLogin()
	g.pass("login")
	g.advance(time.Minute)
	g.pass("login")
	g.noCall("login moved to the resync of 1h")
}

// Decision 2: funcd's Function goes once the pinned revision names another, and stays until then.
func TestAddonFunctionGoesWithThePin(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.function("rauthy-settings", map[string]v1.ObjectName{"idp": "login"})
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function = v1.AddonFunction{Ref: "rauthy-settings"} })
	g.pass("login")
	require.NotNil(t, g.created(policyFunction), "login pins the revision that names the image")
	require.ElementsMatch(t, []controller.Request{settingsReq("login"), settingsReq("strict")},
		g.r.MapFunction(g.ctx, g.created(policyFunction)), "its change is login's, as the pin names it")
	g.moveLogin()
	g.pass("login")
	require.Nil(t, g.created(policyFunction), "the pin names a ref")
	require.NotNil(t, g.created("rauthy-settings"))
	g.callEnds("login")
	require.Equal(t, v1.ObjectName("rauthy-settings"), g.lastCall().fn)
}

// Decision 5: a kind that the Addon no longer declares has no Function and no call, whatever the pinned revision says.
func TestAddonKindDropped(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings = nil })
	g.advance(time.Hour)
	g.pass("login")
	require.Nil(t, g.created(policyFunction))
	g.noCall("login pins a revision with the kind, but the Addon dropped it")
}

// Decision 4: while the image of the revision that login moved to is not read, its Function stays as it is and is not
// called.
func TestAddonFunctionWaitsForTheNewImage(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.moveLogin()
	g.advance(time.Hour)
	g.pass("login")
	require.Equal(t, v1.AddonRevisionName("rauthy", 2), g.object("login").Status.AddonRevision)
	require.Equal(t, settings1, g.created(policyFunction).Spec.Image)
	g.noCall("the new revision's contract is not read")
	g.pass("strict")
	g.requireReason("strict", "ArtifactNotReady")
}

// A settings object waits for its Addon's first revision, and is Progressing again once what it waited for is there.
func TestSettingsObjectWaitsForItsImage(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.editAddon(func(at *v1.AddonAttributes) { at.Settings[0].Function.Image = settings2 })
	g.pass("login")
	g.policy("strict", "login", 16)
	g.pass("strict")
	g.requireReason("strict", "ArtifactNotReady")
	g.images.set(settings2, callContract("RauthyPasswordPolicy", policyProperties))
	g.addonPass()
	g.pass("strict")
	g.requireReason("strict", "Progressing")

	g.function(policyFunction, nil)
	g.pass("strict")
	g.requireReason("strict", "FunctionNotOwned")
	g.remove(v1.KindFunction, policyFunction)
	g.pass("strict")
	g.requireReason("strict", "Progressing")

	a := g.addon()
	a.Status.CurrentRevision = ""
	_, err := g.st.Update(g.ctx, a)
	require.NoError(t, err)
	g.policy("early", "nobody", 16)
	g.pass("early")
	g.requireReason("early", "Progressing")
	c, _ := g.ready("early")
	require.Equal(t, "Addon rauthy has no revision yet", c.Message)
}

// Decision 2: funcd's Function goes once an engineless object names its own, which gets its list.
func TestEnginelessOwnFunctionReplacesTheCreatedOne(t *testing.T) {
	t.Parallel()
	g := newImageStacks(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.converge("site")
	g.callEnds("site")
	g.function("my-reconciler")
	o := g.object("site")
	o.Spec.Functions = []v1.AddonObjectFunction{{Kind: "InfraStack", Ref: "my-reconciler"}}
	_, err := g.st.Update(g.ctx, o)
	require.NoError(t, err)
	g.pass("site")
	g.callEnds("site")
	require.Equal(t, v1.ObjectName("my-reconciler"), g.lastCall().fn)
	_, err = g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "site-infrastack")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "funcd's own goes: %v", err)
}

// A group's list is sent again once a new revision takes the calls, as a target's is.
func TestEnginelessCallFollowsTheRevisionThatAnswers(t *testing.T) {
	t.Parallel()
	g := newEnginelessRig(t)
	g.stack("site", v1.DeletionRetain, "")
	g.marked("site")
	g.pass("site")
	g.callEnds("site")
	serves := func(serving, current string) {
		t.Helper()
		obj, err := g.st.Get(g.ctx, v1.KindFunction.GVK(), "default", "infrastack-reconciler")
		require.NoError(t, err)
		fn := obj.(*v1.Function)
		fn.Status.ServingRevision, fn.Status.CurrentRevision = serving, current
		_, err = g.st.Update(g.ctx, fn)
		require.NoError(t, err)
		g.pass("site")
	}
	serves("infrastack-reconciler-1", "infrastack-reconciler-2")
	require.True(t, poll.Never(func() bool { return len(g.caller.got()) > 1 }, 50*time.Millisecond, 5*time.Millisecond), "revision 1 would answer")
	serves("infrastack-reconciler-2", "infrastack-reconciler-2")
	g.callEnds("site")
	require.Equal(t, "change", g.lastCall().call.Cause)
}

// removed removes the stack name and its Function, as the owner collector does, and runs the pass of its deletion's event.
func (g *enginelessRig) removed(name v1.ObjectName) {
	g.t.Helper()
	require.False(g.t, g.del(name))
	require.NoError(g.t, g.st.Delete(g.ctx, v1.KindFunction.GVK(), "default", name+"-infrastack", ""))
	g.pass(name)
}

// What this process knows of a removed engineless object's calls goes with it, and of no other's: one created again under
// its name starts anew, while another of its namespace and one of its name in another namespace are still known.
func TestEnginelessCallsAreForgottenWithTheirObject(t *testing.T) {
	t.Parallel()
	g := newImageStacks(t)
	other := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	other.Name, other.Namespace, other.ResourceGroup, other.Spec.Kind = "site", "team", "rg1", "InfraStack"
	_, err := g.st.Create(g.ctx, other)
	require.NoError(t, err)
	team := func() {
		t.Helper()
		_, err := g.r.Reconcile(g.ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "team", Name: "site"})
		require.NoError(t, err)
	}
	g.stack("bunny-edge", v1.DeletionRetain, "")
	g.marked("bunny-edge")
	g.converge("bunny-edge")
	g.callEnds("bunny-edge")
	team()
	team()
	stampFunctions(t, g.st)
	team()
	g.callEnds("site")
	for range 2 {
		g.stack("site", v1.DeletionRetain, "")
		g.marked("site")
		g.converge("site")
		g.callEnds("site")
		require.Equal(t, "start", g.lastCall().call.Cause)
		g.removed("site")
	}
	g.advance(time.Minute)
	g.pass("bunny-edge")
	g.callEnds("bunny-edge")
	require.Equal(t, "resync", g.lastCall().call.Cause, "another stack's calls are known")
	team()
	g.callEnds("site")
	require.Equal(t, "resync", g.lastCall().call.Cause, "so are those of another namespace's stack of that name")
}

// One call at most runs for an object's name: one created again while its call runs waits for that call's end, and then
// starts anew (ADR-0341 Decision 1, ADR-0345 Decision 2).
func TestAnObjectCreatedAgainWaitsForItsCall(t *testing.T) {
	t.Parallel()
	s := newImageStacks(t)
	release := make(chan struct{})
	s.caller.set(func(_ context.Context, c addon.ObjectsCall) (addon.SettingsReply, error) {
		<-release
		return answerAll(c, true, ""), nil
	})
	s.stack("site", v1.DeletionRetain, "")
	s.marked("site")
	s.converge("site")
	require.Eventually(t, func() bool { return len(s.caller.got()) == 1 }, 5*time.Second, 5*time.Millisecond)
	s.removed("site")
	s.stack("site", v1.DeletionRetain, "")
	s.marked("site")
	s.converge("site")
	require.True(t, poll.Never(func() bool { return len(s.caller.got()) > 1 }, 50*time.Millisecond, 5*time.Millisecond), "the former one's call runs")
	s.caller.set(nil)
	close(release)
	s.callEnds("site")
	s.pass("site")
	s.callEnds("site")
	require.Equal(t, "start", s.lastCall().call.Cause)

	g := newSettingsRig(t, 5*time.Second)
	release = make(chan struct{})
	g.caller.set(func(_ context.Context, c addon.SettingsCall) (addon.SettingsReply, error) {
		<-release
		return okAll(c), nil
	})
	g.policy("strict", "login", 16)
	g.pass("login")
	require.Eventually(t, func() bool { return len(g.caller.got()) == 1 }, 5*time.Second, 5*time.Millisecond)
	g.remove(v1.KindAddonObject, "login")
	g.pass("login")
	g.target("login")
	g.pass("login")
	g.noCall("the former login's call runs")
	g.caller.set(nil)
	close(release)
	g.callEnds("login")
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "start", g.lastCall().call.Cause, "the new login holds nothing yet")
}

// A kind whose created Function waits for its stamp keeps no other kind of the target waiting.
func TestOneWaitingKindOfTwo(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.images.set(settings2, callContract("RauthyClient", policyProperties))
	g.editAddon(func(at *v1.AddonAttributes) {
		at.Settings = append(at.Settings, v1.AddonSettings{Kind: "RauthyClient", Target: "spec.config.provider", Function: v1.AddonFunction{Image: settings2}})
	})
	g.policy("strict", "login", 16)
	web := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
	web.Name, web.Namespace, web.ResourceGroup, web.Spec.Kind = "web", "default", "rg1", "RauthyClient"
	web.Spec.Config = json.RawMessage(`{"provider":"login"}`)
	_, err := g.st.Create(g.ctx, web)
	require.NoError(t, err)
	g.pass("login")
	g.revise("login-rauthyclient", "login-rauthyclient-1")
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, v1.ObjectName("login-rauthyclient"), g.lastCall().fn)
	g.noCall("the policies' Function is not stamped")
}

// A contract says a kind by enum or by const, in a union that kind discriminates alone.
func TestKindSchemaOfAUnion(t *testing.T) {
	t.Parallel()
	member := func(kind, config string) string {
		return `{"type":"object","properties":{"kind":{"const":"` + kind + `"},"objects":{"type":"array","items":{"type":"object",` +
			`"properties":{"config":` + config + `}}}}}`
	}
	union := func(discriminator string) v1.WorkflowContract {
		return v1.WorkflowContract{Input: json.RawMessage(`{"oneOf":[` + member("RauthyPasswordPolicy", `{"type":"integer"}`) + `,` +
			member("RauthyClient", `{"type":"string"}`) + `],"discriminator":{"propertyName":"` + discriminator + `"}}`)}
	}
	require.JSONEq(t, `{"type":"string"}`, string(addon.KindSchema(union("kind"), "RauthyClient")))
	require.JSONEq(t, `{"type":"integer"}`, string(addon.KindSchema(union("kind"), "RauthyPasswordPolicy")))
	require.Nil(t, addon.KindSchema(union("cause"), "RauthyClient"))
}

// Decision 4: a contract must type a kind's target as a string that it requires at each level.
func TestCheckSettingsTarget(t *testing.T) {
	t.Parallel()
	record := func(properties, required string) string {
		return `{"type":"object","additionalProperties":false,"properties":{` + properties + `},"required":[` + required + `]}`
	}
	named := record(`"name":{"type":"string"}`, `"name"`)
	for name, c := range map[string]struct {
		target, schema string
		fits           bool
	}{
		"required":            {"spec.config.provider", record(`"provider":{"type":"string"}`, `"provider"`), true},
		"optional":            {"spec.config.provider", record(`"provider":{"type":"string"}`, ``), false},
		"no string":           {"spec.config.provider", record(`"provider":{"type":"integer"}`, `"provider"`), false},
		"absent":              {"spec.config.provider", named, false},
		"an optional level":   {"spec.config.idp.name", record(`"idp":`+named, ``), false},
		"required each level": {"spec.config.idp.name", record(`"idp":`+named, `"idp"`), true},
		"nothing said":        {"spec.config.provider", `{}`, true},
	} {
		err := addon.CheckSettings(&v1.AddonSettings{Kind: "RauthyPasswordPolicy", Target: c.target}, json.RawMessage(c.schema))
		if c.fits {
			require.NoError(t, err, name)
			continue
		}
		require.ErrorContains(t, err, "target "+c.target, name)
	}
}

// Decision 5: a contract that says nothing of a kind judges no config, and its rules judge each write, as they check
// when the Addon is registered; a contract that is not read lets no rule judge.
func TestAdmitSettingsWithASilentContract(t *testing.T) {
	t.Parallel()
	st := &v1.AddonSettings{Kind: "RauthyPasswordPolicy", Target: "spec.config.provider",
		Constraints: v1.AddonConstraints{Rules: []v1.AddonRule{{Rule: "${{ others.length < 1 }}", Message: "one policy per namespace"}}}}
	o := &v1.AddonObject{}
	o.Name, o.Spec.Config = "strict", json.RawMessage(`{"provider":"login"}`)
	others := []*v1.AddonObject{{}}
	silent := json.RawMessage(`{}`)
	require.NoError(t, addon.CheckSettings(st, silent))
	require.NoError(t, addon.AdmitSettings(st, silent, o, nil, nil))
	require.ErrorContains(t, addon.AdmitSettings(st, silent, o, nil, others), "one policy per namespace")
	require.NoError(t, addon.AdmitSettings(st, nil, o, nil, others), "a contract that is not read")

	st.Constraints.Rules[0].Rule = "${{ object.spec.config.lengthMin > 1 }}"
	require.ErrorContains(t, addon.CheckSettings(st, silent), "lengthMin")
	require.ErrorContains(t, addon.AdmitSettings(st, silent, o, nil, nil), "lengthMin", "a rule that does not fit refuses the write")
}

// A failed call of the empty list keeps the Function, and its retry lets it go.
func TestAddonFunctionGoesAfterARetry(t *testing.T) {
	t.Parallel()
	g := newImageRig(t)
	g.policy("strict", "login", 16)
	g.applied("strict")
	g.remove(v1.KindAddonObject, "strict")
	g.caller.set(func(context.Context, addon.SettingsCall) (addon.SettingsReply, error) {
		return addon.SettingsReply{}, fault.Unavailablef("test", "rauthy is down")
	})
	g.pass("login")
	g.callEnds("login")
	g.pass("login")
	require.NotNil(t, g.created(policyFunction))
	g.caller.set(nil)
	g.advance(time.Second)
	g.pass("login")
	g.callEnds("login")
	require.Equal(t, "retry", g.lastCall().call.Cause)
	g.pass("login")
	require.Nil(t, g.created(policyFunction))
}
