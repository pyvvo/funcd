package funcd_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// openBadgers returns the stacks of the goroutines that open Badger databases run. A platform runs them until its
// Shutdown closes its stores.
func openBadgers() []string {
	buf := make([]byte, 1<<22)
	var open []string
	for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
		if strings.Contains(g, "github.com/dgraph-io/badger/v4") {
			open = append(open, g)
		}
	}
	return open
}

// leftOpen ends TestMain: a run whose tests passed fails when a test left a platform open. Such a platform keeps two
// in-memory stores of about 166 MB under -race to the end of the run, and -count=5 multiplies them (#1362).
func leftOpen(code int) int {
	var open []string
	if code != 0 || !poll.Never(func() bool { open = openBadgers(); return len(open) == 0 }, 2*time.Second, 20*time.Millisecond) {
		return code
	}
	fmt.Fprintf(os.Stderr, "TestMain: a test left a platform open: %d goroutines of open Badger databases remain after the tests."+
		" Shut every platform down: t.Cleanup(func() { _ = p.Shutdown(context.Background()) }). The first one:\n%s\n",
		len(open), open[0])
	return 1
}

// Issue #1362: openBadgers shows a platform's stores until its Shutdown, so TestMain finds a platform a test left open.
// Not parallel: no other test's platform may be open while it checks that none is.
func TestIssue1362_OpenBadgersShowsAPlatformUntilItsShutdown(t *testing.T) {
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	require.NotEmpty(t, openBadgers())
	require.NoError(t, p.Shutdown(context.Background()))
	require.False(t, poll.Never(func() bool { return len(openBadgers()) == 0 }, 2*time.Second, 20*time.Millisecond),
		"Badger goroutines still run after Shutdown")
}
