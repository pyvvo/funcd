//go:build dev

package main

import (
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
