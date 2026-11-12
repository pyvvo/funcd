//go:build dev

package main

import (
	"flag"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/reference"
	"github.com/pyvvo/funcd/internal/testkit/funcdenv"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var updateSpec = flag.Bool("update", false, "rewrite the funcdctl Spec page")

const funcdctlSpec = "../../docs/guide/develop/tools/funcdctl/spec.md"

// scenario: command-spec-generated
func TestScenarioCommandSpecGenerated(t *testing.T) {
	funcdenv.Clear(t)
	page := renderFuncdctlSpec(t)
	if *updateSpec {
		require.NoError(t, os.WriteFile(funcdctlSpec, page, 0o644))
		return
	}
	require.NoError(t, reference.Stale(funcdctlSpec, page))
	require.Contains(t, string(page), "## `funcdctl apply`")
	require.Contains(t, string(page), "## `funcdctl dev`\n\nfuncdctl-dev only.")
}

// scenario: page-same-on-every-host
func TestScenarioPageSameOnEveryHost(t *testing.T) {
	names, err := reference.Variables("../funcd", "../funcdctl")
	require.NoError(t, err)
	funcdenv.Sentinels(t, names)
	require.NoError(t, reference.Stale(funcdctlSpec, renderFuncdctlSpec(t)))
}

func renderFuncdctlSpec(t *testing.T) []byte {
	t.Helper()
	cmds, err := reference.Commands(newRootCmd(io.Discard))
	require.NoError(t, err)
	return reference.Page("funcdctl", "Every funcdctl command with its usage line, its text and its flags.",
		reference.CommandTables(cmds))
}
