package funcd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// scenario: record-bound-over-reader-cap — a funclog.maxRecordBytes the reader would drop (above its 1 MiB cap), or one
// too small for a record's envelope, fails startup with fault.Invalid naming the key and the cap (ADR-0168).
func TestScenarioRecordBoundOverReaderCap(t *testing.T) {
	t.Parallel()
	for _, n := range []int{2097152, 1023, -1} {
		_, err := funcd.New(funcd.InMemory(), funcd.WithFunclogMaxRecordBytes(n))
		require.Error(t, err, "maxRecordBytes %d", n)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.Contains(t, err.Error(), "funclog.maxRecordBytes")
		require.Contains(t, err.Error(), "1048576")
	}
}
