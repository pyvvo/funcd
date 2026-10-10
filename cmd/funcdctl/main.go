// Command funcdctl is a kubectl-style CLI over the funcd control-plane API (ADR-0024), built on
// the cobra command framework (ADR-0042). It is a thin shell over pkg/sdk (control-plane verbs)
// and internal/artifact (push/pull/login/logout): the cobra root (newRootCmd) parses
// --server/--token, dispatches one verb, and main maps a returned error to a non-zero exit.
package main

import (
	"fmt"
	"os"
	"strings"
	_ "time/tzdata" // ADR-0211: apply validates cron time zones on a host without zoneinfo
)

func main() {
	if err := newRootCmd(os.Stdout).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, errorLine(err))
		os.Exit(1)
	}
}

// errorLine renders a returned error for the terminal. Its text can relay a function's answer (a failed
// dead-letter replay), so each line passes through termSafe; the line breaks of a joined error stay.
func errorLine(err error) string {
	lines := strings.Split(err.Error(), "\n")
	for i, l := range lines {
		lines[i] = termSafe(l)
	}
	return "funcdctl: " + strings.Join(lines, "\n")
}
