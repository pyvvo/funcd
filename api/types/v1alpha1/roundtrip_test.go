package v1alpha1

import (
	"encoding/json"
	"testing"
)

// scenario: json-roundtrip-stable (ADR-0003)
func TestScenario_JSONRoundtripStable(t *testing.T) {
	for _, k := range AllKinds() {
		obj, ok := NewObject(k)
		if !ok {
			t.Fatalf("NewObject(%q) failed", k)
		}
		// Populate metadata minimally
		meta := obj.GetObjectMeta()
		meta.Name = "test-resource"
		if k.Namespaced() {
			meta.Namespace = "default"
			meta.ResourceGroup = "my-group"
		}

		// Marshal to JSON
		data, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", k, err)
		}

		// Unmarshal back into a fresh object
		fresh, ok := NewObject(k)
		if !ok {
			t.Fatalf("NewObject(%q) for unmarshal failed", k)
		}
		if err := json.Unmarshal(data, fresh); err != nil {
			t.Fatalf("Unmarshal(%q): %v", k, err)
		}

		// Verify wire identity
		freshMeta := fresh.GetObjectMeta()
		if freshMeta.Name != "test-resource" {
			t.Errorf("kind %q: name mismatch after roundtrip: %q", k, freshMeta.Name)
		}
		if k.Namespaced() {
			if freshMeta.Namespace != "default" {
				t.Errorf("kind %q: namespace mismatch after roundtrip: %q", k, freshMeta.Namespace)
			}
			if freshMeta.ResourceGroup != "my-group" {
				t.Errorf("kind %q: resourceGroup mismatch after roundtrip: %q", k, freshMeta.ResourceGroup)
			}
		}

		// Verify apiVersion and kind appear in JSON as root fields
		// (TypeMeta is ",inline" so they flatten)
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("Unmarshal raw(%q): %v", k, err)
		}
		apiVersion, _ := raw["apiVersion"].(string)
		if apiVersion != "funcd.io/v1alpha1" {
			t.Errorf("kind %q: apiVersion = %q, want funcd.io/v1alpha1", k, apiVersion)
		}
		kind, _ := raw["kind"].(string)
		if kind != string(k) {
			t.Errorf("kind %q: kind field = %q, want %q", k, kind, k)
		}
	}
}

// scenario: json-roundtrip-stable — verify full equality after roundtrip for Function
func TestScenario_JSONRoundtripStable_FullEquality(t *testing.T) {
	fn := &Function{
		TypeMeta: TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindFunction},
		ObjectMeta: ObjectMeta{
			Name:          "my-func",
			Namespace:     "default",
			ResourceGroup: "my-group",
			Tags:          Tags{"env": "prod"},
			Generation:    1,
		},
		Status: FunctionStatus{
			Status: Status{
				Phase: PhasePending,
				Conditions: Conditions{
					{Type: "Ready", Status: ConditionFalse},
				},
			},
		},
	}

	data, err := json.Marshal(fn)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var fn2 Function
	if err := json.Unmarshal(data, &fn2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if fn2.Name != fn.Name {
		t.Errorf("Name mismatch: %q vs %q", fn2.Name, fn.Name)
	}
	if fn2.Namespace != fn.Namespace {
		t.Errorf("Namespace mismatch: %q vs %q", fn2.Namespace, fn.Namespace)
	}
	if fn2.ResourceGroup != fn.ResourceGroup {
		t.Errorf("ResourceGroup mismatch: %q vs %q", fn2.ResourceGroup, fn.ResourceGroup)
	}
	if fn2.Generation != fn.Generation {
		t.Errorf("Generation mismatch: %d vs %d", fn2.Generation, fn.Generation)
	}
	if fn2.APIVersion != fn.APIVersion {
		t.Errorf("apiVersion mismatch: %q vs %q", fn2.APIVersion, fn.APIVersion)
	}
	if fn2.Kind != fn.Kind {
		t.Errorf("Kind mismatch: %q vs %q", fn2.Kind, fn.Kind)
	}
	if fn2.Status.Phase != fn.Status.Phase {
		t.Errorf("Phase mismatch: %q vs %q", fn2.Status.Phase, fn.Status.Phase)
	}
}

// scenario: json-roundtrip-stable — spec.pooling.worker survives the wire (ADR-0046).
func TestScenario_PoolingRoundtripAndValidate(t *testing.T) {
	fn := &Function{
		TypeMeta:   TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindFunction},
		ObjectMeta: ObjectMeta{Name: "agent", Namespace: "default", ResourceGroup: "rg1"},
		Spec: FunctionSpec{
			Runtime:  "nodejs22",
			Handler:  "h",
			Image: "oci://example/app:v1",
			Pooling:  Pooling{Worker: "agents"},
		},
	}
	data, err := json.Marshal(fn)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var fn2 Function
	if err := json.Unmarshal(data, &fn2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fn2.Spec.Pooling.Worker != "agents" {
		t.Errorf("Pooling.Worker mismatch after roundtrip: %q", fn2.Spec.Pooling.Worker)
	}

	// Validate: empty worker (solo) is valid; a DNS-1123 label is valid; a bad label is rejected.
	if err := fn.Validate(); err != nil {
		t.Errorf("a DNS-1123 worker id must validate: %v", err)
	}
	solo := *fn
	solo.Spec.Pooling.Worker = ""
	if err := solo.Validate(); err != nil {
		t.Errorf("an empty (solo) worker id must validate: %v", err)
	}
	bad := *fn
	bad.Spec.Pooling.Worker = "Not A Label"
	if err := bad.Validate(); err == nil {
		t.Error("a non-DNS-1123 worker id must be rejected by Validate")
	}
}
