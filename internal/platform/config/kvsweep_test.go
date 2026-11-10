package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// kvstore.sweepInterval is 1s when empty and an ADR-0194 duration from 100ms (ADR-0320 Decision 5); a shorter one, or
// one that is no duration, is refused naming the key.
func TestKVSweepInterval(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]time.Duration{"": time.Second, "100ms": 100 * time.Millisecond, "2s": 2 * time.Second} {
		var c config.Config
		c.Kvstore.SweepInterval = value
		got, err := c.KVSweepInterval()
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
	for _, value := range []string{"99ms", "0s", "soon"} {
		var c config.Config
		c.Kvstore.SweepInterval = value
		_, err := c.KVSweepInterval()
		require.Equal(t, fault.Invalid, fault.KindOf(err), value)
		require.ErrorContains(t, err, `config key "kvstore.sweepInterval"`, value)
	}
}
