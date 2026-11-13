package eventing

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// eventing.blobPollInterval names the poll cadence that empty means, so a changed default fails here until it follows
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "eventing.blobPollInterval", v1.Duration(defaultBlobPollInterval).String())
}
