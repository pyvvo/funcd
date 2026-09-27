# Implementation templates (copy-paste shapes)

Copy the shape that matches what the ADR's *Contracts* section asks for, then rename to the ADR's
real types and fill each body with the **real behavior the ADR specifies**. These shapes show the
*form* (package layout, signatures, error handling); the logic inside is yours to implement so the
Scenario tests pass (see `conventions.md`). Replace `NNNN` with the ADR number throughout.
Module path is `github.com/pyvvo/funcd`.

## Table of contents
1. Port interface (the abstraction, dependency-light)
2. One-file driver in its own subpackage
3. Contract suite (one per port)
4. Functional-options facade (`pkg/funcd`)
5. Internal component (deps-struct constructor)
6. `api/fault` error usage
7. Typed IDs / enums
8. Scenario test (one per ADR Scenario, passing)
9. `go.mod` dependency & tool edits

---

## 1. Port interface — `internal/<port>/<port>.go`

The port package holds the interface + shared types only. It must stay dependency-light (no driver
imports), so consumers and `pkg/funcd` can import it without pulling heavy deps.

```go
// Package widget is the <one-line purpose from the ADR> port.
package widget

import "context"

// Widget is the port. Drivers (subpackages) implement it; consumers depend on it.
type Widget interface {
	Get(ctx context.Context, id ID) (Item, error)
	Put(ctx context.Context, it Item) error
	List(ctx context.Context) ([]Item, error)
}

// Item is a shared type owned by the port (typed fields, no `any`).
type Item struct {
	ID   ID
	Name string
}

// ID is a typed identifier (no bare strings across the boundary).
type ID string
```

## 2. One-file driver — `internal/<port>/<driver>/<driver>.go`

One driver = one file in its own subpackage. The subpackage is *only* for keeping this driver's
third-party deps out of the port package — never an excuse to fan out into several files. Implement
the real behavior: the in-memory driver is the reference the contract suite proves every other
driver against, so it must actually work.

```go
// Package memory is the in-memory driver for the widget port.
package memory

import (
	"context"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/widget"
)

// New returns the in-memory widget driver. Driver constructors return the PORT interface
// (so the facade can swap them), unlike other constructors which return a concrete struct.
func New() widget.Widget { return &store{items: map[widget.ID]widget.Item{}} }

type store struct {
	mu    sync.RWMutex
	items map[widget.ID]widget.Item
}

func (s *store) Get(ctx context.Context, id widget.ID) (widget.Item, error) {
	if err := ctx.Err(); err != nil {
		return widget.Item{}, fault.Wrapf(err, fault.Unavailable, "widget.Get", "context done")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[id]
	if !ok {
		return widget.Item{}, fault.NotFoundf("widget.Get", "no widget with id %q", id)
	}
	return it, nil
}

func (s *store) Put(ctx context.Context, it widget.Item) error {
	if err := ctx.Err(); err != nil {
		return fault.Wrapf(err, fault.Unavailable, "widget.Put", "context done")
	}
	if it.ID == "" {
		return fault.Invalidf("widget.Put", "id must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[it.ID] = it
	return nil
}

func (s *store) List(ctx context.Context) ([]widget.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "widget.List", "context done")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]widget.Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, it)
	}
	return out, nil
}
```

A driver wrapping one library that spans backends (e.g. `gocloud.dev/blob` → memory/file/S3) is
still ONE file; the backend is chosen by URL/config inside `New`, not by extra files.

## 3. Contract suite — `internal/<port>/<port>contract/contract.go`

One reusable suite per port; every driver's test calls it, proving the in-memory driver behaves like
the real one. Carry the real assertions the ADR's Contracts imply.

```go
// Package widgetcontract is the shared conformance suite for any widget.Widget driver.
package widgetcontract

import (
	"context"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/widget"
)

// RunContract runs the identical assertions against any driver. newWidget builds a fresh instance.
func RunContract(t *testing.T, newWidget func(t *testing.T) widget.Widget) {
	t.Helper()
	ctx := context.Background()
	w := newWidget(t)

	// missing → NotFound
	if _, err := w.Get(ctx, "absent"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("Get(absent): want NotFound, got %v", err)
	}
	// put then get round-trips
	want := widget.Item{ID: "a", Name: "alpha"}
	if err := w.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := w.Get(ctx, "a")
	if err != nil || got != want {
		t.Fatalf("Get(a): got (%v, %v), want (%v, nil)", got, err, want)
	}
	// list reflects the put
	items, err := w.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("List: got (%v, %v), want 1 item", items, err)
	}
}
```

## 4. Functional-options facade — `pkg/funcd/{funcd.go,options.go,presets.go}`

Functional options ONLY here (and on driver constructors meant for selection). `New` validates
required deps and returns a typed `fault.Invalid` — never a partial platform, never `panic`.

