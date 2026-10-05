package workerpipe

import "time"

// SetClock makes o read the time from now, which also restarts its drop-Warn interval.
func SetClock(o *Output, now func() time.Time) {
	o.mu.Lock()
	o.now, o.lastWarn = now, now()
	o.mu.Unlock()
}
