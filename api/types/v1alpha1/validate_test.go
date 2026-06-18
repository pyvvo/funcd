package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
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
// is the shape gate's job (ADR-0020).
func TestValidate_CrossFieldAndConditional(t *testing.T) {
	// Scaling: minReplicas ≤ maxReplicas (only when maxReplicas > 0).
	require.Error(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 5, MaxReplicas: 2}}).Validate(), "minReplicas>maxReplicas")
	require.NoError(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 2, MaxReplicas: 5}}).Validate())
	require.NoError(t, fnWith(FunctionSpec{Scaling: Scaling{MinReplicas: 5}}).Validate(), "maxReplicas unset → no cross-field check")

	// Service: type ↔ sub-spec.
	require.NoError(t, svcWith(ServiceTypeKV, &KVServiceSpec{Binding: "b"}, nil).Validate())
	require.NoError(t, svcWith(ServiceTypeBlob, nil, &BlobServiceSpec{Binding: "b"}).Validate())
	require.Error(t, svcWith(ServiceTypeKV, nil, nil).Validate(), "type kv without kv sub-spec")
	require.Error(t, svcWith(ServiceTypeKV, &KVServiceSpec{Binding: "b"}, &BlobServiceSpec{Binding: "c"}).Validate(), "kv with blob set")
	require.Error(t, svcWith(ServiceTypeKV, &KVServiceSpec{Binding: ""}, nil).Validate(), "empty binding")
	require.Error(t, svcWith("nope", nil, nil).Validate(), "unknown service type")

	// EventSource: type ↔ timer sub-spec + a function target.
	require.NoError(t, esWith(EventSourceTypeTimer, &TimerSpec{Interval: 0}, "fn").Validate())
	require.NoError(t, esWith(EventSourceTypeHTTP, nil, "fn").Validate())
	require.Error(t, esWith(EventSourceTypeTimer, nil, "fn").Validate(), "timer type without timer sub-spec")
	require.Error(t, esWith(EventSourceTypeHTTP, &TimerSpec{}, "fn").Validate(), "http type with timer set")
	require.Error(t, esWith(EventSourceTypeTimer, &TimerSpec{}, "").Validate(), "no function target")
}
