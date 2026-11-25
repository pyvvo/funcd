package workflow

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The Workflow's field descriptions copy these constants, so a changed one fails here until they follow (ADR-0323
// Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	step := reflect.TypeFor[v1.RunStepStatus]()
	doctag.Names(t, step, "Error", strconv.Itoa(observability.MaxStatusError)+" bytes")
	doctag.Names(t, step, "Attempts", attemptHeader)
	doctag.Names(t, reflect.TypeFor[v1.FunctionStep](), "Image", v1.MaterializedHandler+" export")
	doctag.Names(t, reflect.TypeFor[v1.WorkflowPooling](), "MinReplicas", strconv.Itoa(int(time.Duration(v1.MaterializedIdleTimeout)/time.Minute))+" minutes")
}
