package dataplane

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A Namespace's and a Route backend's descriptions name these paths, so a changed one fails here until they follow
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.NamespaceSpec](), "DefaultExposure", pathPrefix)
	doctag.Names(t, reflect.TypeFor[v1.RouteBackend](), "Function", healthPathsPrefix)
}
