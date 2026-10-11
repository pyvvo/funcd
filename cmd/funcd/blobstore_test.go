package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// blobConfig loads a file-mode config with a short data dir and yaml.
func blobConfig(t *testing.T, yaml string) config.Config {
	t.Helper()
	dataDir := shortDataDir(t)
	path := filepath.Join(dataDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\n"+yaml), 0o600))
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	return cfg
}

// remoteYAML is blob.target on the stub with a credentials file.
func remoteYAML(t *testing.T, s *s3stub.Stub) string {
	t.Helper()
	return "blob:\n  target: \"" + s.URL() + "\"\n  credentialsFile: \"" + s3stub.CredentialsFile(t, "AKIDSTORE") + "\"\n"
}

// openSubstrate runs substrateOptions and closes what it opened.
func openSubstrate(t *testing.T, cfg config.Config) (blob.Bucket, string, error) {
	t.Helper()
	var logs bytes.Buffer
	_, _, bucket, theBus, err := substrateOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	if err == nil {
		t.Cleanup(func() { _ = bucket.Close(); _ = theBus.Close() })
	}
	return bucket, logs.String(), err
}

// A remote store serves through blob.NoSign and leaves no <dataDir>/blob (the cmd/funcd half of scenario
// remote-store-serves; pkg/funcd's TestScenarioRemoteStoreServes puts through context.blob).
func TestRemoteStoreOpensNoLocalDir(t *testing.T) {
	t.Parallel()
	s := s3stub.New(t, nil)
	s.Set(func(s *s3stub.Stub) { s.Versioning, s.ObjectLock = "Enabled", true })
	cfg := blobConfig(t, remoteYAML(t, s))
	require.Empty(t, cfg.Blob.Dir)
	b, logs, err := openSubstrate(t, cfg)
	require.NoError(t, err)
	require.NotContains(t, logs, "level=WARN")
	require.NoError(t, b.Put(context.Background(), "s3/ns/b/k", []byte("v"), blob.PutOptions{}))
	require.Equal(t, []string{"s3/ns/b/k"}, s.Keys("s3/"))
	_, err = b.SignedURL(context.Background(), "s3/ns/b/k", blob.SignOptions{})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.NoDirExists(t, filepath.Join(cfg.Storage.DataDir, "blob"))
}

// scenario: store-protection-checked — versioning off, suspended or unreadable refuses the start naming
// blob.allowUnversioned (with it true: serves and warns); versioning on without Object Lock serves with one warning
// naming Object Lock; no answer refuses it with fault.Unavailable.
func TestScenarioStoreProtectionChecked(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		set   func(s *s3stub.Stub)
		allow bool
		kind  fault.Kind
		key   string
		warn  string
	}{
		"off":                  {set: func(*s3stub.Stub) {}, kind: fault.Invalid, key: "blob.allowUnversioned"},
		"suspended":            {set: func(s *s3stub.Stub) { s.Versioning = "Suspended" }, kind: fault.Invalid, key: "blob.allowUnversioned"},
		"refused":              {set: func(s *s3stub.Stub) { s.VersioningStatus = 403 }, kind: fault.Invalid, key: "blob.allowUnversioned"},
		"unimplemented":        {set: func(s *s3stub.Stub) { s.VersioningStatus = 501 }, kind: fault.Invalid, key: "blob.allowUnversioned"},
		"no bucket":            {set: func(s *s3stub.Stub) { s.VersioningStatus = 404 }, kind: fault.Invalid, key: "blob.target"},
		"dropped":              {set: func(s *s3stub.Stub) { s.VersioningStatus = s3stub.Drop }, kind: fault.Unavailable, key: "blob.target"},
		"off, allowed":         {set: func(*s3stub.Stub) {}, allow: true, warn: "key=blob.allowUnversioned"},
		"on, no lock":          {set: func(s *s3stub.Stub) { s.Versioning = "Enabled" }, warn: "Object Lock"},
		"on, lock read denied": {set: func(s *s3stub.Stub) { s.Versioning, s.LockStatus = "Enabled", 403 }, warn: "Object Lock"},
		"on, locked":           {set: func(s *s3stub.Stub) { s.Versioning, s.ObjectLock = "Enabled", true }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := s3stub.New(t, nil)
			s.Set(c.set)
			yaml := remoteYAML(t, s)
			if c.allow {
				yaml += "  allowUnversioned: true\n"
			}
			_, logs, err := openSubstrate(t, blobConfig(t, yaml))
			if c.kind != "" {
				require.Equal(t, c.kind, fault.KindOf(err), "%v", err)
				require.ErrorContains(t, err, c.key)
				return
			}
			require.NoError(t, err)
			if c.warn == "" {
				require.NotContains(t, logs, "level=WARN")
				return
			}
			require.Equal(t, 1, strings.Count(logs, "level=WARN"), logs)
			require.Contains(t, logs, c.warn)
		})
	}
}

