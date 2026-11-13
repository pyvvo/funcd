package router

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A Route's and a Namespace's descriptions name these reasons and the path that calls Functions by name, so a changed
// one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	route := reflect.TypeFor[v1.RouteSpec]()
	doctag.Names(t, route, "Host", reasonHostRequired, reasonHostReserved, functionPath)
	doctag.Names(t, route, "Enabled", reasonDisabled)
	doctag.Names(t, reflect.TypeFor[v1.RouteRule](), "Path", reasonReservedPath, functionPath)
	doctag.Names(t, reflect.TypeFor[v1.NamespaceSpec](), "DefaultExposure", reasonHostRequired)
}
