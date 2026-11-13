package health

import (
	"reflect"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// health.storageProbeInterval names the reason of a store whose probe fails, so a changed one fails here until it
// follows (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "health.storageProbeInterval", ReasonStorageUnreachable)
}
