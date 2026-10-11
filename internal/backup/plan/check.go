package plan

import (
	"fmt"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// exitRTOImpossible is the exit code of settings accepted with --rto below the download time (Decision 6).
const exitRTOImpossible = 3

// CheckConfig runs the daemon's loader on merged (Decision 3): config.Parse with funcdctl's FUNCD_* environment, its
// error returned unchanged; then the warnings of CheckBackup and CheckBlob, in that order.
func CheckConfig(path string, merged []byte) ([]config.Finding, error) {
	c, err := config.Parse(path, merged, config.Flags{})
	if err != nil {
		return nil, err
	}
	var warns []config.Finding
	for _, f := range append(c.CheckBackup(), c.CheckBlob()...) {
		if !f.Error {
			warns = append(warns, f)
		}
	}
	return warns, nil
}

// RTOError is an RTO below the download time (Decision 5): the settings were accepted, nothing is printed or written.
type RTOError struct{ Target, Bound time.Duration }

// Error is Decision 5's impossible line.
func (e *RTOError) Error() string {
	return fmt.Sprintf("RTO %s is impossible: the download alone takes %s. Knobs: a faster or closer target (--speed), "+
		"a smaller scope (--size).", v1.Duration(e.Target), v1.Duration(e.Bound))
}

// ExitCode is funcdctl's exit code for the error, 3.
func (e *RTOError) ExitCode() int { return exitRTOImpossible }

// RTOBound is ⌈size ÷ speed⌉ seconds, the download alone, capped at v1alpha1.MaxDuration; size and speed are above 0.
func RTOBound(size, speed int64) time.Duration {
	secs := size / speed
	if size%speed != 0 {
		secs++
	}
	if secs > int64(v1.MaxDuration)/int64(time.Second) {
		return time.Duration(v1.MaxDuration)
	}
	return time.Duration(secs) * time.Second
}

// CheckRTO is (0, nil) without an RTO, else the bound and, when the RTO is below it, an *RTOError.
func CheckRTO(o Objectives) (time.Duration, error) {
	if o.RTO == 0 {
		return 0, nil
	}
	bound := RTOBound(o.Size, o.Speed)
	if o.RTO < bound {
		return bound, &RTOError{Target: o.RTO, Bound: bound}
	}
	return bound, nil
}
