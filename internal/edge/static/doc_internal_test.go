package static

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A static backend's index description names this default, so a changed one fails here until it follows (ADR-0323
// Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.StaticBackend](), "Index", defaultIndex)
}
