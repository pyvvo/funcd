// Command funcdcli is a kubectl-style CLI over the funcd control-plane API (ADR-0024), built on
// the cobra command framework (ADR-0042). It is a thin shell over pkg/sdk (control-plane verbs)
// and internal/artifact (push/pull/login/logout): the cobra root (newRootCmd) parses
// --server/--token, dispatches one verb, and main maps a returned error to a non-zero exit.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd(os.Stdout).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "funcdcli: "+err.Error())
		os.Exit(1)
	}
}
