package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// recorder keeps the errors Recorder("kv") would get.
type recorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *recorder) record(_ time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recorder) last() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) == 0 {
		return nil
	}
	return r.errs[len(r.errs)-1]
}

// syncBuffer is a log buffer the export's goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// kvYAML writes a file-mode config with the Badger KV and yaml, and loads it.
func kvYAML(t *testing.T, yaml string) (config.Config, string) {
	t.Helper()
	dataDir := shortDataDir(t)
	path := filepath.Join(dataDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\nkvstore:\n  engine: badger\n"+yaml), 0o600))
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	return cfg, path
}

// identities returns two age identities and a recipients file of both.
func identities(t *testing.T) ([]age.Identity, string) {
	t.Helper()
	var rs bytes.Buffer
	var ids []age.Identity
	for range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		rs.WriteString(id.Recipient().String() + "\n")
		ids = append(ids, id)
	}
	path := filepath.Join(t.TempDir(), "recipients.txt")
	require.NoError(t, os.WriteFile(path, rs.Bytes(), 0o600))
	return ids, path
}

// runKV builds the KV store with deps, starts its loops, and stops and closes them at cleanup.
func runKV(t *testing.T, cfg config.Config, log *slog.Logger, deps kvBackupDeps) kvstore.KV {
	t.Helper()
	kv, start, closeKV, err := buildKVStore(context.Background(), cfg, nil, log, deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	start(ctx)
	t.Cleanup(func() {
		cancel()
		require.NoError(t, closeKV())
		require.NoError(t, kv.(io.Closer).Close())
	})
	return kv
}

// scenario: inherits-platform-target — backup.target T, backup.credentialsFile C and two recipients, with only
// kvstore.backup.enabled set: the KV's objects are under T/kv/, signed with C's key, and open with either identity;
// with kvstore.backup.target K added, it writes to K and never signs with C.
func TestScenarioInheritsPlatformTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ids, recipients := identities(t)
	platform := s3stub.New(t, nil)
	creds := s3stub.CredentialsFile(t, "AKIDPLATFORM")
	key := filepath.Join(t.TempDir(), "secrets.key")
	require.NoError(t, os.WriteFile(key, bytes.Repeat([]byte{3}, 32), 0o600))
	base := "secrets:\n  encryptionKeyFile: \"" + key + "\"\nbackup:\n  target: \"" + platform.URL() + "\"\n  credentialsFile: \"" + creds +
		"\"\n  encryption:\n    recipients:\n      - \"" + recipients + "\"\n"
	start := func(t *testing.T, cfg config.Config) {
		t.Helper()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		sealer, err := envelope.FromConfig(cfg, bytes.Repeat([]byte{7}, 32), log)
		require.NoError(t, err)
		runs, target, err := backupRunner(ctx, cfg, sealer, backupMeter(nil), log, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = target.Close() })
		kv := runKV(t, cfg, log, kvBackupDeps{target: target, record: runs.Recorder("kv")})
		require.NoError(t, kv.Put(ctx, "ns/s/k", []byte("v")))
	}

	cfg, _ := kvYAML(t, "  backup:\n    enabled: true\n    interval: 20ms\n"+base)
	start(t, cfg)
	require.Eventually(t, func() bool { return len(platform.Keys(kvbadger.KVPrefix+"base/")) > 0 && hasManifest(platform) },
		10*time.Second, 5*time.Millisecond)
	requests, auth := platform.Seen()
	for i, r := range requests {
		if strings.HasPrefix(r, "PUT kv/") {
			require.Contains(t, auth[i], "AKIDPLATFORM", "%s is not signed with backup.credentialsFile", r)
		}
	}
	src, err := gocloud.OpenWith(ctx, platform.URL(), gocloud.OpenOptions{CredentialsFile: creds})
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	points, err := kvbadger.ListPoints(ctx, src)
	require.NoError(t, err)
	require.NotEmpty(t, points)
	for _, id := range ids {
		require.NoError(t, kvbadger.RestoreDir(ctx, t.TempDir(), src, points[0], envelope.Opener([]age.Identity{id})))
	}

	own := s3stub.New(t, nil)
	kvCreds := s3stub.CredentialsFile(t, "AKIDKV")
	cfg, _ = kvYAML(t, "  backup:\n    enabled: true\n    interval: 20ms\n    target: \""+own.URL()+"\"\n    credentialsFile: \""+
		kvCreds+"\"\n"+base)
	start(t, cfg)
	require.Eventually(t, func() bool { return hasManifest(own) }, 10*time.Second, 5*time.Millisecond)
	_, auth = own.Seen()
	for _, a := range auth {
		require.NotContains(t, a, "AKIDPLATFORM", "a request to K is signed with backup.credentialsFile")
	}
	before := len(platform.Keys(kvbadger.KVPrefix))
	require.Never(t, func() bool { return len(platform.Keys(kvbadger.KVPrefix)) != before }, 100*time.Millisecond, 10*time.Millisecond,
		"the KV with its own target wrote to backup.target")
}

