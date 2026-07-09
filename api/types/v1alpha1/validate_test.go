package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func fnWith(spec FunctionSpec) *Function {
	f := &Function{Spec: spec}
	f.TypeMeta = TypeMeta{APIVersion: KindFunction.GVK().APIVersion(), Kind: KindFunction}
	f.Name, f.Namespace, f.ResourceGroup = "ok", "default", "rg1"
	return f
}

func svcWith(typ ServiceType, kv *KVServiceSpec, blob *BlobServiceSpec) *Service {
	s := &Service{Spec: ServiceSpec{Type: typ, KV: kv, Blob: blob}}
	s.TypeMeta = TypeMeta{APIVersion: KindService.GVK().APIVersion(), Kind: KindService}
	s.Name, s.Namespace, s.ResourceGroup = "s", "default", "rg1"
	return s
}

func esWith(spec EventSourceSpec) *EventSource {
	e := &EventSource{Spec: spec}
	e.TypeMeta = TypeMeta{APIVersion: KindEventSource.GVK().APIVersion(), Kind: KindEventSource}
	e.Name, e.Namespace, e.ResourceGroup = "e", "default", "rg1"
	return e
}

// scenario: validate-rejects-semantic — admission Validate() catches the cross-field/conditional
// rules JSON Schema can't express (ADR-0048). Field *presence* (handler/artifact) is NOT here — it
// is the shape gate's job (ADR-0020). The Service and EventSource discriminator rules have their own
// exhaustive matrices below (TestServiceValidateMatrix / TestEventSourceValidateMatrix).
func TestValidate_CrossFieldAndConditional(t *testing.T) {
	// Scaling: minReplicas ≤ maxReplicas (only when maxReplicas > 0).
	require.Error(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 5, MaxReplicas: 2}}).Validate(), "minReplicas>maxReplicas")
	require.NoError(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 2, MaxReplicas: 5}}).Validate())
	require.NoError(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 5}}).Validate(), "maxReplicas unset → no cross-field check")
}

// TestServiceValidateMatrix is the Service discriminator matrix: spec.type selects which sub-spec is
// required, forbids the other, and the matching binding must be non-empty (ADR-0048 cross-field rules
// JSON Schema can't express). Parametrized over {type, kv, blob} — each accepted shape passes, every
// malformed shape is fault.Invalid. Covers both discriminator arms + the unknown/empty default branch.
func TestServiceValidateMatrix(t *testing.T) {
	kv := func(b string) *KVServiceSpec { return &KVServiceSpec{Binding: b} }
	blob := func(b string) *BlobServiceSpec { return &BlobServiceSpec{Binding: b} }

	for _, tc := range []struct {
		name  string
		typ   ServiceType
		kv    *KVServiceSpec
		blob  *BlobServiceSpec
		valid bool
	}{
		{"kv with kv sub-spec", ServiceTypeKV, kv("b"), nil, true},
		{"blob with blob sub-spec", ServiceTypeBlob, nil, blob("b"), true},
		{"kv missing kv sub-spec", ServiceTypeKV, nil, nil, false},
		{"kv with blob also set", ServiceTypeKV, kv("b"), blob("c"), false},
		{"kv empty binding", ServiceTypeKV, kv(""), nil, false},
		{"blob missing blob sub-spec", ServiceTypeBlob, nil, nil, false},
		{"blob with kv also set", ServiceTypeBlob, kv("b"), blob("c"), false},
		{"blob empty binding", ServiceTypeBlob, nil, blob(""), false},
		{"unknown type", ServiceType("nope"), nil, nil, false},
		{"empty type", ServiceType(""), nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := svcWith(tc.typ, tc.kv, tc.blob).Validate()
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed Service must be fault.Invalid")
		})
	}
}

