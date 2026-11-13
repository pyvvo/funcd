package main

import (
	"flag"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/reference"
	"github.com/pyvvo/funcd/internal/testkit/funcdenv"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var updateSpec = flag.Bool("update", false, "rewrite the funcd Spec page")

const runningFuncdSpec = "../../docs/guide/develop/running-funcd/spec.md"

// scenario: config-spec-generated
func TestScenarioConfigSpecGenerated(t *testing.T) {
	funcdenv.Clear(t)
	page := renderRunningFuncdSpec(t)
	if *updateSpec {
		require.NoError(t, os.WriteFile(runningFuncdSpec, page, 0o644))
		return
	}
	require.NoError(t, reference.Stale(runningFuncdSpec, page))
	require.Contains(t, string(page), "| `server.listenAddr` | string | `127.0.0.1:8080` |")
	require.Contains(t, string(page), "Variable: `FUNCD_LISTEN_ADDR`.")
	require.Contains(t, string(page), "## `funcd version`")
}

// scenario: page-same-on-every-host
func TestScenarioPageSameOnEveryHost(t *testing.T) {
	names, err := reference.Variables("../funcd", "../funcdctl")
	require.NoError(t, err)
	funcdenv.Sentinels(t, names)
	require.NoError(t, reference.Stale(runningFuncdSpec, renderRunningFuncdSpec(t)))
}

func renderRunningFuncdSpec(t *testing.T) []byte {
	t.Helper()
	cmds, err := reference.Commands(newRootCmd(io.Discard))
	require.NoError(t, err)
	return reference.Page("funcd",
		"Every key of funcdconfig.yaml with its type, default, limits and variable, then every funcd command with its flags.",
		[]byte("## `funcdconfig.yaml`\n"), reference.ConfigTable(reference.ConfigKeys(config.Defaults())),
		reference.CommandTables(cmds))
}
