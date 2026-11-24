package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// ADR-0347 Contracts: job.maxRunning 4 (at least 1), job.payloadLimit 262144 bytes (at least 1), job.retention 720h (a
// positive duration).
func TestJobKeys(t *testing.T) {
	t.Parallel()
	c, err := config.Load(writeCfg(t, "storage:\n  mode: memory\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 4, c.Job.MaxRunning)
	require.Equal(t, int64(262144), c.Job.PayloadLimit)
	retention, err := c.JobRetention()
	require.NoError(t, err)
	require.Equal(t, 720*time.Hour, retention)

	c, err = config.Load(writeCfg(t, "job:\n  maxRunning: 1\n  payloadLimit: 1\n  retention: 1ms\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 1, c.Job.MaxRunning)
	require.Equal(t, int64(1), c.Job.PayloadLimit)
	retention, err = c.JobRetention()
	require.NoError(t, err)
	require.Equal(t, time.Millisecond, retention)

	var empty config.Config
	retention, err = empty.JobRetention()
	require.NoError(t, err)
	require.Equal(t, config.DefaultJobRetention, retention, "an empty job.retention is its default")

	for body, key := range map[string]string{
		"job:\n  maxRunning: 0\n":     "job.maxRunning",
		"job:\n  maxRunning: -2\n":    "job.maxRunning",
		"job:\n  payloadLimit: 0\n":   "job.payloadLimit",
		"job:\n  retention: 0s\n":     "job.retention",
		"job:\n  retention: soon\n":   "job.retention",
		"job:\n  retention: 7d\n":     "job.retention",
		"job:\n  retention: 1500us\n": "job.retention",
	} {
		_, err := config.Load(writeCfg(t, body), config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), body)
		require.ErrorContains(t, err, `config key "`+key+`"`, body)
	}
}
