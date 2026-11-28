//go:build !e2e

package funcd_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// clusterFixture is an object of a cluster-scoped kind, for the rows of the cases its kind's operations share: json is
// its body, and broken that body against a rule the schema cannot hold, which detail names.
type clusterFixture struct {
	kind, kinds, path    string // such as Namespace, Namespaces and namespaces
	name                 v1.ObjectName
	json, broken, detail string
	noSpec               bool // only a create of broken breaks a rule
}

// sharedRows are the rows of the cases that each verb of a described cluster-scoped kind shares with every other kind's
// (ADR-0329 Decision 1), on e. Only an admin acts on a cluster-scoped kind, so each row sends the admin's token, but
// those of a call with no credential and of a developer's, whose role refuses it.
func (k clusterFixture) sharedRows(e *answerEnv) []answerRow {
	coll := "/apis/funcd.io/v1alpha1/" + k.path
	item, named, s := coll+"/"+string(k.name), `"name":"`+string(k.name)+`"`, k.json
	renamed := func(body string, name v1.ObjectName) string { return kindFixture{name: k.name}.renamed(body, name) }
	list, get, create, replace, remove := "list"+k.kinds, "get"+k.kind, "create"+k.kind, "replace"+k.kind, "delete"+k.kind
	plain, unknown := []string{"Content-Type", "text/plain"}, `{"bogus":1,`+strings.TrimPrefix(s, "{")
	weak, stale := []string{"If-Match", `W/"1"`}, []string{"If-Match", `"1"`}
	doomed := k.name + "-doomed"
	row := func(op, when string, a ask) answerRow {
		a.token = answersAdmin
		return answerRow{op: op, when: when, on: e, ask: a}
	}
	// refused are an operation's 401 and 403: no credential, and a developer's.
	refused := func(op, method, path, body string) []answerRow {
		return []answerRow{
			{op: op, when: "carries no bearer token", on: e, ask: ask{what: "no credential", method: method, path: path,
				body: body, detail: "missing bearer token"}},
			{op: op, when: "does not allow it", on: e, ask: ask{what: "a developer", method: method, path: path,
				token: answersDev, body: body, detail: "may not act on cluster-scoped kind"}},
		}
	}
	// bodies are a write's answers to a body that huma refuses.
	bodies := func(op, method, path string) []answerRow {
		return []answerRow{
			row(op, "The body is missing or is not JSON", ask{what: "a body that is not JSON", method: method, path: path,
				body: "{", detail: "unexpected end of JSON input"}),
			row(op, "larger than 1 MiB", ask{what: "a body over 1 MiB", method: method, path: path, body: tooLarge(),
				detail: "request body is too large limit=1048576 bytes"}),
			row(op, "The Content-Type of the body", ask{what: "a body that is not JSON by its Content-Type",
				method: method, path: path, header: plain, body: s, detail: "unknown content type"}),
			row(op, "The body breaks the schema", ask{what: "a field the schema does not know", method: method,
				path: path, body: unknown, detail: "unexpected property"}),
		}
	}
	var replaceRule []answerRow
	if !k.noSpec {
		replaceRule = []answerRow{row(replace, "breaks a rule that the schema cannot hold", ask{what: "a broken rule",
			method: http.MethodPut, path: item, body: k.broken, detail: k.detail})}
	}
	return slices.Concat(
		[]answerRow{row(list, "", ask{what: "the list", method: http.MethodGet, path: coll, detail: named})},
		refused(list, http.MethodGet, coll, ""),

		[]answerRow{
			row(get, "", ask{what: "an object", method: http.MethodGet, path: item, detail: named}),
			row(get, "of that name exists", ask{what: "a missing object", method: http.MethodGet, path: coll + "/nope",
				detail: "not found"}),
			row(get, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodGet,
				path: coll + "/Not_A_Label", detail: "path.name"}),
		},
		refused(get, http.MethodGet, item, ""),

		[]answerRow{
			row(create, "", ask{what: "a new object", method: http.MethodPost, path: coll, body: renamed(s, k.name+"-new"),
				detail: `"name":"` + string(k.name) + `-new"`}),
			row(create, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodPost,
				path: coll + "?force=true", body: s, detail: "unknown query parameter"}),
			row(create, "The dryRun parameter", ask{what: "a dryRun that is no boolean", method: http.MethodPost,
				path: coll + "?dryRun=maybe", body: s, detail: "invalid boolean"}),
			row(create, "breaks a rule that the schema cannot hold", ask{what: "a broken rule", method: http.MethodPost,
				path: coll, body: renamed(k.broken, k.name+"-broken"), detail: k.detail}),
			row(create, "already exists", ask{what: "a taken name", method: http.MethodPost, path: coll, body: s,
				detail: "already exists"}),
		},
		refused(create, http.MethodPost, coll, s),
		bodies(create, http.MethodPost, coll),

		[]answerRow{
			row(replace, "", ask{what: "the same object again", method: http.MethodPut, path: item, body: s, detail: named}),
			row(replace, "of that name exists", ask{what: "a missing object", method: http.MethodPut, path: coll + "/nope",
				body: renamed(s, "nope"), detail: "not found"}),
			row(replace, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodPut,
				path: coll + "/Not_A_Label", body: s, detail: "path.name"}),
			row(replace, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodPut,
				path: item + "?force=true", body: s, detail: "unknown query parameter"}),
			row(replace, "The dryRun parameter", ask{what: "a dryRun that is no boolean", method: http.MethodPut,
				path: item + "?dryRun=maybe", body: s, detail: "invalid boolean"}),
			row(replace, "The If-Match header is not one strong", ask{what: "a weak If-Match", method: http.MethodPut,
				path: item, header: weak, body: s, detail: "is a weak entity-tag"}),
			row(replace, "has changed since", ask{what: "a stale If-Match", method: http.MethodPut, path: item,
				header: stale, body: s, detail: "has changed since resourceVersion"}),
			row(replace, "is not the path's", ask{what: "a body of another name", method: http.MethodPut, path: item,
				body: renamed(s, "x"), detail: `body name "x" does not match path`}),
		},
		replaceRule,
		refused(replace, http.MethodPut, item, s),
		bodies(replace, http.MethodPut, item),

		[]answerRow{
			{op: remove, on: e, setup: func(t *testing.T, e *answerEnv) {
				ask{what: "the fixture " + string(doomed), method: http.MethodPost, path: coll, token: answersAdmin,
					body: renamed(s, doomed), status: http.StatusOK}.run(t, e.c, e.api)
			}, ask: ask{what: "an object", method: http.MethodDelete, path: coll + "/" + string(doomed), token: answersAdmin}},
			row(remove, "of that name exists", ask{what: "a missing object", method: http.MethodDelete,
				path: coll + "/nope", detail: "not found"}),
			row(remove, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodDelete,
				path: coll + "/Not_A_Label", detail: "path.name"}),
			row(remove, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodDelete,
				path: item + "?force=true", detail: "unknown query parameter"}),
			row(remove, "The If-Match header is not one strong", ask{what: "a weak If-Match", method: http.MethodDelete,
				path: item, header: weak, detail: "is a weak entity-tag"}),
			row(remove, "has changed since", ask{what: "a stale If-Match", method: http.MethodDelete, path: item,
				header: stale, detail: "resourceVersion"}),
		},
		refused(remove, http.MethodDelete, item, ""),
	)
}

