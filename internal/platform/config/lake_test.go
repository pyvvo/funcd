package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// ADR-0274 Decision 1: lake.saveInterval is 1s when empty and an ADR-0194 duration from 500ms to 1h, both included.
func TestLakeSaveInterval(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]time.Duration{
		"":        time.Second,
		"500ms":   500 * time.Millisecond,
		"1s500ms": 1500 * time.Millisecond,
		"1m30s":   90 * time.Second,
		"1h":      time.Hour,
	} {
		var c config.Config
		c.Lake.SaveInterval = value
		got, err := c.LakeSaveInterval()
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
}

// scenario: bad-save-interval-refused (ADR-0274) — a lake.saveInterval that is no duration, or outside 500ms to 1h,
// stops the start naming the key.
func TestScenarioBadSaveIntervalRefused(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"400ms", "1h1s", "1.5s", "5", "soon", "30s1m"} {
		_, err := config.Load(writeCfg(t, "lake:\n  saveInterval: "+value+"\n"), config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), value)
		require.ErrorContains(t, err, `config key "lake.saveInterval"`, value)
	}
	c, err := config.Load(writeCfg(t, "lake:\n  saveInterval: 5s\n"), config.Flags{})
	require.NoError(t, err)
	d, err := c.LakeSaveInterval()
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, d)
}
