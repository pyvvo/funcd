package v1alpha1

import (
	"testing"

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

func esWith(typ EventSourceType, timer *TimerSpec, fn ObjectName) *EventSource {
	e := &EventSource{Spec: EventSourceSpec{Type: typ, Timer: timer, Function: fn}}
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

// TestEventSourceValidateMatrix is the EventSource discriminator matrix: spec.type==timer requires the
// timer sub-spec, type==http forbids it, and a target function is always required (ADR-0048). Parametrized
// over {type, timer, function} — each accepted shape passes, every malformed shape is fault.Invalid.
// Covers both discriminator arms, the missing-function rule, and the unknown/empty default branch.
func TestEventSourceValidateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   EventSourceType
		timer *TimerSpec
		fn    ObjectName
		valid bool
	}{
		{"timer with sub-spec + fn", EventSourceTypeTimer, &TimerSpec{}, "fn", true},
		{"http without timer + fn", EventSourceTypeHTTP, nil, "fn", true},
		{"timer missing sub-spec", EventSourceTypeTimer, nil, "fn", false},
		{"http with timer set", EventSourceTypeHTTP, &TimerSpec{}, "fn", false},
		{"timer without function", EventSourceTypeTimer, &TimerSpec{}, "", false},
		{"http without function", EventSourceTypeHTTP, nil, "", false},
		{"unknown type", EventSourceType("nope"), nil, "fn", false},
		{"empty type", EventSourceType(""), nil, "fn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := esWith(tc.typ, tc.timer, tc.fn).Validate()
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
