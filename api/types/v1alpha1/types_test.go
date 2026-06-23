package v1alpha1

import (
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func TestNamespaceName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   NamespaceName
		wantErr bool
	}{
		{"empty", "", true},
		{"valid simple", "default", false},
		{"valid with hyphens", "my-namespace", false},
		{"valid with digits", "ns123", false},
		{"too long", "this-name-is-way-too-long-and-exceeds-the-maximum-allowed-length-of-63-chars", true},
		{"starts with hyphen", "-bad", true},
		{"ends with hyphen", "bad-", true},
		{"uppercase", "Bad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if err != nil && fault.KindOf(err) != fault.Invalid {
				t.Errorf("expected Invalid kind, got %s", fault.KindOf(err))
			}
		})
	}
}

func TestFunctionName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   FunctionName
		wantErr bool
	}{
		{"empty invalid", "", true},
		{"valid", "my-function", false},
		{"starts with hyphen", "-bad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRevisionID_Validate(t *testing.T) {
	if err := RevisionID("").Validate(); err == nil {
		t.Error("empty RevisionID should fail validation")
	}
	if err := RevisionID("rev-001").Validate(); err != nil {
		t.Errorf("valid RevisionID should not fail: %v", err)
	}
}

// Test new typed primitives from ADR-0003.

func TestObjectName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   ObjectName
		wantErr bool
	}{
		{"empty", "", true},
		{"valid", "my-resource", false},
		{"starts with hyphen", "-bad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestResourceGroupName_Validate(t *testing.T) {
	tests := []struct {
		name    string
		input   ResourceGroupName
		wantErr bool
	}{
		{"empty", "", true},
		{"valid", "my-group", false},
		{"uppercase", "Bad", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestPhase_Validate(t *testing.T) {
	tests := []struct {
		name    string
		phase   Phase
		wantErr bool
	}{
		{"Pending", PhasePending, false},
		{"Deploying", PhaseDeploying, false},
		{"Ready", PhaseReady, false},
		{"Idle", PhaseIdle, false},
		{"Degraded", PhaseDegraded, false},
		{"Failed", PhaseFailed, false},
		{"Terminating", PhaseTerminating, false},
		{"unknown", Phase("Unknown"), true},
		{"empty", Phase(""), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.phase.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.phase, err, tt.wantErr)
			}
		})
	}
}

func TestPhase_IsTerminal(t *testing.T) {
	if !PhaseTerminating.IsTerminal() {
		t.Error("Terminating should be terminal")
	}
	if PhaseReady.IsTerminal() {
		t.Error("Ready should not be terminal")
	}
	if PhaseFailed.IsTerminal() {
		t.Error("Failed should not be terminal")
	}
	// Pending, Deploying, Idle, Degraded are not terminal
	for _, p := range []Phase{PhasePending, PhaseDeploying, PhaseIdle, PhaseDegraded} {
		if p.IsTerminal() {
			t.Errorf("%s should not be terminal", p)
		}
	}
}

func TestKind_Namespaced(t *testing.T) {
	clusterKinds := []Kind{KindNamespace, KindRuntimeClass, KindWorkerNode, KindGateway}
	namespacedKinds := []Kind{
		KindResourceGroup, KindFunction, KindRevision, KindRoute,
		KindService, KindEventSource, KindConfigMap, KindSecret,
		KindGrant, KindEgressPolicy, KindInvocation,
	}
	for _, k := range clusterKinds {
		if k.Namespaced() {
			t.Errorf("Kind %q should be cluster-scoped", k)
		}
	}
	for _, k := range namespacedKinds {
		if !k.Namespaced() {
			t.Errorf("Kind %q should be namespaced", k)
		}
	}
}

func TestKind_Validate(t *testing.T) {
	for _, k := range AllKinds() {
		if err := k.Validate(); err != nil {
			t.Errorf("Kind %q should be valid: %v", k, err)
		}
	}
	if err := Kind("Bogus").Validate(); err == nil {
		t.Error("Bogus kind should be invalid")
	}
}
