package safemode

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// recovery.safeMode.afterCrashes names the exit status of a stopped start, so a changed one fails here until it
// follows (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "recovery.safeMode.afterCrashes", strconv.Itoa(ExitStopped))
}