// tokenRows are the rows of the route that mints the token of object name of a cluster-scoped kind, whose ttl is a duration
// in bounds.
func tokenRows(e *answerEnv, kind, path, name, bounds string) []answerRow {
	op := "mint" + kind
	if kind == "WorkerNode" {
		op = "mintWorkerNodeJoinToken"
	}
	coll := "/apis/funcd.io/v1alpha1/" + path
	item := coll + "/" + name + "/token"
	row := func(when, what, path, body, detail string) answerRow {
		return answerRow{op: op, when: when, on: e, ask: ask{what: what, method: http.MethodPost, path: path, token: answersAdmin,
			body: body, detail: detail}}
	}
	noToken := row("carries no bearer token", "no credential", item, `{}`, "missing bearer token")
	noToken.ask.token = ""
	developer := row("does not allow it", "a developer", item, `{}`, "may not act on cluster-scoped kind")
	developer.ask.token = answersDev
	text := row("The Content-Type of the body", "a body that is not JSON by its Content-Type", item, `{}`, "unknown content type")
	text.ask.header = []string{"Content-Type", "text/plain"}
	return []answerRow{row("", "a token", item, `{"ttl":"10m"}`, `"token":`), noToken, developer, text,
		row("of that name exists", "a missing object", coll+"/nope/token", `{}`, "nope"),
		row("in the path is not", "a name that is no DNS-1123 label", coll+"/Not_A_Label/token", `{}`, "path.name"),
		row("The query holds a parameter", "an unknown query parameter", item+"?force=true", `{}`, "unknown query parameter"),
		row("is not a duration "+bounds, "a ttl that is no duration", item, `{"ttl":"forever"}`, "is not a duration"),
		row("The body is missing or is not JSON", "a body that is not JSON", item, "{", "unexpected end of JSON input"),
		row("larger than 1 MiB", "a body over 1 MiB", item, tooLarge(), "request body is too large limit=1048576 bytes"),
		row("The body breaks the schema", "a field the schema does not know", item, `{"bogus":1}`, "unexpected property"),
	}
}

