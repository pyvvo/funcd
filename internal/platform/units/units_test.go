package units

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseBytesMoved (ADR-0223): parseMemoryBytes's grammar, unchanged, plus 0 and the overflow it let wrap.
func TestParseBytesMoved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1024", 1024, true},
		{"4Gi", 4 << 30, true},
		{"512Mi", 512 << 20, true},
		{"2Ki", 2 << 10, true},
		{"1Ti", 1 << 40, true},
		{" 3 Mi ", 3 << 20, true},
		{"0", 0, true},
		{"0Gi", 0, true},
		{"", 0, false},
		{"   ", 0, false},
		{"-1", 0, false},
		{"-1Ki", 0, false},
		{"1.5Gi", 0, false},
		{"1GB", 0, false},
		{"Gi", 0, false},
		{"9223372036854775807", math.MaxInt64, true},
		{"9223372036854775808", 0, false},
		{"8388607Ti", 8388607 << 40, true},
		{"8388608Ti", 0, false},
		{"9007199254740992Ki", 0, false},
	} {
		got, ok := ParseBytes(tc.in)
		require.Equal(t, tc.ok, ok, "%q", tc.in)
		require.Equal(t, tc.want, got, "%q", tc.in)
	}
}
