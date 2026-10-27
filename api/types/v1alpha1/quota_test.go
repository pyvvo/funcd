package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func TestSizeQuota(t *testing.T) {
	t.Parallel()
	for q, want := range map[SizeQuota]int64{
		{}:                  0,
		{MaxSize: "1Gi"}:    1 << 30,
		{MaxSize: "512MiB"}: 512 << 20,
		{MaxSize: "4096"}:   4096,
		{MaxBytes: 64}:      64,
	} {
		require.NoError(t, q.Validate("test", "spec.quota"), "%+v", q)
		n, ok := q.Limit()
		require.Equal(t, want, n, "%+v", q)
		require.Equal(t, want > 0, ok, "%+v", q)
	}
	for q, msg := range map[SizeQuota]string{
		{MaxSize: "1Gi", MaxBytes: 1}: "spec.quota: set maxSize or maxBytes, not both",
		{MaxSize: "1GB"}:              "spec.quota.maxSize",
		{MaxSize: "0"}:                "must be above zero",
		{MaxBytes: -1}:                "spec.quota.maxBytes (-1) must not be negative",
	} {
		err := q.Validate("test", "spec.quota")
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%+v", q)
		require.ErrorContains(t, err, msg)
	}
}
