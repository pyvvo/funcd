package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
)

// Each role's parts (ADR-0363 Decisions 1 to 4): the address funcd server listens on for nodes when its config sets none,
// whether a role serves the node API, and so keeps the change log, whether it runs workers, its platform option, and
// whether its control plane runs alone.
func TestEachRoleRunsItsOwnParts(t *testing.T) {
	listening := config.Config{}
	listening.Cluster.ListenAddr = "127.0.0.1:7443"
	for _, tc := range []struct {
		name                           string
		r                              role
		cfg                            config.Config
		addr                           string
		serves, workers, option, alone bool
	}{
		{"all-in-one", roleAll, config.Config{}, "", false, true, false, false},
		{"all-in-one with nodes", roleAll, listening, "127.0.0.1:7443", true, true, true, false},
		{"control plane alone", roleServer, config.Config{}, defaultClusterAddr, true, false, true, true},
		{"worker node", roleNode, config.Config{}, "", false, true, true, false},
	} {
		cfg := tc.r.prepare(tc.cfg)
		require.Equal(t, tc.addr, cfg.Cluster.ListenAddr, tc.name)
		require.Equal(t, tc.serves, tc.r.servesNodes(cfg), tc.name)
		require.Equal(t, tc.workers, tc.r.runsWorkers(), tc.name)
		require.Equal(t, tc.option, tc.r.option(cfg) != nil, tc.name)
		require.Equal(t, tc.alone, tc.r.clusterOptions(cfg).Alone, tc.name)
	}
	require.Equal(t, "0.0.0.0:7443", defaultClusterAddr)
}

// funcd server runs no namespace, so it takes no container runtime and no shim whatever runtime.mode says, and starts
// on a machine with no containerd (ADR-0363 Decision 2).
func TestAControlPlaneAloneTakesNoContainerRuntime(t *testing.T) {
	cfg := config.Config{}
	cfg.Runtime.Mode = "containerd"
	cfg.Storage.DataDir = t.TempDir()
	opts, closeRuntime, err := roleServer.executionOptions(context.Background(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	defer func() { _ = closeRuntime() }()
	require.Len(t, opts, 1, "one runtime that starts no worker, and no shim")
}

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

// The pre-upgrade generation of a joined node goes under the node's own prefix too, not at the root of the target, which is
// the control plane's.
func TestAJoinedNodesUpgradeWritesUnderItsOwnPrefix(t *testing.T) {
	target := t.TempDir()
	n := newNode(t, upgradeKey(), backupYAML(target))
	cfg, err := config.Load(n.cfgPath, config.Flags{})
	require.NoError(t, err)
	eng, err := badgerstore.Open(cfg.Storage.MetastoreDir)
	require.NoError(t, err)
	st := store.New(eng)
	_, err = st.Create(context.Background(), &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "team"}})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	require.NoError(t, os.MkdirAll(cfg.Cluster.DataDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cluster.DataDir, "name"), []byte("vm1\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(target, "nodes", "vm1"), 0o700)) // the node made it at its first start
	var out bytes.Buffer
	require.NoError(t, runUpgrade(context.Background(), &out, upgradeFlags{config: n.cfgPath}, fakeFuncd(t, "v0.9.0"),
		fakeFuncd(t, "dev")), out.String())
	require.DirExists(t, filepath.Join(target, "nodes", "vm1", "gen", "pre-upgrade"))
	require.NoDirExists(t, filepath.Join(target, "gen"))
}
