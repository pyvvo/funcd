// Package clock is the canonical injected-dependency example (ADR-0002).
// It provides the Clock interface so components can be tested with a
// deterministic fake instead of wall-clock time.
package clock

import (
	"sync"
	"time"
)

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
func Fake(t time.Time) Clock { return NewManual(t) }

// Manual is a fake clock that stands still until Advance moves it, for tests of time-bounded behavior. Safe for
// concurrent use.
type Manual struct {
	mu sync.Mutex
	t  time.Time
}

// NewManual returns a Manual clock standing at t.
func NewManual(t time.Time) *Manual { return &Manual{t: t} }

// Now returns the clock's current time.
func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t
}

// Advance moves the clock forward by d.
func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.t = m.t.Add(d)
}
