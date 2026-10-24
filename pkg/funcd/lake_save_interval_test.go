package funcd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// ADR-0274: WithLakeSaveInterval takes 0 (none set) or whole milliseconds from 500ms to 1h, as lake.saveInterval does,
// so a library caller cannot give an engine a value its shim refuses.
func TestWithLakeSaveIntervalBounds(t *testing.T) {
	t.Parallel()
	for d, ok := range map[time.Duration]bool{
		0:                       true,
		500 * time.Millisecond:  true,
		1500 * time.Millisecond: true,
		time.Hour:               true,
		400 * time.Millisecond:  false,
		time.Hour + time.Second: false,
		1500 * time.Microsecond: false,
		-time.Second:            false,
	} {
		_, err := funcd.New(funcd.InMemory(), funcd.WithLakeSaveInterval(d))
		if ok {
			require.NoError(t, err, d)
			continue
		}
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", d, err)
		require.ErrorContains(t, err, "lake save interval", d)
	}
}