// postFixture creates k's object with the admin's token, at its kind's collection in default.
func postFixture(t *testing.T, e *answerEnv, k kindFixture) {
	t.Helper()
	ask{what: "the fixture " + string(k.name), method: http.MethodPost,
		path: "/apis/funcd.io/v1alpha1/namespaces/default/" + k.path, token: answersAdmin, body: k.json,
		status: http.StatusOK}.run(t, e.c, e.api)
}

// unscoped is body with its metadata.resourceGroup removed, which every namespaced kind requires.
func unscoped(body string) string { return strings.Replace(body, `,"resourceGroup":"rg1"`, "", 1) }

// resourceGroup is ResourceGroup name of default, itself in the group rg1.
func resourceGroup(name v1.ObjectName) *v1.ResourceGroup {
	return &v1.ResourceGroup{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindResourceGroup.GVK().APIVersion(), Kind: v1.KindResourceGroup},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
}

// inGroup is fn in the group group.
func inGroup(group v1.ResourceGroupName, fn *v1.Function) *v1.Function {
	fn.ResourceGroup = group
	return fn
}

// The answers of the Namespaces feature's operations, one row per case of each: Namespace and ResourceGroup.
func TestNamespacesAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, asCluster(t))
	placeOnOfflineNode(t, e)
	create(t, e.c, e.api, inNamespace("e1", function("held")))
	// busy has a member; kept has one that its own rules keep, a Function that a Function outside the group links to.
	for _, obj := range []v1.Object{namespace("tenant"), resourceGroup("team"), resourceGroup("busy"),
		resourceGroup("kept"), inGroup("busy", function("member")), inGroup("kept", function("anchor")),
		linking("pointer", "anchor")} {
		create(t, e.c, e.api, obj)
	}
	budgets := namespace("tenant")
	budgets.Spec.VolumeBudget, budgets.Spec.VolumeBudgetBytes = "1Mi", 1<<20
	group := kindFixture{kind: "ResourceGroup", kinds: "ResourceGroups", path: "resourcegroups", name: "team",
		json: jsonOf(t, resourceGroup("team")), detail: `resourceGroup is required for kind "ResourceGroup"`}
	group.broken = unscoped(group.json)
	groups := group.sharedRows(e)
	for i, r := range groups {
		if r.op == "deleteResourceGroup" { // force is a parameter of this delete, so the unknown one is another
			groups[i].ask.path = strings.Replace(r.ask.path, "?force=true", "?bogus=true", 1)
		}
	}
	const rgs = "/apis/funcd.io/v1alpha1/namespaces/default/resourcegroups"

	moved := namespace("e1")
	wn := &v1.WorkerNode{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkerNode.GVK().APIVersion(), Kind: v1.KindWorkerNode}}
	wn.Name = "vm1"
	node := clusterFixture{kind: "WorkerNode", kinds: "WorkerNodes", path: "workernodes", name: "vm1", json: jsonOf(t, wn),
		noSpec: true, detail: "namespace must be empty"}
	node.broken = strings.Replace(node.json, `"name":"vm1"`, `"name":"vm1","namespace":"default"`, 1)

	rows := slices.Concat(
		clusterFixture{kind: "Namespace", kinds: "Namespaces", path: "namespaces", name: "tenant",
			json: jsonOf(t, namespace("tenant")), broken: jsonOf(t, budgets),
			detail: "spec.volumeBudget and spec.volumeBudgetBytes are both set"}.sharedRows(e),
		node.sharedRows(e),
		tokenRows(e, "WorkerNode", "workernodes", "vm1", "from 1s to 24h"),
		groups,
		[]answerRow{
			{op: "replaceNamespace", when: "changes spec.node", on: e, ask: ask{what: "another node for a namespace that holds objects",
				method: http.MethodPut, path: "/apis/funcd.io/v1alpha1/namespaces/e1", token: answersAdmin, body: jsonOf(t, moved),
				detail: "namespace e1 holds objects, so its spec.node cannot change"}},
			{op: "deleteWorkerNode", when: "spec.node names the WorkerNode", on: e, ask: ask{what: "a node that runs a namespace",
				method: http.MethodDelete, path: "/apis/funcd.io/v1alpha1/workernodes/vm1", token: answersAdmin,
				detail: "still runs namespaces [e1]"}},
			{op: "deleteResourceGroup", when: "still has members", on: e, ask: ask{what: "a group with a member",
				method: http.MethodDelete, path: rgs + "/busy", token: answersDev,
				detail: `ResourceGroup "busy" still has 1 member(s): Function/member`}},
			{op: "deleteResourceGroup", when: "With force=true", on: e, ask: ask{
				what: "a forced delete that a member refuses", method: http.MethodDelete, path: rgs + "/kept?force=true",
				token: answersDev, detail: `function "anchor" is linked by "pointer"`}},
			{op: "deleteResourceGroup", when: "The force parameter", on: e, ask: ask{what: "a force that is no boolean",
				method: http.MethodDelete, path: rgs + "/team?force=maybe", token: answersDev, detail: "invalid boolean"}},
		},
	)
	runMatrix(t, rows)
}

