package cedar

import (
	"reflect"
	"slices"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A RolesAssignment's role reference lists the built-in roles, so an added, removed or renamed one fails here until
// its description follows (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	names := make([]string, 0, len(builtinRoles))
	for name := range builtinRoles {
		names = append(names, name)
	}
	slices.Sort(names)
	doctag.Names(t, reflect.TypeFor[v1.RoleRef](), "Name", names...)
}
