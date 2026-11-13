package sql

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The descriptions of a SQLDatabase's resources, of an EventSource's sql events and of the sql and image config keys
// copy these constants, so a changed one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.SQLDatabaseSpec](), "Resources", defaultMemory)
	doctag.Names(t, reflect.TypeFor[v1.EventSourceSpec](), "SQL", strconv.Itoa(maxChangeData>>10)+" KiB")
	cfg := reflect.TypeFor[config.Config]()
	doctag.NamesAt(t, cfg, "sql.callTimeout", v1.Duration(DefaultCallTimeout).String())
	doctag.NamesAt(t, cfg, "runtime.containerd.imageOverride", Runtime)
}