// identity is the external Identity name of default.
func identity(name v1.ObjectName) *v1.Identity {
	return &v1.Identity{TypeMeta: v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal}}
}

// role is Role name of default, which grants actions.
func role(name v1.ObjectName, actions ...string) *v1.Role {
	return &v1.Role{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRole.GVK().APIVersion(), Kind: v1.KindRole},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.RoleSpec{Actions: append([]string{}, actions...)}}
}

// rolesAssignment is RolesAssignment name of default, whose entries grant their roles to the Identity analyst across
// the namespace.
func rolesAssignment(name v1.ObjectName, entries ...v1.AssignmentEntry) *v1.RolesAssignment {
	return &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindIdentity, Name: "analyst"},
			Scope: &v1.ScopeRef{Kind: v1.ScopeKindNamespace}, Assignments: append([]v1.AssignmentEntry{}, entries...)}}
}

// policy is Policy name of default, whose spec.cedar is cedar.
func policy(name v1.ObjectName, cedar string) *v1.Policy {
	return &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: cedar}}
}

// The answers of the Security feature's operations, one row per case of each: Identity, Role, RolesAssignment and
// Policy.
func TestSecurityAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	reader := v1.AssignmentEntry{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Blob Data Reader"}}
	const forbid = `forbid(principal == Function::"default/denied", action == Action::"kv::read", resource);`
	id := kindFixture{kind: "Identity", kinds: "Identities", path: "identities", name: "analyst",
		json: jsonOf(t, identity("analyst")), detail: `resourceGroup is required for kind "Identity"`}
	id.broken = unscoped(id.json)
	fixtures := []kindFixture{id,
		{kind: "Role", kinds: "Roles", path: "roles", name: "object-writer",
			json: jsonOf(t, role("object-writer", "s3::write")), broken: jsonOf(t, role("object-writer")),
			detail: "spec.actions must list at least one action"},
		{kind: "RolesAssignment", kinds: "RolesAssignments", path: "rolesassignments", name: "analyst-reads",
			json: jsonOf(t, rolesAssignment("analyst-reads", reader)), broken: jsonOf(t, rolesAssignment("analyst-reads")),
			detail: "spec.assignments must have at least one entry"},
		{kind: "Policy", kinds: "Policies", path: "policies", name: "no-reads", json: jsonOf(t, policy("no-reads", forbid)),
			broken: jsonOf(t, policy("no-reads", " ")), detail: "spec.cedar must not be empty"},
	}
	var rows []answerRow
	for _, k := range fixtures {
		postFixture(t, e, k)
		rows = append(rows, k.sharedRows(e)...)
	}

	token := func(spec v1.APITokenSpec) string {
		tk := &v1.APIToken{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAPIToken.GVK().APIVersion(), Kind: v1.KindAPIToken}, Spec: spec}
		tk.Name = "ci"
		return jsonOf(t, tk)
	}
	ci := clusterFixture{kind: "APIToken", kinds: "APITokens", path: "apitokens", name: "ci",
		json: token(v1.APITokenSpec{Role: "viewer", Namespaces: []v1.NamespaceName{"default"}}), broken: token(v1.APITokenSpec{Role: "viewer"}),
		detail: "a viewer acts in at least one namespace"}
	ask{what: "the API token", method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/apitokens", token: answersAdmin,
		body: ci.json, status: http.StatusOK}.run(t, e.c, e.api)
	rows = append(rows, ci.sharedRows(e)...)
	rows = append(rows, tokenRows(e, "APIToken", "apitokens", "ci", "from 1s to 8760h")...)

	const policies = "/apis/funcd.io/v1alpha1/namespaces/default/policies"
	for _, c := range []struct{ when, what, cedar, detail string }{
		{"does not parse as Cedar", "text that does not parse", "permit(", "cedar policy does not parse"},
		{"exactly one action", "an action that funcd does not know",
			`permit(principal, action == Action::"kv::fly", resource);`, `references unknown action "kv::fly"`},
		{"another namespace", "an entity of another namespace",
			`permit(principal == Function::"other/reader", action == Action::"kv::read", resource);`,
			`names Function::"other/reader" outside the Policy's namespace "default"`},
	} {
		rows = append(rows,
			answerRow{op: "createPolicy", when: c.when, on: e, ask: ask{what: c.what, method: http.MethodPost,
				path: policies, token: answersDev, body: jsonOf(t, policy("refused", c.cedar)), detail: c.detail}},
			answerRow{op: "replacePolicy", when: c.when, on: e, ask: ask{what: c.what, method: http.MethodPut,
				path: policies + "/no-reads", token: answersDev, body: jsonOf(t, policy("no-reads", c.cedar)),
				detail: c.detail}})
	}
	runMatrix(t, rows)
}

