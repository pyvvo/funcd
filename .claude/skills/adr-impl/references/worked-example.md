# Worked example — a complete minimal implementation, end to end

The fragments in `templates.md` are pieces; this shows the **whole shape** for one small, fictional
ADR so you can copy the structure when the pieces alone aren't enough. It is deliberately tiny: one
port, one in-memory driver, one facade option, one passing scenario test — real behavior that builds,
lints, and passes.

> Fictional input: **ADR-0099 — Token-bucket rate limiter port.**
> Contracts: a `ratelimit.Limiter` port with `Allow(ctx, key) (bool, error)`; an in-memory driver;
> a `funcd.WithRateLimiter` option.
> Scenario: `scenario: denies-over-budget` — Given a limiter at its budget, when `Allow` is called
> again for the same key, then it returns `false`.
> Implementation plan files: the five below. Deps: none.

## Files produced

```
internal/ratelimit/ratelimit.go                       # the port
internal/ratelimit/memory/memory.go                   # one-file in-memory driver (real)
internal/ratelimit/ratelimitcontract/contract.go      # shared conformance suite (real assertions)
internal/ratelimit/memory/memory_test.go              # scenario: denies-over-budget (passing, via contract)
pkg/funcd/options.go                                   # + WithRateLimiter (shown as an additive edit)
```

### `internal/ratelimit/ratelimit.go`
```go
// Package ratelimit is the token-bucket rate-limiter port (ADR-0099).
package ratelimit

import "context"

// Limiter decides whether an action keyed by `key` may proceed under its budget.
type Limiter interface {
	Allow(ctx context.Context, key Key) (bool, error)
}

// Key identifies a bucket (typed, not a bare string).
type Key string
```

### `internal/ratelimit/memory/memory.go`
```go
// Package memory is the in-memory driver for the ratelimit port (ADR-0099).
package memory

import (
	"context"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/ratelimit"
)

// New returns the in-memory limiter driver with a fixed budget of allows per key.
// Driver constructors return the port interface.
func New(budget int) ratelimit.Limiter {
	return &limiter{budget: budget, used: map[ratelimit.Key]int{}}
}

type limiter struct {
	mu     sync.Mutex
	budget int
	used   map[ratelimit.Key]int
}

func (l *limiter) Allow(ctx context.Context, key ratelimit.Key) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fault.Wrapf(err, fault.Unavailable, "ratelimit.Allow", "context done")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used[key] >= l.budget {
		return false, nil
	}
	l.used[key]++
	return true, nil
}
```

### `internal/ratelimit/ratelimitcontract/contract.go`
```go
// Package ratelimitcontract is the shared conformance suite for any ratelimit.Limiter driver.
package ratelimitcontract

import (
	"context"
	"testing"

	"github.com/pyvvo/funcd/internal/ratelimit"
)

// RunContract runs identical assertions against any driver. newLimiter builds a fresh
// limiter with the given budget.
func RunContract(t *testing.T, newLimiter func(t *testing.T, budget int) ratelimit.Limiter) {
	t.Helper()
	ctx := context.Background()
	lim := newLimiter(t, 1)

	// first call is within budget → allowed
	ok, err := lim.Allow(ctx, "k")
	if err != nil || !ok {
		t.Fatalf("Allow #1: got (%v, %v), want (true, nil)", ok, err)
	}
	// second call is over budget → denied
	ok, err = lim.Allow(ctx, "k")
	if err != nil || ok {
		t.Fatalf("Allow #2: got (%v, %v), want (false, nil)", ok, err)
	}
}
```

### `pkg/funcd/options.go` (additive — add the field + the With func to the existing file)
```go
// add to the config struct:
//   ratelimiter ratelimit.Limiter
// add the import:
//   "github.com/pyvvo/funcd/internal/ratelimit"

// WithRateLimiter injects the rate-limiter driver.
func WithRateLimiter(l ratelimit.Limiter) Option {
	return func(c *config) error { c.ratelimiter = l; return nil }
}
```

### `internal/ratelimit/memory/memory_test.go`
```go
package memory_test

import (
	"testing"

	"github.com/pyvvo/funcd/internal/ratelimit"
	"github.com/pyvvo/funcd/internal/ratelimit/memory"
	"github.com/pyvvo/funcd/internal/ratelimit/ratelimitcontract"
)

// scenario: denies-over-budget  (ADR-0099)
func TestScenario_DeniesOverBudget(t *testing.T) {
	ratelimitcontract.RunContract(t, func(t *testing.T, budget int) ratelimit.Limiter {
		return memory.New(budget)
	})
}
```
> Note: this driver-level test lives beside the driver, so it may import `internal/**` and run the
> shared contract suite against the real driver — proving the scenario passes. A *facade-level*
> scenario goes under `tests/e2e/**`, which depguard restricts to `pkg/**` + `api/**`: exercise it
> through the public surface — `funcd.New(...)` wired to a preset that selects the in-memory driver
> internally — never by importing `internal/**` from an e2e test. Both layers are legitimate; pick
> the driver level for per-driver conformance and the facade level for end-to-end behavior.

## Verifying green

```bash
just build      # compiles
just lint       # clean: no panic, no any-in-API, import graph respected
just test       # PASS — TestScenario_DeniesOverBudget runs and passes
```

## What this example demonstrates (the transferable shape)

- Port in its own dependency-light package; **one-file** driver in a subpackage; constructor returns
  the port interface.
- Contract suite carries real assertions (one per port) — runnable by every driver, proving fake ≡ real.
- Facade option added the functional-options way; nothing inside `internal/` grows a `With*`.
- One passing test per Scenario, named after the scenario, running the contract suite against the
  real driver so the behavior is actually proven.
- Real behavior, lint-clean: typed errors via `api/fault` (never `panic`, never a shipped
  `not implemented`); the whole thing builds, lints, and passes.
- The import-graph caveat is real — keep `tests/e2e/**` on `pkg/**` + `api/**`; put driver-level
  proofs beside the driver and run facade-level scenarios through the public surface.
