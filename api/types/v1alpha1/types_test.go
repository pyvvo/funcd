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

func TestPhase_Validate(t *testing.T) {
	tests := []struct {
		name    string
		phase   Phase
		wantErr bool
	}{
		{"Pending", PhasePending, false},
		{"Ready", PhaseReady, false},
		{"Failed", PhaseFailed, false},
		{"Deleted", PhaseDeleted, false},
		{"Scaling", PhaseScaling, false},
		{"Reconciling", PhaseReconciling, false},
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
	if !PhaseFailed.IsTerminal() {
		t.Error("Failed should be terminal")
	}
	if !PhaseDeleted.IsTerminal() {
		t.Error("Deleted should be terminal")
	}
	if PhaseReady.IsTerminal() {
		t.Error("Ready should not be terminal")
	}
}
