package v1alpha1

import (
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

// TestByteSizeGrammar (ADR-0266 Decision 1): binary units either way, plain bytes, nothing else, and never a rounding.
func TestByteSizeGrammar(t *testing.T) {
	ok := map[ByteSize]int64{
		"1048576": 1048576, "1Ki": 1024, "1KiB": 1024, "512Ki": 524288, "2Gi": 2 << 30, "2GiB": 2 << 30, "1Ti": 1 << 40,
		"999999999999999": 999999999999999, "8388607TiB": 8388607 << 40,
	}
	for s, want := range ok {
		if got, err := s.Bytes(); err != nil || got != want {
			t.Fatalf("%q.Bytes() = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []ByteSize{"2G", "2GB", "1.5Gi", "1 Gi", "-1", "2gib", "Gi", "", "1000000000000000", "8388608Ti", "999999999999999Ti"} {
		if _, err := s.Bytes(); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("%q.Bytes() = %v, want Invalid", s, err)
		}
	}
}

// TestScenarioSizeWithUnits: size 2GiB is the same 2147483648 bytes as sizeBytes 2147483648.
// scenario: size-with-units
func TestScenarioSizeWithUnits(t *testing.T) {
	a := VolumeEntry{Name: "state", Type: VolumeLocal, Size: "2GiB"}
	b := VolumeEntry{Name: "state", Type: VolumeLocal, SizeBytes: 2147483648}
	if a.Bytes() != 2147483648 || a.Bytes() != b.Bytes() {
		t.Fatalf("Bytes = %d and %d, want 2147483648 both", a.Bytes(), b.Bytes())
	}
	if err := (&FunctionSpec{Volumes: []VolumeEntry{a}}).Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
}

// TestScenarioOneSizeForm: both size and sizeBytes, or neither, is refused, naming the volume.
// scenario: one-size-form
func TestScenarioOneSizeForm(t *testing.T) {
	for _, v := range []VolumeEntry{
		{Name: "state", Type: VolumeLocal, Size: "1Mi", SizeBytes: 1048576},
		{Name: "state", Type: VolumeLocal},
	} {
		err := (&FunctionSpec{Volumes: []VolumeEntry{v}}).Validate()
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "spec.volumes[0]") || !strings.Contains(err.Error(), "exactly one of size and sizeBytes") {
			t.Fatalf("Validate(%+v) = %v, want Invalid naming spec.volumes[0]", v, err)
		}
	}
}

// TestScenarioBadSize: a decimal unit, a fraction, zero, sixteen digits or a size past 2⁶³−1 is refused, naming the field.
// scenario: bad-size
func TestScenarioBadSize(t *testing.T) {
	for _, s := range []ByteSize{"2G", "1.5Gi", "0", "1234567890123456", "999999999999999Ti"} {
		err := (&FunctionSpec{Volumes: []VolumeEntry{{Name: "state", Type: VolumeEmptyDir, Size: s}}}).Validate()
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "spec.volumes[0].size") {
			t.Fatalf("size %q: Validate = %v, want Invalid naming spec.volumes[0].size", s, err)
		}
	}
}

// TestScenarioBudgetAndCapacityWithUnits: volumeBudget 4Gi is the budget volumeBudgetBytes 4294967296 is, and a
// Namespace setting both is refused; cmd/funcd tests the capacity's twin rule.
// scenario: budget-and-capacity-with-units
func TestScenarioBudgetAndCapacityWithUnits(t *testing.T) {
	if got := (NamespaceSpec{VolumeBudget: "4Gi"}).VolumeBudgetOf(); got != 4294967296 {
		t.Fatalf("VolumeBudgetOf = %d", got)
	}
	if got := (NamespaceSpec{VolumeBudgetBytes: 4294967296}).VolumeBudgetOf(); got != 4294967296 {
		t.Fatalf("VolumeBudgetOf = %d", got)
	}
	n := &Namespace{TypeMeta: typeMetaFor(KindNamespace), ObjectMeta: ObjectMeta{Name: "shop"}, Spec: NamespaceSpec{VolumeBudget: "4Gi", VolumeBudgetBytes: 1}}
	if err := n.Validate(); fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both forms = %v, want Invalid", err)
	}
	n.Spec.VolumeBudgetBytes = 0
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	n.Spec.VolumeBudget = "4G"
	if err := n.Validate(); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a decimal budget = %v, want Invalid", err)
	}
}