// scenario: blob-keys-checked — blob.target of another scheme, target with dir, credentialsFile alone, or a dir at
// or above storage.dataDir refuses the daemon's start with fault.Invalid naming the key; under storage.mode memory
// every blob key is ignored with one warning.
func TestScenarioBlobKeysChecked(t *testing.T) {
	t.Parallel()
	for key, yaml := range map[string]string{
		"blob.target":          "blob:\n  target: file:///srv/blob\n",
		"blob.dir":             "blob:\n  target: \"s3://b?region=r\"\n  dir: /srv/blob\n",
		"blob.credentialsFile": "blob:\n  credentialsFile: /etc/funcd/blob\n",
	} {
		dataDir := shortDataDir(t)
		path := filepath.Join(dataDir, "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\n"+yaml), 0o600))
		err := serve(context.Background(), path, nil, io.Discard)
		require.Equal(t, fault.Invalid, fault.KindOf(err), key)
		require.ErrorContains(t, err, key)
	}
	dataDir := shortDataDir(t)
	path := filepath.Join(dataDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+filepath.Join(dataDir, "d")+"\"\nblob:\n  dir: \""+dataDir+"\"\n"), 0o600))
	require.ErrorContains(t, serve(context.Background(), path, nil, io.Discard), "blob.dir")

	memDir := shortDataDir(t)
	memPath := filepath.Join(memDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(memPath, []byte("storage:\n  mode: memory\n  dataDir: \""+memDir+"\"\n"+
		"blob:\n  target: file:///srv/blob\n  allowUnversioned: true\n"), 0o600))
	cfg, err := config.Load(memPath, config.Flags{})
	require.NoError(t, err)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	logBackupFindings(cfg, log)
	require.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), logs.String())
	require.Contains(t, logs.String(), "blob.target, blob.allowUnversioned ignored")
	_, label, b, theBus, err := substrateOptions(context.Background(), cfg, log)
	require.NoError(t, err)
	require.Equal(t, "memory", label)
	require.NoError(t, b.Close())
	require.NoError(t, theBus.Close())
}