func hasManifest(s *s3stub.Stub) bool {
	for _, k := range s.Keys(kvbadger.KVPrefix) {
		if strings.HasSuffix(k, "/manifest.yaml") {
			return true
		}
	}
	return false
}

// scenario: kv-keys-checked — with kvstore.backup.enabled, no target in either block, a KV credentialsFile without
// its target, a KV target equal to backup.target, no recipients and no none in either block, chunkBytes above 8 MiB,
// or rebaseline 48h with retention.hourly 48 refuses the daemon's start with fault.Invalid naming the key; KV
// encryption.none without a secrets key starts and warns.
func TestScenarioKVKeysChecked(t *testing.T) {
	t.Parallel()
	for key, yaml := range map[string]string{
		"kvstore.backup.target":                      "  backup:\n    enabled: true\n    encryption:\n      none: true\n",
		"kvstore.backup.credentialsFile":             "  backup:\n    enabled: true\n    credentialsFile: /etc/kv\n    encryption:\n      none: true\nbackup:\n  target: s3://b\n  encryption:\n    none: true\nsecrets:\n  encryptionKeyFile: /k\n",
		"kvstore.backup.encryption.recipients":       "  backup:\n    enabled: true\n    target: file:///srv/kv\n",
		"kvstore.backup.chunkBytes":                  "  backup:\n    enabled: true\n    target: file:///srv/kv\n    chunkBytes: 67108864\n    encryption:\n      none: true\n",
		"kvstore.backup.retention.hourly":            "  backup:\n    enabled: true\n    target: file:///srv/kv\n    rebaseline: 48h\n    encryption:\n      none: true\n",
		"kvstore.backup.target equals backup.target": "  backup:\n    enabled: true\n    target: s3://b\n    encryption:\n      none: true\nbackup:\n  target: s3://b\n  encryption:\n    none: true\nsecrets:\n  encryptionKeyFile: /k\n",
	} {
		dataDir := shortDataDir(t)
		path := filepath.Join(dataDir, "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\nkvstore:\n  engine: badger\n"+yaml), 0o600))
		err := serve(context.Background(), path, nil, io.Discard)
		require.Equal(t, fault.Invalid, fault.KindOf(err), key)
		require.ErrorContains(t, err, strings.TrimSuffix(key, " equals backup.target"), key)
	}

	cfg, _ := kvYAML(t, "  backup:\n    enabled: true\n    target: \""+gocloud.FileURL(t.TempDir())+"\"\n    encryption:\n      none: true\n")
	require.Empty(t, cfg.Secrets.EncryptionKeyFile)
	var logs syncBuffer
	runKV(t, cfg, slog.New(slog.NewTextHandler(&logs, nil)), kvBackupDeps{})
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "every record leaves in plaintext")
	require.Contains(t, logs.String(), "key=kvstore.backup.encryption.none")
}

