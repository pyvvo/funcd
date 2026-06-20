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