```go
// pkg/funcd/options.go
package funcd

import (
	"log/slog"

	"github.com/pyvvo/funcd/internal/widget"
)

type config struct {
	widget widget.Widget
	logger *slog.Logger
}

// Option configures the platform. Add one With* per injectable dependency.
type Option func(*config) error

func WithWidget(w widget.Widget) Option { return func(c *config) error { c.widget = w; return nil } }
func WithLogger(l *slog.Logger) Option  { return func(c *config) error { c.logger = l; return nil } }
```

```go
// pkg/funcd/funcd.go
package funcd

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// Platform is the embeddable funcd instance.
type Platform struct{ cfg config }

// New builds a Platform from options, validating required dependencies.
func New(opts ...Option) (*Platform, error) {
	var c config
	for _, opt := range opts {
		if err := opt(&c); err != nil {
			return nil, err
		}
	}
	if c.widget == nil {
		return nil, fault.Invalidf("funcd.New", "missing required dependency: widget")
	}
	return &Platform{cfg: c}, nil
}

// Run starts the platform's subsystems and blocks until ctx is cancelled (implement per the ADR).
func (p *Platform) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// Shutdown gracefully stops the platform (implement per the ADR).
func (p *Platform) Shutdown(ctx context.Context) error { return nil }
```

```go
// pkg/funcd/presets.go
package funcd

// InMemory wires every port to its in-memory driver (tests, e2e). Real wiring at implementation.
func InMemory() Option { return func(c *config) error { return nil } }
```

## 5. Internal component — explicit deps struct, NO options

```go
package controller

import (
	"context"
	"errors"
	"log/slog"

	"github.com/pyvvo/funcd/internal/widget"
)

// Deps are the explicit dependencies of the controller (no functional options inside internal/).
type Deps struct {
	Widget widget.Widget
	Logger *slog.Logger // already namespaced by the app
}

type Controller struct{ deps Deps }

func New(d Deps) (*Controller, error) {
	if d.Widget == nil {
		return nil, errors.New("controller: Widget is required")
	}
	return &Controller{deps: d}, nil
}

func (c *Controller) Reconcile(ctx context.Context) error {
	// implement the reconcile logic per the ADR; honor ctx cancellation.
	return ctx.Err()
}
```

## 6. `api/fault` usage (you import it; you don't redefine error kinds)

```go
import "github.com/pyvvo/funcd/api/fault"

// construct
return fault.NotFoundf("widget.Get", "no widget with id %q", id)
return fault.Invalidf("widget.Put", "name must not be empty")

// wrap a lower error, keep the chain
if err != nil {
	return fault.Wrapf(err, fault.Unavailable, "widget.List", "backend unreachable")
}

// inspect (only at the edge / where a decision depends on it)
switch fault.KindOf(err) {
case fault.NotFound:
	// …
}
```

If `api/fault` does not exist yet, ADR-0002 is not implemented — that is a prerequisite (Step 0).

## 7. Typed IDs / enums — `api/types/v1alpha1/{ids.go,enums.go}`

```go
package v1alpha1

import "github.com/pyvvo/funcd/api/fault"

// IDs / names — no bare strings across boundaries.
type NamespaceName string
type FunctionName string

// Validate enforces the rule the ADR specifies (here: a non-empty name).
func (n NamespaceName) Validate() error {
	if n == "" {
		return fault.Invalidf("NamespaceName.Validate", "must not be empty")
	}
	return nil
}

// Enum — typed constant set with Validate()/String().
type Phase string

const (
	PhasePending Phase = "Pending"
	PhaseReady   Phase = "Ready"
	PhaseFailed  Phase = "Failed"
)

func (p Phase) String() string { return string(p) }
func (p Phase) Validate() error {
	switch p {
	case PhasePending, PhaseReady, PhaseFailed:
		return nil
	default:
		return fault.Invalidf("Phase.Validate", "unknown phase %q", p)
	}
}
```

## 8. Scenario test (one per ADR Scenario, passing) — `<pkg>/<thing>_test.go`

Name the test after the scenario so traceability is grep-able. Exercise the real behavior and assert
the outcome — the test must pass against the implemented code (no `t.Skip`).

```go
package funcd_test

import (
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// scenario: facade-missing-dep  (ADR-NNNN)
func TestScenario_FacadeMissingDep(t *testing.T) {
	_, err := funcd.New() // no required deps
	if fault.KindOf(err) != fault.Invalid {
		t.Fatalf("New() with no deps: want Invalid, got %v", err)
	}
}
```

## 9. `go.mod` edits (run, don't hand-edit)

```bash
# library dependency (pin a version; commits go.sum)
go get gocloud.dev@v0.40.0

# codegen / lint tool (Go 1.24 tool directive — ADR-0001)
go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.0

go mod tidy
```

Record the resolved versions in the implementation report. Add only deps the ADR sanctioned.