// scenario: kv-probe-failure-stops-backup — kvstore.backup.target ignoring IfNotExist: the backup writes nothing, its
// log and Record get the cause naming kvstore.backup.singleWriter, and the KV serves.
func TestScenarioKVProbeFailureStopsBackup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, nil)
	s.Set(func(s *s3stub.Stub) { s.Cond = s3stub.Ignored })
	cfg, _ := kvYAML(t, "  backup:\n    enabled: true\n    interval: 20ms\n    target: \""+s.URL()+"\"\n    credentialsFile: \""+
		s3stub.CredentialsFile(t, "AKIDKV")+"\"\n    encryption:\n      none: true\n")
	var logs syncBuffer
	rec := &recorder{}
	kv := runKV(t, cfg, slog.New(slog.NewTextHandler(&logs, nil)), kvBackupDeps{record: rec.record})
	require.Eventually(t, func() bool { return rec.last() != nil }, 10*time.Second, 5*time.Millisecond)
	require.ErrorContains(t, rec.last(), "kvstore.backup.singleWriter")
	require.Contains(t, logs.String(), "kvstore.backup.singleWriter")
	require.Empty(t, s.Keys(kvbadger.KVPrefix), "the backup wrote under kv/")
	require.NoError(t, kv.Put(ctx, "ns/s/k", []byte("v")))
	v, found, err := kv.Get(ctx, "ns/s/k")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("v"), v)
}

// The daemon wires the KV export with its hold (ADR-0206 Decision 6) on the inherited backup.target: held, no base
// is written and nothing recorded; released, the next check writes one.
func TestKVBackupAssembledWithHold(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	cfg, err := config.Load(backupConfig(t, "file", "kvstore:\n  engine: badger\n  backup:\n    enabled: true\n    interval: 20ms\n"+
		"backup:\n  target: \""+gocloud.FileURL(target)+"\"\n  encryption:\n    none: true\n"), config.Flags{})
	require.NoError(t, err)
	require.NoError(t, hold.Write(cfg.Storage.DataDir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	held, err := hold.Open(cfg.Storage.DataDir)
	require.NoError(t, err)

	var logs bytes.Buffer
	_, closeExec, start, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), held)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	start(ctx)
	t.Cleanup(func() { cancel(); _ = closeExec() })
	require.Contains(t, logs.String(), "kv/inc/:2")
	bases := filepath.Join(target, "kv", "base")
	require.Never(t, func() bool { _, err := os.Stat(bases); return err == nil }, 300*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, held.Release(time.Now()))
	require.Eventually(t, func() bool { _, err := os.Stat(bases); return err == nil }, 10*time.Second, 20*time.Millisecond)
}

// kvChain writes chain 1 to the directory target: a base holding victim and keep, then segments 1 to 5, each with a
// key s<i>, victim deleted in 3.
func kvChain(t *testing.T, target string) {
	t.Helper()
	ctx := context.Background()
	b, err := gocloud.Open(ctx, gocloud.FileURL(target))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	kv, seams, err := kvbadger.OpenWithSeamsFor(t.TempDir(), b, kvbadger.BackupConfig{}, nil, kvbadger.CDCConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.(io.Closer).Close() })
	require.NoError(t, kv.Put(ctx, "ns/s/victim", []byte("v")))
	require.NoError(t, kv.Put(ctx, "ns/s/keep", []byte("k")))
	_, err = seams.Backup.Ship(ctx)
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		require.NoError(t, kv.Put(ctx, fmt.Sprintf("ns/s/s%d", i), []byte("v")))
		if i == 3 {
			require.NoError(t, kv.Delete(ctx, "ns/s/victim"))
		}
		_, err = seams.Backup.Ship(ctx)
		require.NoError(t, err)
	}
}

// restoredKV opens the KV a restore wrote and reports whether it holds key.
func restoredKV(t *testing.T, cfg config.Config, key string) bool {
	t.Helper()
	kv, err := kvbadger.Open(cfg.Kvstore.DataDir)
	require.NoError(t, err)
	defer func() { _ = kv.(io.Closer).Close() }()
	_, found, err := kv.Get(context.Background(), key)
	require.NoError(t, err)
	return found
}

