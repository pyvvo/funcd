package funcd

import (
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// scenario: facade-missing-dep (ADR-0002)
func TestScenario_FacadeMissingDep(t *testing.T) {
	p, err := New()
	if err != nil {
		if fault.KindOf(err) != fault.Invalid {
			t.Fatalf("expected Invalid kind, got %s: %v", fault.KindOf(err), err)
		}
	}
	if p == nil {
		t.Fatal("New must not return nil Platform when err is nil")
	}
}

// scenario: facade-missing-dep — explicit missing dependency
func TestScenario_FacadeMissingDep_Explicit(t *testing.T) {
	err := fault.Invalidf("funcd.New", "store is required")
	if fault.KindOf(err) != fault.Invalid {
		t.Fatalf("expected Invalid, got %s", fault.KindOf(err))
	}
}
