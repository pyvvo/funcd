//go:build dev

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// scenario: dev-python-from-manifest / dev-interpreter-env-overrides (the resolution half) — a manifest
// dev.python/dev.node value resolves relative to the manifest dir (or absolute); empty ⇒ "" so the caller
// falls back to PATH. The env-override precedence is enforced in devShimOptions (env checked before this).
func TestResolveInterpreter(t *testing.T) {
	base := filepath.Join("home", "proj")
	require.Equal(t, "", resolveInterpreter("", base), "empty ⇒ empty (caller falls back to PATH)")
	require.Equal(t, filepath.Join("/abs", "python"), resolveInterpreter(filepath.Join("/abs", "python"), base),
		"an absolute interpreter path is used as-is")
	require.Equal(t, filepath.Join(base, ".venv", "bin", "python"), resolveInterpreter(filepath.Join(".venv", "bin", "python"), base),
		"a relative path resolves against the manifest dir (the project venv)")
}

// Issue #322: funcdctl dev registered whatever python it found, so one that cannot import the shim (too
// old, or missing fastjsonschema) surfaced only later as a shape error. A python handler now fails at
// startup with the interpreter's reason; a node handler still starts.
func TestIssue322_UnusablePythonFailsAtStartup(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\necho \"ModuleNotFoundError: No module named 'fastjsonschema'\" >&2\nexit 1\n"), 0o700))
	t.Setenv("FUNCD_PYTHON", python)
	start := func(t *testing.T, files map[string]string) error {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		a := &cli{out: io.Discard}
		inst, err := a.startDev(ctx, devProject(t, files), "", devConfig{})
		t.Cleanup(func() {
			cancel()
			if inst != nil {
				_ = inst.stop()
			}
		})
		return err
	}

	t.Run("python-handler", func(t *testing.T) {
		err := start(t, map[string]string{
			"funcdctl.yaml": "runtime: python314\nhandler: handle\n" + permissiveContract,
			"handler.py":    "def handle(event, context):\n    return {}\n",
		})
		require.Error(t, err, "a python handler with a python that cannot load the shim fails at startup")
		require.Contains(t, err.Error(), "fastjsonschema", "the error names why the shim cannot load")
	})
	t.Run("node-handler", func(t *testing.T) {
		requireNode(t)
		err := start(t, map[string]string{
			"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
			"handler.mjs":   "export function handle() { return {}; }\n",
		})
		require.NoError(t, err, "an unusable python does not block a node handler")
	})
}

// Issue #430: with node on PATH and no python anywhere, funcdctl dev registered only node and started; the
// python Function then failed RuntimeUnavailable at reconcile. A python handler with no python found now
// fails at startup; a node handler still starts.
func TestIssue430_MissingPythonFailsAtStartup(t *testing.T) {
	realNode, _ := exec.LookPath("node")
	t.Setenv("FUNCD_PYTHON", "")
	t.Setenv("PATH", t.TempDir())
	start := func(t *testing.T, files map[string]string) error {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		a := &cli{out: io.Discard}
		inst, err := a.startDev(ctx, devProject(t, files), "", devConfig{})
		t.Cleanup(func() {
			cancel()
			if inst != nil {
				_ = inst.stop()
			}
		})
		return err
	}

	t.Run("python-handler", func(t *testing.T) {
		node := filepath.Join(t.TempDir(), "node")
		require.NoError(t, os.WriteFile(node, []byte("#!/bin/sh\nexit 1\n"), 0o700))
		t.Setenv("FUNCD_NODE", node)
		err := start(t, map[string]string{
			"funcdctl.yaml": "runtime: python314\nhandler: handle\n" + permissiveContract,
			"handler.py":    "def handle(event, context):\n    return {}\n",
		})
		require.Error(t, err, "a python handler with no python found fails at startup")
		require.Contains(t, err.Error(), "no python interpreter", "the error says no python was found")
	})
	t.Run("node-handler", func(t *testing.T) {
		if realNode == "" {
			t.Skip("node not on PATH; skipping the funcdctl dev node lane")
		}
		t.Setenv("FUNCD_NODE", realNode)
		err := start(t, map[string]string{
			"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
			"handler.mjs":   "export function handle() { return {}; }\n",
		})
		require.NoError(t, err, "a missing python does not block a node handler")
	})
}
