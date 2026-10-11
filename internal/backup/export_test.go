package backup

import (
	"os"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// SetClock replaces a Target's clock, for the ladder's tests.
func SetClock(t Target, c clock.Clock) { t.(*target).clock = c }

// LockFile is a directory Target's lock, nil until Ready takes it.
func LockFile(t Target) *os.File { return t.(*target).lock }
