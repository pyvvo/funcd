package verify_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/verify"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/snapshot"
)

const tl = "1111111111111111"

type source struct {
	version string
	records []snapshot.Record
}

func (s source) Snapshot(_ context.Context, emit func(snapshot.Record) error) (string, error) {
	for _, r := range s.records {
		if err := emit(r); err != nil {
			return "", err
		}
	}
	return s.version, nil
}

// sealedTarget writes n hourly generations sealed to two fresh recipients and returns the target directory, a bucket
// on it and the identities.
func sealedTarget(t *testing.T, n int) (string, blob.Bucket, []age.Identity) {
	t.Helper()
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	b, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipients := filepath.Join(t.TempDir(), "recipients.txt")
	require.NoError(t, os.WriteFile(recipients, []byte(a.Recipient().String()+"\n"+b.Recipient().String()+"\n"), 0o600))
	sealer, err := envelope.New(envelope.Config{Recipients: []string{recipients}, NoSecrets: true, Logger: quiet})
	require.NoError(t, err)
	dir := t.TempDir()
	tg, err := backup.Open(ctx, backup.Config{Target: gocloud.FileURL(dir), DataDir: t.TempDir(),
		Retention: backup.Retention{Hourly: 48}, Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	rec := func(k string) snapshot.Record { return snapshot.Record{Key: []byte(k), Value: []byte("value of " + k)} }
	for range n {
		_, err := tg.Write(ctx, source{records: []snapshot.Record{rec("e1"), rec("e2")}},
			source{version: tl + "-7", records: []snapshot.Record{rec("m1"), rec("m2"), rec("m3")}},
			source{records: []snapshot.Record{rec("r1")}},
			backup.WriteOptions{Seal: sealer.Seal(), Keys: sealer.Keys()})
		require.NoError(t, err)
	}
	bucket, err := gocloud.OpenWith(ctx, gocloud.FileURL(dir), gocloud.OpenOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })
	return dir, bucket, []age.Identity{a}
}

// scenario: verify-detects-damage — one changed byte in a part of generation 7, or its last part missing: verify fails
// naming the store and part, and writes nothing under gen/verified/.
func TestScenarioVerifyDetectsDamage(t *testing.T) {
	part := func(dir, store string) string {
		return filepath.Join(dir, "gen", "hourly", "0000000007-"+tl, store, "part-00000")
	}
	for _, tc := range []struct {
		name, store string
		damage      func(t *testing.T, path string)
	}{
		{"a changed byte", "metastore", func(t *testing.T, path string) {
			t.Helper()
			data, err := os.ReadFile(path) //nolint:gosec // a test temp path
			require.NoError(t, err)
			data[len(data)/2] ^= 0x01
			require.NoError(t, os.WriteFile(path, data, 0o600))
		}},
		{"the last part missing", "runs", func(t *testing.T, path string) {
			t.Helper()
			require.NoError(t, os.Remove(path))
			_ = os.Remove(path + ".attrs")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, bucket, ids := sealedTarget(t, 7)
			tc.damage(t, part(dir, tc.store))
			_, err := verify.Verify(context.Background(), verify.Options{Bucket: bucket, Identities: ids})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, "store "+tc.store)
			require.ErrorContains(t, err, "part-00000")
			_, serr := os.Stat(filepath.Join(dir, "gen", "verified"))
			require.True(t, os.IsNotExist(serr), "nothing is pinned")
		})
	}
}

// A target without a complete ladder generation, or without the generation asked for, is fault.NotFound; a passed
// verification of an undamaged one counts every record.
func TestVerifyNoGeneration(t *testing.T) {
	ctx := context.Background()
	empty, err := gocloud.OpenWith(ctx, gocloud.FileURL(t.TempDir()), gocloud.OpenOptions{})
	require.NoError(t, err)
	defer func() { _ = empty.Close() }()
	_, err = verify.Verify(ctx, verify.Options{Bucket: empty})
	require.Equal(t, fault.NotFound, fault.KindOf(err))

	_, bucket, ids := sealedTarget(t, 2)
	_, err = verify.Verify(ctx, verify.Options{Bucket: bucket, Identities: ids, Generation: 9})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	res, err := verify.Verify(ctx, verify.Options{Bucket: bucket, Identities: ids, Generation: 1})
	require.NoError(t, err)
	require.Equal(t, verify.Result{Generation: 1, Records: 6, Pinned: true}, res)
}
