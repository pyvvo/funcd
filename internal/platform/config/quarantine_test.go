package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// ADR-0288 Decision 4: storage.quarantineRetention is 168h when empty and an ADR-0194 duration of at least 1h; any
// other value stops the start naming the key.
func TestQuarantineRetention(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]time.Duration{"": 168 * time.Hour, "1h": time.Hour, "720h": 720 * time.Hour} {
		var c config.Config
		c.Storage.QuarantineRetention = value
		got, err := c.QuarantineRetention()
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
	for _, value := range []string{"59m", "7d", "soon", "0s"} {
		_, err := config.Load(writeCfg(t, "storage:\n  quarantineRetention: "+value+"\n"), config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), value)
		require.ErrorContains(t, err, `config key "storage.quarantineRetention"`, value)
	}
}
