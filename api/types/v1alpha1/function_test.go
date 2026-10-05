package v1alpha1

import (
	"reflect"
	"strconv"
	"testing"
)

// The OpenAPI schema bounds spec.timeout by its struct tag; it must stay the cap funcd.New enforces (ADR-0151).
func TestFunctionSpecTimeoutTagIsMaxInvokeTimeout(t *testing.T) {
	f, ok := reflect.TypeFor[FunctionSpec]().FieldByName("Timeout")
	if !ok {
		t.Fatal("FunctionSpec has no Timeout field")
	}
	if got, want := f.Tag.Get("maximum"), strconv.FormatInt(int64(MaxInvokeTimeout), 10); got != want {
		t.Fatalf("spec.timeout maximum tag = %s, want %s (MaxInvokeTimeout)", got, want)
	}
	if got := f.Tag.Get("minimum"); got != "0" {
		t.Fatalf("spec.timeout minimum tag = %s, want 0", got)
	}
}
