# Worked example — a complete minimal scaffold, end to end

The fragments in `templates.md` are pieces; this shows the **whole shape** for one small, fictional
ADR so you can copy the structure when the pieces alone aren't enough. It is deliberately tiny: one
port, one in-memory driver, one facade option, one skipped scenario test — and it compiles and lints.

> Fictional input: **ADR-0099 — Token-bucket rate limiter port.**
> Contracts: a `ratelimit.Limiter` port with `Allow(ctx, key) (bool, error)`; an in-memory driver;
> a `funcd.WithRateLimiter` option.
> Scenario: `scenario: denies-over-budget` — Given a limiter at its budget, when `Allow` is called
> again for the same key, then it returns `false`.
> Scaffold plan files: the five below. Deps: none.

## Files produced

```
internal/ratelimit/ratelimit.go                       # the port
internal/ratelimit/memory/memory.go                   # one-file in-memory driver
internal/ratelimit/ratelimitcontract/contract.go      # shared conformance suite (stub)
pkg/funcd/options.go                                   # + WithRateLimiter (shown as an additive edit)
tests/e2e/ratelimit_test.go                            # scenario: denies-over-budget (skipped)
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
	"errors"

	"github.com/green-0-rabbit/funcd/internal/ratelimit"
)

// New returns the in-memory limiter driver. Driver constructors return the port interface.
func New() ratelimit.Limiter { return &limiter{} }

type limiter struct{ /* buckets added at implementation time */ }

func (l *limiter) Allow(ctx context.Context, key ratelimit.Key) (bool, error) {
	return false, errors.New("not implemented: ADR-0099")
}
```

### `internal/ratelimit/ratelimitcontract/contract.go`
```go
// Package ratelimitcontract is the shared conformance suite for any ratelimit.Limiter driver.
package ratelimitcontract

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/ratelimit"
)

// RunContract runs identical assertions against any driver.
func RunContract(t *testing.T, newLimiter func(t *testing.T) ratelimit.Limiter) {
	t.Helper()
	t.Skip("scaffold ADR-0099 — contract assertions land at implementation")
	_ = newLimiter
}
```

### `pkg/funcd/options.go` (additive — add the field + the With func to the existing file)
```go
// add to the config struct:
//   ratelimiter ratelimit.Limiter
// add the import:
//   "github.com/green-0-rabbit/funcd/internal/ratelimit"

// WithRateLimiter injects the rate-limiter driver.
func WithRateLimiter(l ratelimit.Limiter) Option {
	return func(c *config) error { c.ratelimiter = l; return nil }
}
```

### `tests/e2e/ratelimit_test.go`
```go
package e2e_test

import (
	"context"
	"testing"

	"github.com/green-0-rabbit/funcd/internal/ratelimit"
	"github.com/green-0-rabbit/funcd/internal/ratelimit/memory"
)

// scenario: denies-over-budget  (ADR-0099)
func TestScenario_DeniesOverBudget(t *testing.T) {
	t.Skip("scaffold ADR-0099 — scenario: denies-over-budget")

	var lim ratelimit.Limiter = memory.New()
	// the real test will exhaust the budget, then assert Allow == false:
	ok, err := lim.Allow(context.Background(), "k")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	_ = ok
}
```
> Note: this e2e test imports `internal/**`, which the depguard rule forbids for `tests/e2e/**`
> (only `pkg/**` + `api/**` allowed). In a real scaffold the scenario is exercised *through the
> facade* — `funcd.New(funcd.WithRateLimiter(memory.New()))` and a public method — keeping the e2e
> test on the public surface. Shown here in the simpler internal form only to keep the example
> short; honor the import graph in real scaffolds (place a driver-level test under
> `internal/ratelimit/...` and the facade-level scenario under `tests/e2e/`).

## Verifying green

```bash
just build      # compiles
just lint       # clean: no panic, no any-in-API, import graph respected
just test       # PASS — TestScenario_DeniesOverBudget reported as SKIP
```

## What this example demonstrates (the transferable shape)

- Port in its own dependency-light package; **one-file** driver in a subpackage; constructor returns
  the port interface.
- Contract suite stub present (one per port) — proves fake ≡ real later.
- Facade option added the functional-options way; nothing inside `internal/` grows a `With*`.
- One skipped test per Scenario, named after the scenario, referencing real signatures so it breaks
  loudly on contract drift.
- Every body is a not-implemented stub (`errors.New`, never `panic`); the whole thing builds and
  lints with zero business logic.
- The import-graph caveat is real — keep `tests/e2e/**` on `pkg/**` + `api/**`; put driver-level
  skeletons beside the driver.
