// Package poll checks a test condition over time in the caller's goroutine (#950). testify's Never runs each check in
// a goroutine and returns on its timer without waiting for it, so a check that uses the test can fail it after its
// window, once the test's cleanup has stopped what the check reads.
package poll

import "time"

// Never reports whether cond stayed false for waitFor, checked every tick and once more after waitFor has passed. It
// returns false at the first true. No check runs after it returns.
func Never(cond func() bool, waitFor, tick time.Duration) bool {
	for end := time.Now().Add(waitFor); ; time.Sleep(tick) {
		if cond() {
			return false
		}
		if !time.Now().Before(end) {
			return true
		}
	}
}
