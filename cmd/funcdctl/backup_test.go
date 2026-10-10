package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/backup/verify"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const (
	adminToken  = "admin-secret"
	backupTL    = "1111111111111111"
	backupHours = time.Hour
)

// backupT0 is when generation 0 would have been written; generation n is n hours later.
var backupT0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // a test fixture

type fixedSource struct {
	version string
	records []snapshot.Record
}

func (s fixedSource) Snapshot(_ context.Context, emit func(snapshot.Record) error) (string, error) {
	for _, r := range s.records {
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return s.version, nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func backupTimes() config.BackupTimes {
	return config.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: 5 * time.Minute}
}

func unsealed(t *testing.T) *envelope.Sealer {
	t.Helper()
	s, err := envelope.New(envelope.Config{None: true, NoSecrets: true, Logger: quietLog()})
	require.NoError(t, err)
	return s
}

// writeGenerations writes n hourly generations into the directory target dir, generation i with the ModTime
// backupT0 + i hours.
func writeGenerations(t *testing.T, dir string, n int, sealer *envelope.Sealer) {
	t.Helper()
	ctx := context.Background()
	tg, err := backup.Open(ctx, backup.Config{Target: gocloud.FileURL(dir), DataDir: t.TempDir(),
		Retention: backup.Retention{Hourly: 48}, Logger: quietLog()})
	require.NoError(t, err)
	defer func() { _ = tg.Close() }()
	rec := func(k string) snapshot.Record { return snapshot.Record{Key: []byte(k), Value: []byte("v")} }
	for i := 1; i <= n; i++ {
		m, err := tg.Write(ctx, fixedSource{records: []snapshot.Record{rec("e1")}},
			fixedSource{version: backupTL + "-7", records: []snapshot.Record{rec("m1")}},
			fixedSource{records: []snapshot.Record{rec("r1")}},
			backup.WriteOptions{Seal: sealer.Seal(), Keys: sealer.Keys()})
		require.NoError(t, err)
		at := backupT0.Add(time.Duration(i) * backupHours)
		name := string(filepath.Separator) + fmt.Sprintf("%010d-%s", m.Generation, backupTL) + string(filepath.Separator)
		require.NoError(t, filepath.WalkDir(filepath.Join(dir, "gen"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.Contains(p, name) {
				return err
			}
			return os.Chtimes(p, at, at)
		}))
	}
}

// backupServer mounts the control plane reading s, and returns an admin's and a developer's client.
func backupServer(t *testing.T, s controlplane.BackupStatuser) (admin, dev *sdk.Client) {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		adminToken: {Subject: "admin", Role: auth.RoleAdmin},
		devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{Store: store.New(memory.New()), Authorizer: rbac.New(),
		Credentials: creds, Backup: s})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	admin, err = sdk.New(srv.URL, sdk.WithToken(adminToken))
	require.NoError(t, err)
	dev, err = sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	return admin, dev
}

// files maps every file under dir to its ModTime.
func files(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = info.ModTime()
		return nil
	}))
	return out
}

