//go:build unix

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
	fnruntime "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/containerd"
	"github.com/pyvvo/funcd/internal/runtime/ctrmanager"
)

// scenario: memory-mode-storage-is-ephemeral — on the containerd path with storage.mode memory the engines' storage
// root is a temporary directory that the execution closer removes at exit; otherwise it is <dataDir>/providers.
func TestScenarioMemoryModeStorageIsEphemeral(t *testing.T) {
	var cfg config.Config
	cfg.Storage.DataDir = t.TempDir()
	root, closeRoot, err := providerStorageRoot(cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.Storage.DataDir, "providers"), root)
	require.NoError(t, closeRoot())

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	mem := cfgProcess(t.TempDir())
	mem.Storage.Mode = "memory"
	mem.Runtime.Mode = "containerd"
	mem.Runtime.Containerd.Socket = filepath.Join(t.TempDir(), "containerd.sock")
	_, closeExec, err := containerdOptions(context.Background(), mem, slog.New(slog.DiscardHandler),
		func(ctrmanager.Config) (ctrmanager.Manager, error) {
			return exitingManager{exited: make(chan struct{})}, nil
		},
		func(containerd.Config) (fnruntime.Runtime, error) { return &sweptRuntime{}, nil })
	require.NoError(t, err)
	roots, err := filepath.Glob(filepath.Join(tmp, "funcd-providers-*"))
	require.NoError(t, err)
	require.Len(t, roots, 1, "memory mode makes one temporary storage root")
	require.NoError(t, closeExec())
	require.NoDirExists(t, roots[0], "the exit removes it")
}
