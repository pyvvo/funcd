//go:build !e2e

package funcd_test

import (
	"encoding/json"
	"net/http"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The answers of the add-ons' operations (ADR-0329, ADR-0332): an Addon's and an add-on object's, one row per case of
// each, through the platform's own servers.

const (
	addonsPath     = "/apis/funcd.io/v1alpha1/addons"
	identityPath   = "/apis/funcd.io/v1alpha1/namespaces/default/identityproviders"
	unregistered   = "/apis/funcd.io/v1alpha1/namespaces/default/nothings"
	addonImageHash = "@sha256:7686fef5edcb27399e23f8386fecb03b13f909966d2c3c4708095e2c31ccfe2d"
)

// addonFixture is the Addon name, which registers kind: an engine pinned by digest at /auth/v1, with a private path
// and an issuer in its config.
func addonFixture(name v1.ObjectName, kind string) *v1.Addon {
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon}}
	a.Name = name
	a.Spec = v1.AddonSpec{Kind: kind, Image: "ghcr.io/pyvvo/funcd-rauthy" + addonImageHash, Port: 8080,
		BasePath: "/auth/v1", PrivatePaths: []string{"/auth/v1/clients_dyn"},
		Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"},
		Config:    json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"issuer":{"type":"string"}},"required":["issuer"]}`),
		Env:       map[string]string{"PUB_URL": "${{ config.issuer }}"}}
	return a
}

// registerAddons applies each Addon with the admin's token.
func registerAddons(t *testing.T, e *answerEnv, addons ...*v1.Addon) {
	t.Helper()
	for _, a := range addons {
		ask{what: "the Addon " + string(a.Name), method: http.MethodPost, path: addonsPath, token: answersAdmin,
			body: jsonOf(t, a), status: http.StatusOK}.run(t, e.c, e.api)
	}
}

// identityFixture is the IdentityProvider name of default, an object of the kind addonFixture's rauthy registers.
func identityFixture(name v1.ObjectName) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: "IdentityProvider"}}
	o.Name, o.Namespace, o.ResourceGroup = name, "default", "rg1"
	o.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v1"}`)
	return o
}

// The answers of the Addon's operations, one row per case of each.
func TestAddonAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, funcd.WithAddonRegistries("ghcr.io"))
	registerAddons(t, e, addonFixture("rauthy", "IdentityProvider"))
	broken := addonFixture("rauthy", "IdentityProvider")
	broken.Spec.PrivatePaths = []string{"/admin"}
	shared := clusterFixture{kind: "Addon", kinds: "Addons", path: "addons", name: "rauthy",
		json: jsonOf(t, addonFixture("rauthy", "IdentityProvider")), broken: jsonOf(t, broken),
		detail: `spec.privatePaths[0] "/admin" is not under spec.basePath "/auth/v1"`}.sharedRows(e)
	// One kind is one Addon's, so the Addons that the rows create register kinds of their own.
	for i, r := range shared {
		switch {
		case r.op == "createAddon" && r.when == "":
			shared[i].ask.body = jsonOf(t, addonFixture("rauthy-new", "NewService"))
		case r.op == "deleteAddon" && r.when == "":
			shared[i].setup = func(t *testing.T, e *answerEnv) {
				registerAddons(t, e, addonFixture("rauthy-doomed", "DoomedService"))
			}
		}
	}
	// edited is rauthy with edit applied, and other is an Addon of its own kind with edit applied.
	edited := func(edit func(*v1.AddonSpec)) string {
		a := addonFixture("rauthy", "IdentityProvider")
		edit(&a.Spec)
		return jsonOf(t, a)
	}
	other := func(name v1.ObjectName, kind string, edit func(*v1.AddonSpec)) string {
		a := addonFixture(name, kind)
		edit(&a.Spec)
		return jsonOf(t, a)
	}
	quay := func(s *v1.AddonSpec) { s.Image = "quay.io/pyvvo/funcd-rauthy" + addonImageHash }
	untyped := func(s *v1.AddonSpec) { s.Env = map[string]string{"PUB_URL": "${{ config.nope }}"} }
	row := func(op, when string, a ask) answerRow {
		a.token = answersAdmin
		return answerRow{op: op, when: when, on: e, ask: a}
	}
	runMatrix(t, append(shared,
		row("createAddon", "is a kind of funcd's", ask{what: "a kind of funcd's", method: http.MethodPost,
			path: addonsPath, body: other("lake", "Lake", func(*v1.AddonSpec) {}), detail: "is a kind of funcd's own"}),
		row("createAddon", "The registry of spec.image", ask{what: "an image from an unlisted registry",
			method: http.MethodPost, path: addonsPath, body: other("quay", "QuayService", quay),
			detail: "does not come from a registry that addons.registries lists"}),
		row("createAddon", "spec.config is not a JSON Schema", ask{what: "an env value the schema does not type",
			method: http.MethodPost, path: addonsPath, body: other("untyped", "UntypedService", untyped), detail: "spec.env"}),
		row("createAddon", "Another Addon registers", ask{what: "a kind another Addon registers", method: http.MethodPost,
			path: addonsPath, body: other("twin", "IdentityProvider", func(*v1.AddonSpec) {}),
			detail: `Addon "rauthy" registers kind "IdentityProvider"`}),

		row("replaceAddon", "is a kind of funcd's", ask{what: "a changed kind", method: http.MethodPut,
			path: addonsPath + "/rauthy", body: edited(func(s *v1.AddonSpec) { s.Kind = "OtherService" }),
			detail: `spec.kind cannot change from "IdentityProvider" to "OtherService"`}),
		row("replaceAddon", "The registry of spec.image", ask{what: "an image from an unlisted registry",
			method: http.MethodPut, path: addonsPath + "/rauthy", body: edited(quay),
			detail: "does not come from a registry that addons.registries lists"}),
		row("replaceAddon", "spec.config is not a JSON Schema", ask{what: "an env value the schema does not type",
			method: http.MethodPut, path: addonsPath + "/rauthy", body: edited(untyped), detail: "spec.env"}),
	))
}

