package diskfull_test

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/diskfull"
)

// scenario: enospc-is-507 — a store write that fails with an error matching syscall.ENOSPC is a 507, however the
// store wrapped it; an error that keeps only the text stays a 500, as Badger's do until its upstream release.
func TestScenarioEnospcIs507(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"errno":   syscall.ENOSPC,
		"path":    &os.PathError{Op: "write", Path: "000001.vlog", Err: syscall.ENOSPC},
		"wrapped": fmt.Errorf("cannot create new mem table: %w", syscall.ENOSPC),
		"fault":   fault.Wrapf(syscall.ENOSPC, fault.Internal, "badger.Open", "open badger"),
		"syscall": os.NewSyscallError("fallocate", syscall.ENOSPC),
		"joined":  errors.Join(errors.New("flush"), syscall.ENOSPC),
	} {
		got := diskfull.Wrapf(err, "kvbadger.write", "put %q", "k")
		require.Equal(t, fault.InsufficientStorage, fault.KindOf(got), name)
		require.ErrorIs(t, got, syscall.ENOSPC, name)
		require.Equal(t, http.StatusInsufficientStorage, fault.ToProblem(got).Status, name)
	}
	text := diskfull.Wrapf(errors.New("cannot create new mem table err: no space left on device"), "kvbadger.write", "put")
	require.Equal(t, fault.Internal, fault.KindOf(text), "an error that keeps only its text is not recognised")
	require.Equal(t, fault.Internal, fault.KindOf(diskfull.Wrapf(errors.New("conflict"), "op", "put")))
}
