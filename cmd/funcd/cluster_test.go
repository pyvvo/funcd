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

func TestAJoinedNodeRestoresFromItsOwnPrefix(t *testing.T) {
	dir := t.TempDir()
	cluster := filepath.Join(dir, "cluster")
	path := filepath.Join(dir, "funcdconfig.yaml")
	key := filepath.Join(dir, "secrets.key")
	require.NoError(t, os.WriteFile(key, make([]byte, 32), 0o600))
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: "+filepath.Join(dir, "data")+"\n"+
		"secrets:\n  encryptionKeyFile: "+key+"\n"+
		"backup:\n  target: file:///srv/backup\n  encryption:\n    none: true\ncluster:\n  dataDir: "+cluster+"\n"), 0o600))
	f := &restoreFlags{config: path}
	cfg, err := f.load()
	require.NoError(t, err)
	require.Equal(t, "file:///srv/backup", cfg.Backup.Target, "a funcd that never joined reads the target's root")
	require.NoError(t, os.MkdirAll(cluster, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cluster, "name"), []byte("vm1\n"), 0o600))
	cfg, err = f.load()
	require.NoError(t, err)
	require.Equal(t, "file:///srv/backup/nodes/vm1", cfg.Backup.Target)
}
