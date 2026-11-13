package blobmirror

import (
	"reflect"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// blob.backup.retention names the prefix the mirror writes under, so a changed one fails here until it follows
// (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "blob.backup.retention", rootPrefix)
}
