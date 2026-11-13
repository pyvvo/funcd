package activator

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A Sensor action's description names this reason, so a renamed one fails here until it follows (ADR-0323
// Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.Action](), "Function", ReasonTargetServesHTTP)
}