// egressPolicy is EgressPolicy name of default, which allows rules.
func egressPolicy(name v1.ObjectName, rules ...v1.EgressRule) *v1.EgressPolicy {
	return &v1.EgressPolicy{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEgressPolicy.GVK().APIVersion(), Kind: v1.KindEgressPolicy},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.EgressPolicySpec{Rules: append([]v1.EgressRule{}, rules...)}}
}

// route is Route name of default, whose one rule, at path on devices.example.com, calls the Function devices-api.
func route(name v1.ObjectName, path string) *v1.Route {
	return &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RouteSpec{Host: "devices.example.com",
			Rules: []v1.RouteRule{{Path: path, Backend: v1.RouteBackend{Function: "devices-api"}}}}}
}

// The answers of the Networking feature's operations, one row per case of each: Route and EgressPolicy.
func TestNetworkingAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, asCluster(t))
	placeOnOfflineNode(t, e)
	https := v1.EgressRule{To: v1.EgressTo{Domains: []string{"*.example.com"}}, Ports: []int{443}}
	fixtures := []kindFixture{
		{kind: "Route", kinds: "Routes", path: "routes", name: "devices", json: jsonOf(t, route("devices", "/")),
			broken: jsonOf(t, route("devices", "devices")), detail: `spec.rules[0].path "devices" must begin with '/'`},
		{kind: "EgressPolicy", kinds: "EgressPolicies", path: "egresspolicies", name: "egress",
			json: jsonOf(t, egressPolicy("egress", https)), broken: jsonOf(t, egressPolicy("egress")),
			detail: "spec.rules must list at least one rule"},
	}
	var rows []answerRow
	for _, k := range fixtures {
		postFixture(t, e, k)
		rows = append(rows, k.sharedRows(e)...)
	}
	rows = append(rows, hostRows(t, e, "Route", "routes", "devices", func(name v1.ObjectName, host string) string {
		r := route(name, "/")
		r.Spec.Host = host
		return jsonOf(t, r)
	})...)
	runMatrix(t, rows)
}

// oidcProviderFixture is OIDCProvider name of default, whose client is written by hand.
func oidcProviderFixture(name v1.ObjectName) *v1.OIDCProvider {
	p := &v1.OIDCProvider{Spec: v1.OIDCProviderSpec{Issuer: "https://login.example.com/auth/v1",
		Client: v1.OIDCProviderClient{CredentialsSecret: "sso-client"}, SessionSecret: "sso-session"}}
	p.TypeMeta, p.ObjectMeta = fixtureMeta(v1.KindOIDCProvider, name)
	return p
}

// The answers of the OIDCProvider's operations (ADR-0335), one row per case of each.
func TestOIDCProviderAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	broken := oidcProviderFixture("sso")
	broken.Spec.Client.Registration = &v1.OIDCRegistration{}
	k := kindFixture{kind: "OIDCProvider", kinds: "OIDCProviders", path: "oidcproviders", name: "sso",
		json: jsonOf(t, oidcProviderFixture("sso")), broken: jsonOf(t, broken),
		detail: "spec.client must set exactly one of registration and credentialsSecret"}
	postFixture(t, e, k)
	runMatrix(t, k.sharedRows(e))
}
