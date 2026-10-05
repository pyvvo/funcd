package function

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pyvvo/funcd/internal/runtime"
)

// The boot-crash backoff defaults (ADR-0160 Decision 7), as the kubelet's CrashLoopBackOff.
const (
	defaultBootBackoffInitial = 10 * time.Second
	defaultBootBackoffMax     = 5 * time.Minute
)

// reasonCrashLoop is the Ready and RevisionReady reason of a replica that crashed while booting (ADR-0160 Decision 6).
const reasonCrashLoop = "CrashLoopBackOff"

// shapeErrorCode is the exit code of a shim that cannot load its handler (ADR-0037).
const shapeErrorCode = 3

// exitClass is how the reconciler reads a terminal solo replica (ADR-0160 Decision 3).
type exitClass int

const (
	exitStopped      exitClass = iota // Stop ended it or ran after
	exitAfterServing                  // it listened, then ended: replaced after the period
	exitShapeError                    // exit 3 before listening in a pass not serving: ShapeInvalid
	exitBootCrash                     // any other end before listening: re-created after a growing wait
)

// classifyExit reads how terminal replica in ended. In the legacy placeholder mode no port file is written, so any end
// on its own is exitAfterServing.
func classifyExit(in runtime.Instance, serving, legacy bool) exitClass {
	switch {
	case in.Exit.Cause == runtime.ExitByStop:
		return exitStopped
	case legacy || in.Listened:
		return exitAfterServing
	case in.Exit.Cause == runtime.ExitByCode && in.Exit.Code == shapeErrorCode && !serving:
		return exitShapeError
	}
	return exitBootCrash
}

// bootBackoff counts each solo replica's consecutive boot crashes, in memory and keyed by instance ID, so a new
// revision starts at zero and a daemon restart forgets them (ADR-0160 Decision 5).
type bootBackoff struct {
	mu             sync.Mutex
	initial, limit time.Duration
	crashes        map[runtime.InstanceID]bootCrash
	logger         *slog.Logger
}

// bootCrash is one replica's crash loop: how many boot crashes in a row, the CreatedAt of the instance last counted,
// and the status message that crash gave.
type bootCrash struct {
	count   int
	counted time.Time
	message string
}

func newBootBackoff(initial, limit time.Duration, logger *slog.Logger) *bootBackoff {
	return &bootBackoff{initial: initial, limit: limit, crashes: map[runtime.InstanceID]bootCrash{}, logger: logger}
}

// observe records how terminal replica in ended, as class, and returns its crash record and when it may be re-created.
// A boot crash is counted once per instance, and a counted crash Stop ran after keeps its wait; both return a zero
// record and time otherwise. An end after listening clears the count.
func (b *bootBackoff) observe(in runtime.Instance, class exitClass) (bootCrash, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.crashes[in.ID]
	switch class {
	case exitAfterServing:
		delete(b.crashes, in.ID)
	case exitStopped:
		if ok && c.counted.Equal(in.CreatedAt) {
			return c, in.CreatedAt.Add(b.wait(c.count))
		}
	case exitBootCrash:
		if !ok || !c.counted.Equal(in.CreatedAt) {
			c.count++
			c.counted = in.CreatedAt
			c.message = fmt.Sprintf("replica %d %s before it listened; boot crash %d in a row, retried %s after its last start",
				in.Replica, describeExit(in.Exit), c.count, b.wait(c.count))
			b.crashes[in.ID] = c
			b.logger.Warn("a worker ended before it listened", "namespace", in.Namespace, "name", in.Name,
				"replica", in.Replica, "count", c.count, "exit", describeExit(in.Exit))
		}
		return c, in.CreatedAt.Add(b.wait(c.count))
	}
	return bootCrash{}, time.Time{}
}

// crash is the crash record of id, if it has one.
func (b *bootBackoff) crash(id runtime.InstanceID) (bootCrash, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.crashes[id]
	return c, ok
}

// wait is the wait after the n-th consecutive boot crash: min(initial · 2^(n−1), limit), doubled while below limit so
// it never overflows.
func (b *bootBackoff) wait(n int) time.Duration {
	w := b.initial
	for i := 1; i < n && w < b.limit; i++ {
		if w > b.limit/2 {
			w = b.limit
			break
		}
		w *= 2
	}
	return min(w, b.limit)
}

// reset forgets id's crash loop.
func (b *bootBackoff) reset(id runtime.InstanceID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.crashes, id)
}

// describeExit says how a replica ended, for the status message and the log.
func describeExit(e runtime.Exit) string {
	switch e.Cause {
	case runtime.ExitByCode:
		return fmt.Sprintf("exited with code %d", e.Code)
	case runtime.ExitBySignal:
		return fmt.Sprintf("was killed by signal %d", e.Signal)
	}
	return "ended with an unknown exit status"
}

// earlier is the sooner of two deadlines, where zero means none.
func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}
