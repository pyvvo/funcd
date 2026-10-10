package backup

import "github.com/pyvvo/funcd/internal/platform/clock"

// SetClock replaces a Target's clock, for the ladder's tests.
func SetClock(t Target, c clock.Clock) { t.(*target).clock = c }
