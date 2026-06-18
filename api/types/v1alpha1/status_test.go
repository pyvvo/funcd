package v1alpha1

import (
	"testing"
	"time"
)

// scenario: conditions-upsert-by-type (ADR-0003)
func TestScenario_ConditionsUpsertByType(t *testing.T) {
	var cs Conditions

	// First set: add a condition — the Set method stamps LastTransitionTime itself.
	cs.Set(Condition{
		Type:   "Ready",
		Status: ConditionFalse,
	})
	if len(cs) != 1 {
		t.Fatalf("Conditions length = %d, want 1", len(cs))
	}
	firstTime := cs[0].LastTransitionTime
	if firstTime.IsZero() {
		t.Fatal("first LastTransitionTime should be non-zero")
	}

	// Second set: same Type, same Status → LastTransitionTime must NOT advance
	cs.Set(Condition{
		Type:   "Ready",
		Status: ConditionFalse,
	})
	if len(cs) != 1 {
		t.Fatalf("Conditions length after same-status upsert = %d, want 1", len(cs))
	}
	if !cs[0].LastTransitionTime.Equal(firstTime) {
		t.Errorf("LastTransitionTime advanced on same status: %v != %v", cs[0].LastTransitionTime, firstTime)
	}

	// Third set: same Type, different Status → LastTransitionTime must advance
	time.Sleep(1 * time.Millisecond) // ensure time advances
	cs.Set(Condition{
		Type:   "Ready",
		Status: ConditionTrue,
	})
	if len(cs) != 1 {
		t.Fatalf("Conditions length after status-change upsert = %d, want 1", len(cs))
	}
	if !cs[0].LastTransitionTime.After(firstTime) {
		t.Errorf("LastTransitionTime should have advanced on status change: %v (was %v)", cs[0].LastTransitionTime, firstTime)
	}
	c, found := cs.Get("Ready")
	if !found {
		t.Fatal("Get(Ready) returned false")
	}
	if c.Status != ConditionTrue {
		t.Errorf("Get(Ready).Status = %q, want True", c.Status)
	}
	if _, found := cs.Get("Missing"); found {
		t.Error("Get(Missing) should return false")
	}
}

// scenario: generic-status-writeback (ADR-0003)
func TestScenario_GenericStatusWriteback(t *testing.T) {
	fn := &Function{
		TypeMeta:   TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindFunction},
		ObjectMeta: ObjectMeta{Name: "test-fn", Namespace: "default", ResourceGroup: "my-group"},
	}

	// Function implements StatusObject
	var so StatusObject = fn
	st := so.GetStatus()
	if st == nil {
		t.Fatal("GetStatus() returned nil")
	}

	// Mutate Phase generically
	st.Phase = PhaseReady
	if fn.Status.Phase != PhaseReady {
		t.Errorf("Phase not written back: %q", fn.Status.Phase)
	}

	// Mutate a Condition generically
	st.Conditions.Set(Condition{
		Type:   "Ready",
		Status: ConditionTrue,
	})
	if len(fn.Status.Conditions) != 1 {
		t.Fatalf("Conditions not written back: len=%d", len(fn.Status.Conditions))
	}

	// Config does NOT implement StatusObject
	cfg := &Config{
		TypeMeta:   TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: KindConfig},
		ObjectMeta: ObjectMeta{Name: "test-cfg", Namespace: "default", ResourceGroup: "my-group"},
	}
	var obj Object = cfg
	if _, ok := obj.(StatusObject); ok {
		t.Error("Config should NOT implement StatusObject")
	}

	// All four pure-data/policy kinds must not implement StatusObject
	pureKinds := []Kind{KindConfig, KindSecret, KindGrant, KindEgressPolicy}
	for _, k := range pureKinds {
		o, ok := NewObject(k)
		if !ok {
			t.Fatalf("NewObject(%q) failed", k)
		}
		if _, ok := o.(StatusObject); ok {
			t.Errorf("Kind %q should NOT implement StatusObject", k)
		}
	}

	// All status-bearing kinds must implement StatusObject
	statusKinds := []Kind{
		KindNamespace, KindResourceGroup, KindFunction, KindRevision,
		KindRoute, KindService, KindEventSource, KindInvocation,
		KindRuntimeClass, KindWorkerNode, KindGateway,
	}
	for _, k := range statusKinds {
		o, ok := NewObject(k)
		if !ok {
			t.Fatalf("NewObject(%q) failed", k)
		}
		if _, ok := o.(StatusObject); !ok {
			t.Errorf("Kind %q should implement StatusObject", k)
		}
	}
}

// Compile-time interface checks (sanitized from the Object/StatusObject test above)
var (
	_ Object       = (*Function)(nil)
	_ StatusObject = (*Function)(nil)
	_ Object       = (*Config)(nil)
	// Config must NOT compile as StatusObject — verified at runtime above
)
