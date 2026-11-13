package funclog

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The funclog keys' descriptions copy these segment defaults and record bounds, so a changed one fails here until they
// follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[config.Config]()
	doctag.NamesAt(t, cfg, "funclog.segmentMaxBytes", strconv.FormatFloat(float64(defaultSegmentMaxBytes)/(1<<20), 'f', -1, 64)+" MiB")
	doctag.NamesAt(t, cfg, "funclog.segmentMaxAge", v1.Duration(defaultSegmentMaxAge).String())
	doctag.NamesAt(t, cfg, "funclog.maxRecordBytes", strconv.Itoa(DefaultMaxRecordBytes), strconv.Itoa(MaxLineBytes))
}
