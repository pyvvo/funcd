//go:build dev

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// pythonHandlerProject and nodeHandlerProject return the minimal one-Function folders the interpreter startup tests
// boot.
func pythonHandlerProject() map[string]string {
	return map[string]string{
		"api.yaml":          fnDoc("api", "python314", ""),
		"api.py":            "def handle(event, context):\n    return {}\n",
		"api.contract.yaml": "runtime: python314\ninput:\n  type: object\noutput:\n  type: object\n",
	}
}

func nodeHandlerProject() map[string]string {
	return nodeFunction("api", "", "export function handle() { return {}; }\n")
}

// scenario: dev-interpreter-env-overrides (the resolution half) — FUNCD_PYTHON / FUNCD_NODE win, else the
// interpreter on PATH, else none (ADR-0247 Decision 9).
func TestDevInterpreter(t *testing.T) {
	bin := t.TempDir()
	onPath := filepath.Join(bin, "python3")
	require.NoError(t, os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o700))
	t.Setenv("PATH", bin)
	t.Setenv("FUNCD_PYTHON", "")
	require.Equal(t, onPath, devInterpreter("FUNCD_PYTHON", "python3"), "the interpreter on PATH")
	t.Setenv("FUNCD_PYTHON", filepath.Join("venv", "bin", "python"))
	require.Equal(t, filepath.Join("venv", "bin", "python"), devInterpreter("FUNCD_PYTHON", "python3"), "the override wins")
	t.Setenv("FUNCD_NODE", "")
	require.Empty(t, devInterpreter("FUNCD_NODE", "node"), "none found")
}

// Issue #322: funcdctl dev registered whatever python it found, so one that cannot import the shim (too
// old for its syntax) surfaced only later as a shape error. A python handler now fails at
// startup with the interpreter's reason; a node handler still starts.
func TestIssue322_UnusablePythonFailsAtStartup(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\necho \"SyntaxError: invalid syntax\" >&2\nexit 1\n"), 0o700))
	t.Setenv("FUNCD_PYTHON", python)

	t.Run("python-handler", func(t *testing.T) {
		_, err := tryStartDev(t, devProject(t, pythonHandlerProject()))
		require.Error(t, err, "a python handler with a python that cannot load the shim fails at startup")
		require.Contains(t, err.Error(), "SyntaxError", "the error names why the shim cannot load")
	})
	t.Run("node-handler", func(t *testing.T) {
		requireNode(t)
		_, err := tryStartDev(t, devProject(t, nodeHandlerProject()))
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

	t.Run("python-handler", func(t *testing.T) {
		node := filepath.Join(t.TempDir(), "node")
		require.NoError(t, os.WriteFile(node, []byte("#!/bin/sh\nexit 1\n"), 0o700))
		t.Setenv("FUNCD_NODE", node)
		_, err := tryStartDev(t, devProject(t, pythonHandlerProject()))
		require.Error(t, err, "a python handler with no python found fails at startup")
		require.Contains(t, err.Error(), "no python interpreter", "the error says no python was found")
	})
	t.Run("node-handler", func(t *testing.T) {
		if realNode == "" {
			t.Skip("node not on PATH; skipping the funcdctl dev node lane")
		}
		t.Setenv("FUNCD_NODE", realNode)
		_, err := tryStartDev(t, devProject(t, nodeHandlerProject()))
		require.NoError(t, err, "a missing python does not block a node handler")
	})
}

// Issue #503: with no node found and a python that loads the shim, funcdctl dev registered python as the
// default shim and started; the node Function then ran on the python shim. A node handler with no node
// found now fails at startup; a python handler still starts.
func TestIssue503_MissingNodeFailsAtStartup(t *testing.T) {
	python := filepath.Join(t.TempDir(), "python3")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("FUNCD_PYTHON", python)
	t.Setenv("FUNCD_NODE", "")
	t.Setenv("PATH", t.TempDir())

	t.Run("node-handler", func(t *testing.T) {
		_, err := tryStartDev(t, devProject(t, nodeHandlerProject()))
		require.Error(t, err, "a node handler with no node found fails at startup")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "a missing node is NotFound: %v", err)
		require.Contains(t, err.Error(), "no node interpreter", "the error says no node was found")
	})
	t.Run("python-handler", func(t *testing.T) {
		_, err := tryStartDev(t, devProject(t, pythonHandlerProject()))
		require.NoError(t, err, "a missing node does not block a python handler")
	})
}

// requirePython skips a test when funcdctl dev finds no python that loads the shim.
func requirePython(t *testing.T) {
	t.Helper()
	_, cleanup, err := devShimOptions(context.Background(), "requirePython", true, false)
	if err != nil {
		t.Skipf("funcdctl dev has no usable python: %v", err)
	}
	cleanup()
}

// pythonBuild is a python handler that returns version v.
func pythonBuild(v int) string {
	return fmt.Sprintf("def handle(context, event):\n    return {\"v\": %d}\n", v)
}

// scenario: dev-python-in-place — with intake = "src/intake/main.py" in [tool.funcd-bundle.functions], intake runs from
// source and a save rolls a new revision; without the table, src/main.py runs the Function named after the folder.
func TestScenarioDevPythonInPlace(t *testing.T) {
	requirePython(t)
	intake := map[string]string{
		"intake.yaml":          fnDoc("intake", "python314", ""),
		"intake.contract.yaml": "runtime: python314\ninput:\n  type: object\noutput:\n  type: object\n",
	}

	t.Run("listed", func(t *testing.T) {
		dir := devProject(t, merge(intake, map[string]string{
			"pyproject.toml":     "[tool.funcd-bundle.functions]\nintake = \"src/intake/main.py\"\n",
			"src/intake/main.py": pythonBuild(1),
		}))
		inst := runDev(t, dir)
		eventuallyServes(t, inst.gatewayURL+"/function/intake", `{"data":{}}`, http.StatusOK, `"v":1`)
		// Another size: python reuses its cached bytecode for a source of the same size saved within the same second.
		writeProject(t, dir, map[string]string{"src/intake/main.py": pythonBuild(22)})
		eventuallyServes(t, inst.gatewayURL+"/function/intake", `{"data":{}}`, http.StatusOK, `"v":22`)
	})
	t.Run("src-main", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "intake")
		require.NoError(t, os.Mkdir(dir, 0o700))
		writeProject(t, dir, merge(intake, map[string]string{"src/main.py": pythonBuild(1)}))
		inst := runDev(t, dir)
		eventuallyServes(t, inst.gatewayURL+"/function/intake", `{"data":{}}`, http.StatusOK, `"v":1`)
	})
}
