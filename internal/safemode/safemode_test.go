package safemode_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/safemode"
)

func readState(t *testing.T, dir string) safemode.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, safemode.StateFile))
	require.NoError(t, err)
	var s safemode.State
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// TestBeginModes: N−1 unclean starts start normally, N and 2N−1 held, 2N stopped with nothing written.
func TestBeginModes(t *testing.T) {
	t.Parallel()
	const n = 3
	cfg := safemode.Config{AfterCrashes: n, StableAfter: time.Minute}
	for unclean, want := range map[int]safemode.Mode{n - 1: safemode.Normal, n: safemode.Held, 2*n - 1: safemode.Held, 2 * n: safemode.Stopped} {
		dir := t.TempDir()
		for range unclean {
			_, _, _, err := safemode.Begin(dir, "v0.8.0", cfg)
			require.NoError(t, err)
		}
		before, err := os.ReadFile(filepath.Join(dir, safemode.StateFile))
		require.NoError(t, err)
		s, st, mode, err := safemode.Begin(dir, "v0.9.0", cfg)
		require.Equal(t, want, mode, "after %d unclean starts", unclean)
		require.Equal(t, unclean, st.Unclean)
		require.Equal(t, "v0.8.0", st.Version, "the state read is the last start's")
		if want == safemode.Stopped {
			var stopped *safemode.StoppedError
			require.ErrorAs(t, err, &stopped)
			require.Nil(t, s)
			after, rerr := os.ReadFile(filepath.Join(dir, safemode.StateFile))
			require.NoError(t, rerr)
			require.Equal(t, before, after, "a stopped start writes nothing")
			continue
		}
		require.NoError(t, err)
		require.Equal(t, unclean+1, readState(t, dir).Unclean)
		require.Equal(t, "v0.9.0", readState(t, dir).Version)
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, safemode.StateFile), []byte("{"), 0o600))
	_, _, _, err := safemode.Begin(dir, "v0.9.0", cfg)
	require.Equal(t, fault.Internal, fault.KindOf(err), "%v", err)
	_, _, _, err = safemode.Begin(t.TempDir(), "v0.9.0", safemode.Config{})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// TestStateRoundTrip: the file's keys are the lowerCamel field names, Upgrade.At ADR-0196's form; Clean, Failed,
// RecordUpgrade and Reset each change only their fields.
func TestStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	gen := &backup.GenRef{Timeline: "0123456789abcdef", Generation: 7}
	require.NoError(t, safemode.RecordUpgrade(dir, safemode.Upgrade{From: "v0.8.0", To: "v0.9.0", Generation: gen, At: v1.NewTimestamp(at)}))
	s, _, _, err := safemode.Begin(dir, "v0.9.0", safemode.Config{AfterCrashes: 3})
	require.NoError(t, err)
	require.NoError(t, s.Failed(errors.New("run: boom")))
	raw, err := os.ReadFile(filepath.Join(dir, safemode.StateFile))
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	require.ElementsMatch(t, []string{"unclean", "version", "lastError", "upgrade"}, keysOf(m))
	var u map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(m["upgrade"], &u))
	require.ElementsMatch(t, []string{"from", "to", "generation", "at"}, keysOf(u))
	require.JSONEq(t, `"2026-10-10T12:00:00.000Z"`, string(u["at"]))
	require.Equal(t, safemode.State{Unclean: 1, Version: "v0.9.0", LastError: "run: boom",
		Upgrade: &safemode.Upgrade{From: "v0.8.0", To: "v0.9.0", Generation: gen, At: v1.NewTimestamp(at)}}, readState(t, dir))

	require.NoError(t, s.Clean())
	require.Equal(t, 0, readState(t, dir).Unclean)
	require.Equal(t, "run: boom", readState(t, dir).LastError)
	_, _, _, err = safemode.Begin(dir, "v0.9.0", safemode.Config{AfterCrashes: 3})
	require.NoError(t, err)
	require.NoError(t, safemode.Reset(dir))
	got := readState(t, dir)
	require.Equal(t, 0, got.Unclean)
	require.Empty(t, got.LastError)
	require.Equal(t, gen, got.Upgrade.Generation, "Reset keeps the upgrade")
}

func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestStartCleanFailedRace: the stableAfter timer's Clean and serve's Failed write the file concurrently; every write
// is whole (run under -race).
func TestStartCleanFailedRace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, _, _, err := safemode.Begin(dir, "v0.9.0", safemode.Config{AfterCrashes: 3})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if i%2 == 0 {
				require.NoError(t, s.Clean())
			} else {
				require.NoError(t, s.Failed(errors.New("run: boom")))
			}
		})
	}
	wg.Wait()
	got := readState(t, dir)
	require.Equal(t, 0, got.Unclean)
	require.Equal(t, "run: boom", got.LastError)
}

// The stopped error names the count, the last error and the next command: the rollback to the upgrade's
// generation when its to is this version, else the newest verified generation, "no way back" after --no-snapshot.
func TestStoppedErrorNamesNextCommand(t *testing.T) {
	t.Parallel()
	gen := &backup.GenRef{Timeline: "0123456789abcdef", Generation: 7}
	err := &safemode.StoppedError{State: safemode.State{Unclean: 6, LastError: "assemble platform: boom",
		Upgrade: &safemode.Upgrade{From: "v0.8.0", To: version.Version, Generation: gen}}}
	require.Contains(t, err.Error(), "6 unclean starts")
	require.Contains(t, err.Error(), "assemble platform: boom")
	require.Contains(t, err.Error(), ".previous restore run 0123456789abcdef/7")

	err.State.Upgrade.Generation = nil
	require.Contains(t, err.Error(), "no way back")
	require.Contains(t, err.Error(), "restore run verified")
	require.NotContains(t, err.Error(), ".previous")

	err.State.Upgrade = &safemode.Upgrade{From: "v0.7.0", To: "v0.8.0-not-this", Generation: gen}
	require.NotContains(t, err.Error(), "no way back")
	require.Contains(t, err.Error(), "restore run verified")
}