// scenario: verify-pins-generation — generation 7 the newest complete: `funcdctl backup verify` with an identity
// copies it to gen/verified/<7>-<timeline>/; a rerun writes nothing and exits 0.
func TestScenarioVerifyPinsGeneration(t *testing.T) {
	a, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	b, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	keys := t.TempDir()
	recipients, identity := filepath.Join(keys, "recipients.txt"), filepath.Join(keys, "identity.txt")
	require.NoError(t, os.WriteFile(recipients, []byte(a.Recipient().String()+"\n"+b.Recipient().String()+"\n"), 0o600))
	require.NoError(t, os.WriteFile(identity, []byte(a.String()+"\n"), 0o600))
	sealer, err := envelope.New(envelope.Config{Recipients: []string{recipients}, NoSecrets: true, Logger: quietLog()})
	require.NoError(t, err)
	dir := t.TempDir()
	writeGenerations(t, dir, 7, sealer)

	var out bytes.Buffer
	args := []string{"backup", "verify", "--target", gocloud.FileURL(dir), "--identity", identity}
	require.NoError(t, execCLI(&out, nil, args...))
	require.Contains(t, out.String(), "generation 7 verified: 3 records")
	require.Contains(t, out.String(), "; pinned under gen/verified/")
	name := fmt.Sprintf("%010d-%s", 7, backupTL)
	original, err := os.ReadFile(filepath.Join(dir, "gen", "hourly", name, "manifest.yaml")) //nolint:gosec // a test temp path
	require.NoError(t, err)
	pinned, err := os.ReadFile(filepath.Join(dir, "gen", "verified", name, "manifest.yaml")) //nolint:gosec // a test temp path
	require.NoError(t, err)
	require.Equal(t, original, pinned)
	verified := filepath.Join(dir, "gen", "verified")
	before := files(t, verified)
	require.Len(t, before, 4, "three parts and the manifest")

	out.Reset()
	require.NoError(t, execCLI(&out, nil, args...))
	require.Contains(t, out.String(), "already pinned under gen/verified/: nothing written")
	require.Equal(t, before, files(t, verified))
}

// scenario: status-shows-restore-points — generations 3 to 9 complete and 7 verified: an admin's `funcdctl backup
// status` prints the times of 9, 7 and 3 and rpoRisk; a developer gets 403.
func TestScenarioStatusShowsRestorePoints(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeGenerations(t, dir, 9, unsealed(t))
	for _, n := range []int{1, 2} {
		require.NoError(t, os.RemoveAll(filepath.Join(dir, "gen", "hourly", fmt.Sprintf("%010d-%s", n, backupTL))))
	}
	bucket, err := gocloud.OpenWith(ctx, gocloud.FileURL(dir), gocloud.OpenOptions{})
	require.NoError(t, err)
	_, err = verify.Verify(ctx, verify.Options{Bucket: bucket, Generation: 7})
	require.NoError(t, err)
	require.NoError(t, bucket.Close())

	tg, err := backup.Open(ctx, backup.Config{Target: gocloud.FileURL(dir), Retention: backup.Retention{Hourly: 48}, Logger: quietLog()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	r, err := runner.New(runner.Config{Target: tg, Sealer: unsealed(t), Times: backupTimes(), Logger: quietLog(),
		Clock: clock.NewManual(backupT0.Add(9*time.Hour + 10*time.Minute))})
	require.NoError(t, err)
	admin, dev := backupServer(t, r)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, admin, "backup", "status"))
	stamp := func(n int) string { return v1.NewTimestamp(backupT0.Add(time.Duration(n) * time.Hour)).String() }
	require.Contains(t, out.String(), "lastSuccessTime: \""+stamp(9)+"\"")
	require.Contains(t, out.String(), "lastVerifiedTime: \""+stamp(7)+"\"")
	require.Contains(t, out.String(), "earliestRestorePoint: \""+stamp(3)+"\"")
	require.Contains(t, out.String(), "rpoRisk: true")
	require.Contains(t, out.String(), "enabled: true")

	err = execCLI(io.Discard, dev, "backup", "status")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}

// scenario: kv-stream-without-platform-backup — no backup.target and a KV export whose target refuses puts: after a
// failed KV run, `funcdctl backup status` shows enabled: false and streams.kv.lastFailure. The KV export reports
// through Recorder("kv"), which ADR-0209 wires into its loop.
func TestScenarioKVStreamWithoutPlatformBackup(t *testing.T) {
	r, err := runner.New(runner.Config{Times: backupTimes(), Logger: quietLog()})
	require.NoError(t, err)
	r.Recorder("kv")(time.Now().Add(-time.Second), fault.Forbiddenf("kvbadger.Ship", "the target refused the put"))
	admin, _ := backupServer(t, r)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, admin, "backup", "status"))
	require.Contains(t, out.String(), "enabled: false")
	require.Contains(t, out.String(), "streams:\n  kv:\n    lastFailure:\n")
	require.Contains(t, out.String(), "the target refused the put")
}
