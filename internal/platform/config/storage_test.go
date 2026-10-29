package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

func TestStorageMinFreeBytes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		value string
		want  int64
	}{
		{"", 2 << 30},
		{"0", 0},
		{"512Mi", 512 << 20},
		{"1GiB", 1 << 30},
	} {
		var cfg config.Config
		cfg.Storage.MinFreeSize = c.value
		got, err := cfg.StorageMinFreeBytes()
		require.NoError(t, err, c.value)
		require.Equal(t, c.want, got, c.value)
	}
	var cfg config.Config
	cfg.Storage.MinFreeSize = "lots"
	_, err := cfg.StorageMinFreeBytes()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "storage.minFreeSize")
}

// A storage.minFreeSize that does not parse fails the config's load (ADR-0296), not the start after it.
func TestCheckStorageRefusesABadFloor(t *testing.T) {
	_, err := config.Load(writeCfg(t, "storage:\n  minFreeSize: lots\n"), config.Flags{})
	require.ErrorContains(t, err, "storage.minFreeSize")
}
