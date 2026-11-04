package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

func TestWorkloadBackupMaxListedKeyBytes(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]int64{"": 256 << 20, "1": 1, "512Mi": 512 << 20, "1GiB": 1 << 30} {
		var cfg config.Config
		cfg.WorkloadBackup.MaxListedKeySize = value
		got, err := cfg.WorkloadBackupMaxListedKeyBytes()
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
	for _, value := range []string{"lots", "0", "0MiB", "-1"} {
		var cfg config.Config
		cfg.WorkloadBackup.MaxListedKeySize = value
		_, err := cfg.WorkloadBackupMaxListedKeyBytes()
		require.Equal(t, fault.Invalid, fault.KindOf(err), value)
		require.ErrorContains(t, err, "workloadBackup.maxListedKeySize", value)
	}
}

// The keys load from the file and from their variables, with their defaults otherwise, and a value that bounds nothing fails
// the config's load, not the start after it.
func TestWorkloadBackupKeysLoad(t *testing.T) {
	cfg, err := config.Load(writeCfg(t, "server:\n  listenAddr: 127.0.0.1:8080\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 1<<20, cfg.WorkloadBackup.MaxListedKeys)
	require.Equal(t, "256MiB", cfg.WorkloadBackup.MaxListedKeySize)
	require.Equal(t, 1024, cfg.WorkloadBackup.MaxCopyMembers)

	cfg, err = config.Load(writeCfg(t, "workloadBackup:\n  maxListedKeys: 5\n  maxListedKeySize: 1MiB\n  maxCopyMembers: 7\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 5, cfg.WorkloadBackup.MaxListedKeys)
	require.Equal(t, "1MiB", cfg.WorkloadBackup.MaxListedKeySize)
	require.Equal(t, 7, cfg.WorkloadBackup.MaxCopyMembers)

	t.Setenv("FUNCD_WORKLOAD_BACKUP_MAX_LISTED_KEYS", "6")
	t.Setenv("FUNCD_WORKLOAD_BACKUP_MAX_LISTED_KEY_SIZE", "2MiB")
	t.Setenv("FUNCD_WORKLOAD_BACKUP_MAX_COPY_MEMBERS", "8")
	cfg, err = config.Load(writeCfg(t, "server:\n  listenAddr: 127.0.0.1:8080\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 6, cfg.WorkloadBackup.MaxListedKeys)
	require.Equal(t, "2MiB", cfg.WorkloadBackup.MaxListedKeySize)
	require.Equal(t, 8, cfg.WorkloadBackup.MaxCopyMembers)
}

func TestWorkloadBackupKeysRefuseAValueThatBoundsNothing(t *testing.T) {
	for _, c := range []struct{ yaml, key string }{
		{"workloadBackup:\n  maxListedKeys: 0\n", "workloadBackup.maxListedKeys"},
		{"workloadBackup:\n  maxListedKeys: -1\n", "workloadBackup.maxListedKeys"},
		{"workloadBackup:\n  maxCopyMembers: 0\n", "workloadBackup.maxCopyMembers"},
		{"workloadBackup:\n  maxCopyMembers: -1\n", "workloadBackup.maxCopyMembers"},
		{"workloadBackup:\n  maxListedKeySize: lots\n", "workloadBackup.maxListedKeySize"},
		{"workloadBackup:\n  maxListedKeySize: \"0\"\n", "workloadBackup.maxListedKeySize"},
	} {
		_, err := config.Load(writeCfg(t, c.yaml), config.Flags{})
		require.ErrorContains(t, err, c.key, c.yaml)
	}
}
