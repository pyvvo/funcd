package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// scenario: eventstore-default-dir (ADR-0300) — with no event store key, the directory is <storage.dataDir>/eventstore.
func TestScenarioEventStoreDefaultDir(t *testing.T) {
	c, err := config.Load(writeCfg(t, "storage:\n  dataDir: /srv/funcd\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/srv/funcd/eventstore", c.Eventing.EventStore.DataDir)
}

// scenario: eventstore-key (ADR-0300) — eventing.eventstore.dataDir, or FUNCD_EVENTING_EVENTSTORE_DATA_DIR, sets it.
func TestScenarioEventStoreKey(t *testing.T) {
	c, err := config.Load(writeCfg(t, "eventing:\n  eventstore:\n    dataDir: /srv/events\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/srv/events", c.Eventing.EventStore.DataDir)

	t.Setenv("FUNCD_EVENTING_EVENTSTORE_DATA_DIR", "/srv/env-events")
	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/srv/env-events", c.Eventing.EventStore.DataDir)
}

// scenario: old-key-refused (ADR-0300) — eventing.deadletter.dataDir is no longer a key, so the file fails to load.
func TestScenarioOldEventStoreKeyRefused(t *testing.T) {
	_, err := config.Load(writeCfg(t, "eventing:\n  deadletter:\n    dataDir: /srv/dl\n"), config.Flags{})
	require.ErrorContains(t, err, `unknown field "dataDir"`)
}

// scenario: deadletter-keys-kept (ADR-0300) — the dead-letter limits keep their keys.
func TestScenarioDeadLetterKeysKept(t *testing.T) {
	c, err := config.Load(writeCfg(t, "eventing:\n  deadletter:\n    retention: 1h\n    maxEntries: 10\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "1h", c.Eventing.Deadletter.Retention)
	require.Equal(t, 10, c.Eventing.Deadletter.MaxEntries)
}
