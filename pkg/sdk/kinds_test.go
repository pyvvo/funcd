package sdk

import (
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// scenario: kinddescriptor-covers-all-kinds (the drift guard against the server routes).
func TestKindDescriptorCoversAllKinds(t *testing.T) {
	t.Parallel()
	for _, k := range v1.AllKinds() {
		if _, ok := kindDescriptors[k]; !ok {
			t.Errorf("kind %q has no kindDescriptor (route plural missing)", k)
		}
	}
}

func TestKindFromToken(t *testing.T) {
	t.Parallel()
	cases := map[string]v1.Kind{
		"functions":      v1.KindFunction,
		"function":       v1.KindFunction,
		"fn":             v1.KindFunction,
		"namespaces":     v1.KindNamespace,
		"egresspolicies": v1.KindEgressPolicy,
		"runtimeclasses": v1.KindRuntimeClass,
	}
	for token, want := range cases {
		got, ok := KindFromToken(token)
		if !ok || got != want {
			t.Errorf("KindFromToken(%q) = %q,%v; want %q,true", token, got, ok, want)
		}
	}
	if _, ok := KindFromToken("frobnicate"); ok {
		t.Errorf("KindFromToken(unknown) should be !ok")
	}
}