// scenario: target-inside-store-refused — with the store on bucket B, backup.target or kvstore.backup.target on B
// at the same endpoint, or a directory inside blob.dir, refuses the start naming the key; another bucket on the
// same endpoint, or a directory on blob.dir's device, starts with a warning.
func TestScenarioTargetInsideStoreRefused(t *testing.T) {
	t.Parallel()
	s := s3stub.New(t, nil)
	s.Set(func(s *s3stub.Stub) { s.Versioning, s.ObjectLock = "Enabled", true })
	other := "s3://other?region=us-east-1&use_path_style=true&endpoint=" + s.Endpoint()
	remote := func(t *testing.T, mod func(c *config.Config)) (string, error) {
		cfg := blobConfig(t, remoteYAML(t, s))
		mod(&cfg)
		_, logs, err := openSubstrate(t, cfg)
		return logs, err
	}
	_, err := remote(t, func(c *config.Config) { c.Backup.Target = s.URL("prefix=backup/") })
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "backup.target")
	_, err = remote(t, func(c *config.Config) { c.Kvstore.Backup.Enabled, c.Kvstore.Backup.Target = true, s.URL() })
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "kvstore.backup.target")
	logs, err := remote(t, func(c *config.Config) { c.Backup.Target = other })
	require.NoError(t, err)
	require.Contains(t, logs, "shares the blob store's provider")
	require.Contains(t, logs, "key=backup.target")

	local := func(t *testing.T, target func(blobDir string) string) (string, error) {
		cfg := blobConfig(t, "")
		cfg.Backup.Target = target(cfg.Blob.Dir)
		_, logs, err := openSubstrate(t, cfg)
		return logs, err
	}
	_, err = local(t, func(dir string) string { return gocloud.FileURL(filepath.Join(dir, "backup")) })
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "backup.target")
	_, err = local(t, func(dir string) string {
		link := filepath.Join(t.TempDir(), "link")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Symlink(dir, link))
		return gocloud.FileURL(filepath.Join(link, "x"))
	})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a symlink into blob.dir")
	logs, err = local(t, func(dir string) string { return gocloud.FileURL(filepath.Join(filepath.Dir(dir), "backup")) })
	require.NoError(t, err)
	require.Contains(t, logs, "shares the blob store's provider")
}

// mirrorConfig is a file-mode config whose platform backup goes unsealed to a directory, the mirror due every 100ms.
func mirrorConfig(t *testing.T, target string) config.Config {
	t.Helper()
	cfg, err := config.Load(backupConfig(t, "file", "backup:\n  target: \""+gocloud.FileURL(target)+"\"\n  encryption:\n"+
		"    none: true\nblob:\n  backup:\n    interval: 100ms\n"), config.Flags{})
	require.NoError(t, err)
	return cfg
}

