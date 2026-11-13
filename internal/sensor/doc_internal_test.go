package sensor

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// eventing.deliveryAttempts names the attempts that 0 means, so a changed default fails here until it follows
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "eventing.deliveryAttempts", strconv.Itoa(defaultDeliveryAttempts))
}