// TestEventSourceValidateMatrix is the EventSource v2 kind-union matrix (ADR-0108): exactly one source
// kind, ≥1 named events with unique DNS-1123 names and in-bounds intervals. Each accepted shape passes,
// every malformed shape is fault.Invalid. (The removed v1 `type:`/`function:` keys are rejected at the
// schema edge, additionalProperties:false → 422 — not Validate's concern.)
func TestEventSourceValidateMatrix(t *testing.T) {
	tev := func(name string, iv time.Duration) TimerEvent {
		return TimerEvent{Name: ObjectName(name), Interval: iv}
	}
	timer := func(events ...TimerEvent) EventSourceSpec {
		return EventSourceSpec{Timer: &TimerSource{Events: events}}
	}
	for _, tc := range []struct {
		name  string
		spec  EventSourceSpec
		valid bool
	}{
		{"one named timer event", timer(tev("tick", time.Minute)), true},
		{"multiple named events", timer(tev("fast", time.Second), tev("slow", time.Hour)), true},
		{"no source kind", EventSourceSpec{}, false},
		{"timer with no events", EventSourceSpec{Timer: &TimerSource{}}, false},
		{"duplicate event names", timer(tev("t", time.Minute), tev("t", time.Hour)), false},
		{"non-DNS-1123 event name", timer(tev("Bad_Name", time.Minute)), false},
		{"interval below floor", timer(tev("t", time.Millisecond)), false},
		{"interval above ceiling", timer(tev("t", 48*time.Hour)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := esWith(tc.spec).Validate()
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed EventSource must be fault.Invalid")
		})
	}
}

// TestFunctionLinkValidateMatrix is the spec.links structural matrix (ADR-0064): alias is a
// DNS-1123 label unique within Links, target is a DNS-1123 label. Cross-resource rules
// (target-exists, acyclic, no self-link) are an admission, not here. Parametrized accept/reject.
func TestFunctionLinkValidateMatrix(t *testing.T) {
	link := func(alias, target string) FunctionLink {
		return FunctionLink{Alias: alias, Target: ObjectName(target)}
	}
	for _, tc := range []struct {
		name  string
		links []FunctionLink
		valid bool
	}{
		{"no links", nil, true},
		{"valid single", []FunctionLink{link("payments", "checkout")}, true},
		{"valid multiple", []FunctionLink{link("payments", "checkout"), link("mail", "mailer")}, true},
		{"uppercase alias", []FunctionLink{link("Payments", "checkout")}, false},
		{"empty alias", []FunctionLink{link("", "checkout")}, false},
		{"underscore alias", []FunctionLink{link("pay_ments", "checkout")}, false},
		{"duplicate alias", []FunctionLink{link("a", "x"), link("a", "y")}, false},
		{"bad target", []FunctionLink{link("a", "Bad_Target")}, false},
		{"empty target", []FunctionLink{link("a", "")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fnWith(FunctionSpec{Links: tc.links}).Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed link set must be fault.Invalid")
			}
		})
	}
}

// scenario: binding-validity (structural half) — the spec.kv structural matrix (ADR-0073): alias is a
// DNS-1123 label unique within KV, table is a DNS-1123 label. Cross-resource rules (the store/table
// exist) are an admission, not here.
func TestFunctionKVValidateMatrix(t *testing.T) {
	bind := func(alias, store, table string) FunctionKV {
		return FunctionKV{Alias: alias, Store: ObjectName(store), Table: table}
	}
	for _, tc := range []struct {
		name  string
		kv    []FunctionKV
		valid bool
	}{
		{"no kv", nil, true},
		{"valid single", []FunctionKV{bind("counters", "counters-kv", "table-counters")}, true},
		{"valid multiple", []FunctionKV{bind("a", "s1", "t1"), bind("b", "s2", "t2")}, true},
		{"uppercase alias", []FunctionKV{bind("Counters", "s", "t")}, false},
		{"empty alias", []FunctionKV{bind("", "s", "t")}, false},
		{"underscore alias", []FunctionKV{bind("a_b", "s", "t")}, false},
		{"duplicate alias", []FunctionKV{bind("a", "s1", "t1"), bind("a", "s2", "t2")}, false},
		{"bad table", []FunctionKV{bind("a", "s", "Bad_Table")}, false},
		{"empty table", []FunctionKV{bind("a", "s", "")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fnWith(FunctionSpec{KV: tc.kv}).Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed kv binding set must be fault.Invalid")
			}
		})
	}
}

