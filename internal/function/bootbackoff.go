package function

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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

// bootCrash is one replica's crash loop: how many boot crashes and Start failures in a row, the CreatedAt of the
// instance last counted, and the status message that crash gave.
type bootCrash struct {
	count   int
	counted time.Time
	message string

	startErr   error     // the last Start or worker-spec error, until the replica starts (ADR-0169)
	startAfter time.Time // when that replica may be started again
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

// count counts one boot crash of id that happened in the start at `at`, once per start, with message, and returns its
// record: a pooled member whose load timed out, judged on its pool host's /health/members rather than an exit.
func (b *bootBackoff) count(id runtime.InstanceID, at time.Time, message string) bootCrash {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.crashes[id]
	if c.count == 0 || !c.counted.Equal(at) {
		c.count++
		c.counted = at
		c.message = fmt.Sprintf("%s; boot crash %d in a row", message, c.count)
		b.crashes[id] = c
	}
	return c
}

// reread is when a pass reads again the pooled member whose load timed out, with crash record c: at its backoff
// deadline, a wait after the pool start counted; once that has passed, a wait from now, as the pool host does not
// reload the member before its next start.
func (b *bootBackoff) reread(c bootCrash, now time.Time) time.Time {
	w := b.wait(c.count)
	if due := c.counted.Add(w); due.After(now) {
		return due
	}
	return now.Add(w)
}

// timedOut counts running replica in, which did not listen within bootTimeout, as a boot crash, once per instance
// (ADR-0161 Decision 3). It is re-created no sooner than bootTimeout after its last start, so the message names
// max(wait, bootTimeout).
func (b *bootBackoff) timedOut(in runtime.Instance) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.crashes[in.ID]
	if c.count > 0 && c.counted.Equal(in.CreatedAt) {
		return
	}
	c.count++
	c.counted = in.CreatedAt
	c.message = fmt.Sprintf("replica %d did not listen within %s; boot crash %d in a row, retried %s after its last start",
		in.Replica, bootTimeout, c.count, max(b.wait(c.count), bootTimeout))
	b.crashes[in.ID] = c
	b.logger.Warn("a worker did not listen within the boot timeout", "namespace", in.Namespace, "name", in.Name,
		"replica", in.Replica, "count", c.count, "timeout", bootTimeout)
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

// startResult records starting replica id at now: a non-nil err counts a failure, remembers err and returns
// now + wait(count); nil clears startErr and startAfter (the count stays until the replica listens), zero time.
func (b *bootBackoff) startResult(id runtime.InstanceID, now time.Time, err error) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.crashes[id]
	if err == nil {
		if ok && c.startErr != nil {
			c.startErr, c.startAfter = nil, time.Time{}
			b.crashes[id] = c
		}
		return time.Time{}
	}
	c.count++
	c.startErr, c.startAfter = err, now.Add(b.wait(c.count))
	b.crashes[id] = c
	return c.startAfter
}

// held reports whether replica id still waits out a Start failure at now: its startAfter and startErr (zero/nil if not).
func (b *bootBackoff) held(id runtime.InstanceID, now time.Time) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.crashes[id]
	if !ok || c.startErr == nil || !now.Before(c.startAfter) {
		return time.Time{}, nil
	}
	return c.startAfter, c.startErr
}

// forget drops every entry whose ID starts with prefix: "<ns>/<name>/", or "<ns>/<name>/<rev>/" (the trailing '/'
// keeps revision fn-1 from matching fn-10).
func (b *bootBackoff) forget(prefix string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.crashes {
		if strings.HasPrefix(string(id), prefix) {
			delete(b.crashes, id)
		}
	}
}

// forgetStale drops every entry under prefix "<ns>/<name>/" whose revision is none of live: a replaced revision's
// replica that never got an instance (a worker-spec failure) has no worker for retire to reset (ADR-0169 Decision 4).
func (b *bootBackoff) forgetStale(prefix string, live ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.crashes {
		rest, ok := strings.CutPrefix(string(id), prefix)
		if !ok {
			continue
		}
		if rev, _, ok := strings.Cut(rest, "/"); ok && !slices.Contains(live, rev) {
			delete(b.crashes, id)
		}
	}
}

// backoffPrefix is the bootBackoff key prefix of every replica of Function name in ns (runtime.NewInstanceID).
func backoffPrefix(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name) + "/"
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