// The daemon wires the mirror with its hold (ADR-0206 Decision 6): held, no generation is written; released, the
// mirror's next check runs one.
func TestBlobMirrorAssembledWithHold(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	cfg := mirrorConfig(t, target)
	store, err := gocloud.Open(context.Background(), gocloud.FileURL(mustDir(t, cfg.Blob.Dir)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Put(context.Background(), "s3/ns/b/k", []byte("v"), blob.PutOptions{}))
	require.NoError(t, hold.Write(cfg.Storage.DataDir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	held, err := hold.Open(cfg.Storage.DataDir)
	require.NoError(t, err)

	var logs bytes.Buffer
	_, closeExec, start, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(&logs, nil)), held)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	start(ctx)
	t.Cleanup(func() { cancel(); _ = closeExec() })
	require.Contains(t, logs.String(), "prefix=blob/ days=60")
	manifest := filepath.Join(target, "blob", "0000000001", "gen", "0000000001", "manifest.yaml")
	require.Never(t, func() bool { _, err := os.Stat(manifest); return err == nil }, 400*time.Millisecond, 20*time.Millisecond)
	require.NoError(t, held.Release(time.Now()))
	require.Eventually(t, func() bool { _, err := os.Stat(manifest); return err == nil }, 10*time.Second, 20*time.Millisecond)
}

func mustDir(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	return dir
}

// runRestore runs `funcd restore <args>` and returns its output.
func runRestore(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs(append([]string{"restore"}, args...))
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

// `funcd restore blob` puts the newest mirror generation into the empty blob.dir and leaves the platform held (DR
// answer Q15); a non-empty store is refused; `restore list` shows the blob rows; --at needs a versioned store.
func TestRestoreBlobCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target := t.TempDir()
	path := backupConfig(t, "file", "backup:\n  target: \""+gocloud.FileURL(target)+"\"\n  encryption:\n    none: true\n")
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)

	src := mustDir(t, filepath.Join(t.TempDir(), "blob"))
	from, err := gocloud.Open(ctx, gocloud.FileURL(src))
	require.NoError(t, err)
	t.Cleanup(func() { _ = from.Close() })
	require.NoError(t, from.Put(ctx, "s3/ns/b/k", []byte("age-encryption.org/v1\nuser bytes"), blob.PutOptions{ContentType: "text/plain"}))
	tb, err := gocloud.Open(ctx, gocloud.FileURL(target))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tb.Close() })
	m, err := blobmirror.New(blobmirror.Config{Dir: src, Target: tb, Interval: time.Hour, Rebaseline: time.Hour,
		Retention: time.Hour, RetryInterval: time.Minute})
	require.NoError(t, err)
	_, err = m.Run(ctx)
	require.NoError(t, err)

	out, err := runRestore(t, "list", "--config", path)
	require.NoError(t, err)
	require.Contains(t, out, "BLOB GENERATION")
	require.Regexp(t, `\n1 +1 +\S+ +complete\n`, out)

	_, err = runRestore(t, "blob", "--config", path, "--at", "2026-10-05T10:00:00Z")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "the local store keeps no versions")

	out, err = runRestore(t, "blob", "--config", path)
	require.NoError(t, err)
	require.Contains(t, out, "restored blob generation 1 (1 objects)")
	data, err := os.ReadFile(filepath.Join(cfg.Blob.Dir, "s3", "ns", "b", "k"))
	require.NoError(t, err)
	require.Equal(t, "age-encryption.org/v1\nuser bytes", string(data), "unsealed user bytes come back as is")
	require.FileExists(t, filepath.Join(cfg.Storage.DataDir, hold.MarkerFile), "the next start is held")
	require.NoFileExists(t, filepath.Join(cfg.Storage.DataDir, hold.BusyFile))

	_, err = runRestore(t, "blob", "--config", path, "1")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty store")
	require.NoFileExists(t, filepath.Join(cfg.Storage.DataDir, hold.BusyFile))
}

// `funcd restore blob --at` restores a versioned blob.target with --store-credentials-file and blob.versionRetention,
// and holds the platform; without blob.versionRetention it refuses and writes no marker.
func TestRestoreBlobAtCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, clock.NewManual(time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)))
	s.Set(func(s *s3stub.Stub) { s.Versioning = "Enabled" })
	creds := s3stub.CredentialsFile(t, "AKIDOPERATOR")
	store, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{CredentialsFile: creds})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Put(ctx, "k", []byte("then"), blob.PutOptions{}))
	s.Clock.Advance(time.Minute)
	at := s.Clock.Now().Format(time.RFC3339Nano)
	s.Clock.Advance(time.Minute)
	require.NoError(t, store.Put(ctx, "k", []byte("now"), blob.PutOptions{}))
	require.NoError(t, store.Put(ctx, "new", []byte("x"), blob.PutOptions{}))

	write := func(yaml string) (string, string) {
		dataDir := shortDataDir(t)
		path := filepath.Join(dataDir, "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dataDir+"\"\nblob:\n  target: \""+s.URL()+
			"\"\n  credentialsFile: /nonexistent/daemon-credentials\n"+yaml), 0o600))
		return path, dataDir
	}
	path, dataDir := write("")
	_, err = runRestore(t, "blob", "--config", path, "--store-credentials-file", creds, "--at", at)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "blob.versionRetention")
	require.NoFileExists(t, filepath.Join(dataDir, hold.MarkerFile))

	path, dataDir = write("  versionRetention: 24h\n")
	out, err := runRestore(t, "blob", "--config", path, "--store-credentials-file", creds, "--at", at)
	require.NoError(t, err)
	require.Contains(t, out, "1 copied, 1 deleted")
	require.Contains(t, out, "for want of a version at that time: new")
	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "then", string(got))
	require.FileExists(t, filepath.Join(dataDir, hold.MarkerFile))
}
