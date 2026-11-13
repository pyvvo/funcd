package site

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A Site's descriptions name the default index and the folder funcd keeps for its own files, so a changed one fails
// here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	spec := reflect.TypeFor[v1.SiteSpec]()
	doctag.Names(t, spec, "Index", defaultIndex)
	doctag.Names(t, spec, "Image", runtimeDir)
}
