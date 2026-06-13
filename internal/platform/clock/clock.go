// Package clock is the canonical injected-dependency example (ADR-0002).
// It provides the Clock interface so components can be tested with a
// deterministic fake instead of wall-clock time.
package clock

import "time"

// Clock is the port for time operations. Components depend on this interface;
// the app injects System() in production and a Fake in tests.
type Clock interface {
	Now() time.Time
}

// system is the real wall clock.
type system struct{}

func (system) Now() time.Time { return time.Now() }

// System returns the real-time clock.
func System() Clock { return system{} }

// Fake returns a clock fixed at the given time — useful for deterministic tests.
func Fake(t time.Time) Clock { return &fake{t: t} }

type fake struct {
	t time.Time
}

func (f *fake) Now() time.Time { return f.t }