// scenario: function-config-validate (ADR-0093) — each spec.config entry names a ConfigMap and must
// be a valid DNS-1123 name (mirrors spec.secrets / CatalogService.spec.config). Cross-resource
// existence is the reconciler's fail-closed read, not Validate.
func TestFunctionConfigValidateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config []ObjectName
		valid  bool
	}{
		{"no config", nil, true},
		{"valid single", []ObjectName{"tuning"}, true},
		{"valid multiple", []ObjectName{"tuning", "engine-cfg"}, true},
		{"uppercase name", []ObjectName{"Tuning"}, false},
		{"underscore name", []ObjectName{"tun_ing"}, false},
		{"empty name", []ObjectName{""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fnWith(FunctionSpec{Config: tc.config}).Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed config set must be fault.Invalid")
			}
		})
	}
}

// TestSensorValidateMatrix is the Sensor wiring matrix (ADR-0109): ≥1 dep + ≥1 action, unique dep/action
// names, each action bound to a declared dep with exactly one action kind. (The ${{ }} input check is a
// reconcile-time step, not Validate's.)
func TestSensorValidateMatrix(t *testing.T) {
	se := func(on []Dependency, do []Action) *Sensor {
		s := &Sensor{Spec: SensorSpec{On: on, Do: do}}
		s.TypeMeta = TypeMeta{APIVersion: KindSensor.GVK().APIVersion(), Kind: KindSensor}
		s.Name, s.Namespace, s.ResourceGroup = "s", "default", "rg1"
		return s
	}
	d := func(name, source, event string) Dependency {
		return Dependency{Name: ObjectName(name), Source: ObjectName(source), Event: ObjectName(event)}
	}
	deps := []Dependency{d("dep", "src", "ev")}
	for _, tc := range []struct {
		name  string
		on    []Dependency
		do    []Action
		valid bool
	}{
		{"workflow action", deps, []Action{{Name: "a", On: "dep", Workflow: "wf"}}, true},
		{"function action", deps, []Action{{Name: "a", On: "dep", Function: "fn"}}, true},
		{"no dependencies", nil, []Action{{Name: "a", On: "dep", Workflow: "wf"}}, false},
		{"no actions", deps, nil, false},
		{"dangling dependency", deps, []Action{{Name: "a", On: "nope", Workflow: "wf"}}, false},
		{"both action kinds", deps, []Action{{Name: "a", On: "dep", Workflow: "wf", Function: "fn"}}, false},
		{"neither action kind", deps, []Action{{Name: "a", On: "dep"}}, false},
		{"duplicate dep names", []Dependency{d("dep", "s", "e"), d("dep", "s2", "e2")}, []Action{{Name: "a", On: "dep", Workflow: "wf"}}, false},
		{"duplicate action names", deps, []Action{{Name: "a", On: "dep", Workflow: "wf"}, {Name: "a", On: "dep", Function: "fn"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := se(tc.on, tc.do).Validate()
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed Sensor must be fault.Invalid")
		})
	}
}

// TestRouteValidateMatrix covers the F79 Route semantic rules (ADR-0110).
func TestRouteValidateMatrix(t *testing.T) {
	rt := func(host string, rules ...RouteRule) *Route {
		r := &Route{}
		r.TypeMeta = TypeMeta{APIVersion: KindRoute.GVK().APIVersion(), Kind: KindRoute}
		r.Name, r.Namespace, r.ResourceGroup = "r", "default", "rg1"
		r.Spec = RouteSpec{Host: host, Rules: rules}
		return r
	}
	rule := func(path string, pt PathType, fn string, ms ...HTTPMethod) RouteRule {
		return RouteRule{Path: path, PathType: pt, Methods: ms, Backend: RouteBackend{Function: ObjectName(fn)}}
	}
	for _, tc := range []struct {
		name  string
		route *Route
		valid bool
	}{
		{"minimal prefix", rt("", rule("/orders", "", "orders-fn")), true},
		{"exact + methods", rt("api.example.com", rule("/x", PathTypeExact, "fn", MethodGet, MethodPost)), true},
		{"no rules", rt(""), false},
		{"path not slash-prefixed", rt("", rule("orders", "", "fn")), false},
		{"bad pathType", rt("", rule("/x", PathType("Regex"), "fn")), false},
		{"bad backend label", rt("", rule("/x", "", "Bad_Fn")), false},
		{"empty backend", rt("", rule("/x", "", "")), false},
		{"invalid method", rt("", RouteRule{Path: "/x", Methods: []HTTPMethod{"FETCH"}, Backend: RouteBackend{Function: "fn"}}), false},
		{"duplicate (path,methods)", rt("", rule("/x", "", "a"), rule("/x", "", "b")), false},
		{"same path different methods ok", rt("", rule("/x", "", "a", MethodGet), rule("/x", "", "b", MethodPost)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.route.Validate()
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a malformed Route must be fault.Invalid")
		})
	}
}

// TestNamespaceExposureValidate covers the F79 exposure enum incl. the absent/empty case.
func TestNamespaceExposureValidate(t *testing.T) {
	ns := func(mode ExposureMode) *Namespace {
		n := &Namespace{}
		n.TypeMeta = TypeMeta{APIVersion: KindNamespace.GVK().APIVersion(), Kind: KindNamespace}
		n.Name = "team"
		n.Spec.DefaultExposure = mode
		return n
	}
	require.NoError(t, ns("").Validate(), "absent/empty exposure is accepted")
	require.Equal(t, ExposureImplicit, ExposureMode("").Normalized(), "empty normalizes to implicit")
	require.NoError(t, ns(ExposureImplicit).Validate())
	require.NoError(t, ns(ExposureExplicit).Validate())
	require.Equal(t, ExposureExplicit, ExposureExplicit.Normalized())
	require.Equal(t, fault.Invalid, fault.KindOf(ns(ExposureMode("public")).Validate()))
}

// TestRouteAndNamespaceAuthValidate covers the F77 AuthMode enum (ADR-0113).
func TestRouteAndNamespaceAuthValidate(t *testing.T) {
	rt := func(mode AuthMode) *Route {
		r := &Route{}
		r.TypeMeta = TypeMeta{APIVersion: KindRoute.GVK().APIVersion(), Kind: KindRoute}
		r.Name, r.Namespace, r.ResourceGroup = "r", "default", "rg1"
		r.Spec = RouteSpec{Rules: []RouteRule{{Path: "/x", Backend: RouteBackend{Function: "fn"}}}, Auth: &RouteAuth{Mode: mode}}
		return r
	}
	require.NoError(t, rt(AuthAuthenticated).Validate())
	require.NoError(t, rt(AuthOpen).Validate())
	require.NoError(t, rt("").Validate(), "empty auth mode is accepted (inherits/open)")
	require.Equal(t, fault.Invalid, fault.KindOf(rt(AuthMode("public")).Validate()))

	ns := func(mode AuthMode) *Namespace {
		n := &Namespace{}
		n.TypeMeta = TypeMeta{APIVersion: KindNamespace.GVK().APIVersion(), Kind: KindNamespace}
		n.Name = "team"
		n.Spec.EdgeDefaults = &EdgeDefaults{Auth: &EdgeAuth{Mode: mode}}
		return n
	}
	require.NoError(t, ns(AuthAuthenticated).Validate())
	require.NoError(t, ns(AuthOpen).Validate())
	require.Equal(t, fault.Invalid, fault.KindOf(ns(AuthMode("nope")).Validate()))
}
