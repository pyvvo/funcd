package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
)

func TestAWorkerNodeKeepsItsSecretsEncryptedWithAKeyOfItsOwn(t *testing.T) {
	var cfg config.Config
	cfg.Cluster.DataDir = filepath.Join(t.TempDir(), "cluster")
	got, err := roleNode.secretsKey(cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.Cluster.DataDir, nodeSecretsKey), got.Secrets.EncryptionKeyFile)
	key, err := os.ReadFile(got.Secrets.EncryptionKeyFile)
	require.NoError(t, err)
	require.Len(t, key, 32, "an AES-256 key")
	info, err := os.Stat(got.Secrets.EncryptionKeyFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	again, err := roleNode.secretsKey(cfg)
	require.NoError(t, err)
	kept, err := os.ReadFile(again.Secrets.EncryptionKeyFile)
	require.NoError(t, err)
	require.Equal(t, key, kept, "a restart keeps the key the Secrets were written with")

	cfg.Secrets.EncryptionKeyFile = "/etc/funcd/secrets.key"
	named, err := roleNode.secretsKey(cfg)
	require.NoError(t, err)
	require.Equal(t, "/etc/funcd/secrets.key", named.Secrets.EncryptionKeyFile, "a key the config names wins")

	var all config.Config
	all.Cluster.DataDir = filepath.Join(t.TempDir(), "cluster")
	same, err := roleAll.secretsKey(all)
	require.NoError(t, err)
	require.Empty(t, same.Secrets.EncryptionKeyFile, "the all-in-one is unchanged")
	_, err = os.Stat(all.Cluster.DataDir)
	require.True(t, os.IsNotExist(err))
}

func TestAWorkerNodeBacksUpUnderItsOwnPrefix(t *testing.T) {
	require.Equal(t, "s3://backups?prefix=nodes%2Fvm1%2F&region=eu-west-1", nodeTarget("s3://backups?region=eu-west-1", "vm1"))
	require.Equal(t, "s3://backups?prefix=funcd%2Fnodes%2Fvm1%2F", nodeTarget("s3://backups?prefix=funcd", "vm1"))
	require.Equal(t, "file:///srv/backup/nodes/vm1", nodeTarget("file:///srv/backup", "vm1"))
	require.Equal(t, "ftp://nowhere", nodeTarget("ftp://nowhere", "vm1"), "backup.Open refuses another scheme")

	var cfg config.Config
	cfg.Cluster.DataDir = t.TempDir()
	cfg.Backup.Target = "file:///srv/backup"
	_, err := roleNode.nodeBackups(cfg)
	require.ErrorContains(t, err, "has not joined")
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cluster.DataDir, "name"), []byte("vm1\n"), 0o600))
	cfg.Kvstore.Backup.Target = "s3://kv"
	got, err := roleNode.nodeBackups(cfg)
	require.NoError(t, err)
	require.Equal(t, "file:///srv/backup/nodes/vm1", got.Backup.Target)
	require.Equal(t, "s3://kv?prefix=nodes%2Fvm1%2F", got.Kvstore.Backup.Target)

	same, err := roleServer.nodeBackups(cfg)
	require.NoError(t, err)
	require.Equal(t, cfg.Backup.Target, same.Backup.Target, "the control plane keeps the target's root")
}
