package sql

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The descriptions of a SQLDatabase's resources and of an EventSource's sql events copy these constants, so a changed
// one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.SQLDatabaseSpec](), "Resources", defaultMemory)
	doctag.Names(t, reflect.TypeFor[v1.EventSourceSpec](), "SQL", strconv.Itoa(maxChangeData>>10)+" KiB")
}
