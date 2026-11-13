package controller

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// controller.retryBackoffMax names the first retry wait, so a changed one fails here until it follows (ADR-0323
// Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "controller.retryBackoffMax", "starts at "+v1.Duration(baseBackoff).String())
}
