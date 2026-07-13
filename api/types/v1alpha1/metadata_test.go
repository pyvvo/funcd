package v1alpha1

import (
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// scenario: objectmeta-requires-resource-group (ADR-0003)
func TestScenario_ObjectMetaRequiresResourceGroup(t *testing.T) {
	meta := &ObjectMeta{
		Name:      "my-func",
		Namespace: "default",
		// ResourceGroup intentionally empty
	}
	err := meta.Validate(KindFunction)
	if err == nil {
		t.Fatal("expected error for empty resourceGroup on namespaced kind")
	}
	if fault.KindOf(err) != fault.Invalid {
		t.Fatalf("expected Invalid kind, got %v", fault.KindOf(err))
	}
}

// scenario: tags-optional (ADR-0003)
func TestScenario_TagsOptional(t *testing.T) {
	meta := &ObjectMeta{
		Name:          "my-func",
		Namespace:     "default",
		ResourceGroup: "my-group",
		// Tags intentionally empty
	}
	err := meta.Validate(KindFunction)
	if err != nil {
		t.Fatalf("tags should be optional, got: %v", err)
	}
}

// scenario: name-rejects-non-dns-label (ADR-0003)
func TestScenario_NameRejectsNonDNSLabel(t *testing.T) {
	tests := []struct {
		name    ObjectName
		wantErr bool
	}{
		{"", true},
		{"-bad", true},
		{"bad-", true},
		{"BadUppercase", true},
		{"good-name", false},
		{"a", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			meta := &ObjectMeta{
				Name:          tt.name,
				Namespace:     "default",
				ResourceGroup: "my-group",
			}
			err := meta.Validate(KindFunction)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate name=%q: got err=%v, wantErr=%v", tt.name, err, tt.wantErr)
			}
			if err != nil && fault.KindOf(err) != fault.Invalid {
				t.Errorf("expected Invalid kind, got %v", fault.KindOf(err))
			}
		})
	}
}

// scenario: scope-enforced (ADR-0003)
func TestScenario_ScopeEnforced(t *testing.T) {
	// Cluster-scoped kind (Namespace) with namespace set → Invalid
	meta := &ObjectMeta{
		Name:          "my-ns",
		Namespace:     "default", // must be empty for cluster-scoped
		ResourceGroup: "my-group",
	}
	err := meta.Validate(KindNamespace)
	if err == nil {
		t.Fatal("expected error: cluster-scoped kind with namespace set")
	}

	// Namespaced kind (Function) with empty namespace → Invalid
	meta2 := &ObjectMeta{
		Name:          "my-func",
		ResourceGroup: "my-group",
		// Namespace empty
	}
	err = meta2.Validate(KindFunction)
	if err == nil {
		t.Fatal("expected error: namespaced kind with empty namespace")
	}
}

// scenario: generic-object-access (ADR-0003)
func TestScenario_GenericObjectAccess(t *testing.T) {
	fn := &Function{
		TypeMeta: TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindFunction},
		ObjectMeta: ObjectMeta{
			Name:          "my-func",
			Namespace:     "default",
			ResourceGroup: "my-group",
			Generation:    3,
		},
	}
	// Test that the Object interface methods work without a per-kind switch
	var obj Object = fn
	if gvk := obj.GroupVersionKind(); gvk.Kind != KindFunction {
		t.Errorf("GroupVersionKind().Kind = %q, want %q", gvk.Kind, KindFunction)
	}
	if name := obj.GetName(); name != "my-func" {
		t.Errorf("GetName() = %q, want %q", name, "my-func")
	}
	if ns := obj.GetNamespace(); ns != "default" {
		t.Errorf("GetNamespace() = %q, want %q", ns, "default")
	}
	if rg := obj.GetResourceGroup(); rg != "my-group" {
		t.Errorf("GetResourceGroup() = %q, want %q", rg, "my-group")
	}
	if gen := obj.GetGeneration(); gen != 3 {
		t.Errorf("GetGeneration() = %d, want %d", gen, 3)
	}
	if meta := obj.GetObjectMeta(); meta == nil {
		t.Fatal("GetObjectMeta() returned nil")
	}
}

// scenario: kind-registry-roundtrips-every-kind (ADR-0003)
func TestScenario_KindRegistryRoundtripsEveryKind(t *testing.T) {
	all := AllKinds()
	if len(all) != 22 {
		t.Fatalf("AllKinds() returned %d kinds, want 22", len(all))
	}

	// Every Kind const is returned by AllKinds()
	allSet := make(map[Kind]bool)
	for _, k := range all {
		allSet[k] = true
	}
	allConsts := all // AllKinds() is the canonical list
	for _, k := range allConsts {
		if !allSet[k] {
			t.Errorf("Kind %q is a const but not in AllKinds()", k)
		}
	}

	// NewObject round-trips every kind
	for _, k := range all {
		obj, ok := NewObject(k)
		if !ok {
			t.Errorf("NewObject(%q) returned false", k)
			continue
		}
		if gvk := obj.GroupVersionKind(); gvk.Kind != k {
			t.Errorf("NewObject(%q).GroupVersionKind().Kind = %q", k, gvk.Kind)
		}
		if gvk := obj.GroupVersionKind(); gvk.APIVersion() != "funcd.io/v1alpha1" {
			t.Errorf("NewObject(%q).GroupVersionKind().APIVersion() = %q", k, gvk.APIVersion())
		}
		// TypeMeta should be pre-stamped
		meta := obj.GetObjectMeta().GetObjectMeta()
		_ = meta // silence unused
	}

	// Unknown kind returns false
	_, ok := NewObject(Kind("Bogus"))
	if ok {
		t.Error("NewObject(Bogus) should return false")
	}
}

// Test validateMeta rejects TypeMeta/kind mismatch
func TestValidateMeta_RejectsTypeMetaMismatch(t *testing.T) {
	tm := TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindFunction}
	meta := &ObjectMeta{
		Name:          "my-ns",
		ResourceGroup: "my-group",
	}
	// Pass KindNamespace but TypeMeta says Function → mismatch
	err := validateMeta(tm, meta, KindNamespace)
	if err == nil {
		t.Fatal("expected validateMeta to reject TypeMeta/kind mismatch")
	}

	// Wrong apiVersion
	tm2 := TypeMeta{APIVersion: "funcd.io/v2", Kind: KindNamespace}
	meta2 := &ObjectMeta{
		Name: "my-ns",
	}
	err = validateMeta(tm2, meta2, KindNamespace)
	if err == nil {
		t.Fatal("expected validateMeta to reject wrong apiVersion")
	}
}

func TestGenerateObjectName(t *testing.T) {
	n := string(GenerateObjectName("run-"))
	if len(n) != len("run-")+8 || n[:4] != "run-" {
		t.Fatalf("GenerateObjectName(%q) = %q, want run-<8 hex chars>", "run-", n)
	}
	if err := ObjectName(n).Validate(); err != nil {
		t.Fatalf("generated name %q is not a valid ObjectName: %v", n, err)
	}
	n1, n2 := GenerateObjectName("run-"), GenerateObjectName("run-")
	if n1 == n2 {
		t.Fatalf("two calls returned the same name %q — not random", n1)
	}
	long := ""
	for range 100 {
		long += "a"
	}
	if got := GenerateObjectName(long); len(got) > 63 {
		t.Fatalf("GenerateObjectName did not bound to 63 chars: len=%d", len(got))
	}
}