// scenario: restore-any-point — chain n with segments 1 to 5 and victim deleted in 3: `funcd restore kv n/2` and
// `n/5` into empty kvstore.dataDirs hold and lack victim, each leaving the platform held; a non-empty one is
// fault.Conflict; once base n expires both are listed not restorable and a restore is fault.NotFound, writing nothing.
func TestScenarioRestoreAnyPoint(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	kvChain(t, target)
	kvYAMLFor := "  backup:\n    enabled: true\n    target: \"" + gocloud.FileURL(target) + "\"\n    encryption:\n      none: true\n"
	two, twoPath := kvYAML(t, kvYAMLFor)
	five, fivePath := kvYAML(t, kvYAMLFor)

	out, err := runRestore(t, "list", "--config", twoPath, "--from", gocloud.FileURL(target))
	require.NoError(t, err)
	require.Contains(t, out, "KV POINT")
	require.Regexp(t, `\n1/5 +\S+ +\S+ +\S+ +restorable\n`, out)

	_, err = runRestore(t, "kv", "--config", twoPath, "1/2")
	require.NoError(t, err)
	require.True(t, restoredKV(t, two, "ns/s/victim"), "(n, 2) lacks victim")
	require.FileExists(t, filepath.Join(two.Storage.DataDir, hold.MarkerFile), "the next start is not held")
	require.NoFileExists(t, filepath.Join(two.Storage.DataDir, hold.BusyFile))
	out, err = runRestore(t, "kv", "--config", fivePath, "1/5")
	require.NoError(t, err)
	require.Contains(t, out, "restored KV point 1/5")
	require.False(t, restoredKV(t, five, "ns/s/victim"), "(n, 5) holds victim")
	require.True(t, restoredKV(t, five, "ns/s/s5"))

	_, err = runRestore(t, "kv", "--config", twoPath, "1/2")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty kvstore.dataDir")

	require.NoError(t, os.RemoveAll(filepath.Join(target, "kv", "base")))
	out, err = runRestore(t, "list", "--config", twoPath, "--from", gocloud.FileURL(target))
	require.NoError(t, err)
	require.Regexp(t, `\n1/2 +- +\S+ +\S+ +not restorable\n`, out)
	require.Regexp(t, `\n1/5 +- +\S+ +\S+ +not restorable\n`, out)
	empty, emptyPath := kvYAML(t, kvYAMLFor)
	_, err = runRestore(t, "kv", "--config", emptyPath, "1/2")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.NoDirExists(t, empty.Kvstore.DataDir)
	require.NoFileExists(t, filepath.Join(empty.Storage.DataDir, hold.MarkerFile))
}

// scenario: newer-chain-refused (the command) — a point written by funcd 0.(m+1).0 lists as newer to funcd 0.m.x,
// and `restore kv` refuses it with fault.Invalid naming both versions, writing nothing. It sets the process-wide
// version.Version, so it stays serial.
func TestScenarioNewerChainRefusedCommand(t *testing.T) {
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "v0.9.0"
	target := t.TempDir()
	kvChain(t, target)
	version.Version = "v0.8.1"
	cfg, path := kvYAML(t, "  backup:\n    enabled: true\n    target: \""+gocloud.FileURL(target)+"\"\n    encryption:\n      none: true\n")
	out, err := runRestore(t, "list", "--config", path, "--from", gocloud.FileURL(target))
	require.NoError(t, err)
	require.Regexp(t, `\n1/5 +\S+ +\S+ +v0\.9\.0 +newer\n`, out)
	_, err = runRestore(t, "kv", "--config", path, "1/5")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "v0.9.0")
	require.ErrorContains(t, err, "v0.8.1")
	require.NoDirExists(t, cfg.Kvstore.DataDir)
	out, err = runRestore(t, "inspect", "--config", path, "--from", gocloud.FileURL(target), "--kv", "1/5", "-o", "json")
	require.NoError(t, err)
	require.Contains(t, out, `"funcd": "v0.9.0"`)
	require.Contains(t, out, "newer than this funcd v0.8.1")
}