// The answers of the operations of an add-on's objects, one row per case of each, at the plural of rauthy's kind.
func TestAddonObjectAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, funcd.WithAddonRegistries("ghcr.io"))
	capped := newAnswerEnv(t, funcd.WithAddonRegistries("ghcr.io"), funcd.WithAddonObjectQuota(1))
	for _, env := range []*answerEnv{e, capped} {
		registerAddons(t, env, addonFixture("rauthy", "IdentityProvider"), addonFixture("echo", "EchoService"))
	}
	create(t, e.c, e.api, identityFixture("login"))
	create(t, capped.c, capped.api, identityFixture("first"))
	broken := identityFixture("login")
	broken.Spec.Ingress = &v1.Ingress{Host: "login.example.com", Path: "/auth"}
	shared := kindFixture{kind: "AddonObject", kinds: "AddonObjects", path: "identityproviders", name: "login",
		json: jsonOf(t, identityFixture("login")), broken: jsonOf(t, broken),
		detail: "spec.ingress.path is not allowed"}.sharedRows(e)
	// as is login, or the object name, with edit applied.
	as := func(name v1.ObjectName, edit func(*v1.AddonObject)) string {
		o := identityFixture(name)
		edit(o)
		return jsonOf(t, o)
	}
	echo := func(o *v1.AddonObject) { o.Kind = "EchoService" }
	nothing := func(o *v1.AddonObject) { o.Kind = "Nothing" }
	typed := func(o *v1.AddonObject) { o.Spec.Config = json.RawMessage(`{"issuer":5}`) }
	row := func(op, when string, on *answerEnv, a ask) answerRow {
		a.token = answersDev
		return answerRow{op: op, when: when, on: on, ask: a}
	}
	runMatrix(t, append(shared,
		row("listAddonObjects", "No Addon registers the kind, and", e, ask{what: "a plural no Addon registers",
			method: http.MethodGet, path: unregistered, detail: `of the plural "nothings"`}),

		row("createAddonObject", "No Addon registers the kind whose plural", e, ask{what: "a plural no Addon registers",
			method: http.MethodPost, path: unregistered, body: as("lost", nothing), detail: `of the plural "nothings"`}),
		row("createAddonObject", "The body's kind is not", e, ask{what: "another add-on's kind", method: http.MethodPost,
			path: identityPath, body: as("echo", echo), detail: `body kind "EchoService" is not "IdentityProvider"`}),
		row("createAddonObject", "addons.maxObjectsPerNamespace", capped, ask{what: "an object past the cap",
			method: http.MethodPost, path: identityPath, body: as("second", func(*v1.AddonObject) {}),
			detail: "already holds the maximum 1 add-on objects"}),
		row("createAddonObject", "spec.config breaks the schema", e, ask{what: "a config that breaks the Addon's schema",
			method: http.MethodPost, path: identityPath, body: as("typed", typed), detail: "body.spec.config.issuer"}),

		row("replaceAddonObject", "No Addon registers the kind whose plural", e, ask{what: "a plural no Addon registers",
			method: http.MethodPut, path: unregistered + "/login", body: as("login", nothing),
			detail: `of the plural "nothings"`}),
		row("replaceAddonObject", "The body's kind is not", e, ask{what: "another add-on's kind", method: http.MethodPut,
			path: identityPath + "/login", body: as("login", echo),
			detail: `body kind "EchoService" is not "IdentityProvider"`}),
		row("replaceAddonObject", "spec.config breaks the schema", e, ask{what: "a config that breaks the Addon's schema",
			method: http.MethodPut, path: identityPath + "/login", body: as("login", typed),
			detail: "body.spec.config.issuer"}),
	))
}
