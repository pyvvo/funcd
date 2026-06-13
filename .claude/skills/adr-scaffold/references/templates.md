# Scaffold templates (copy-paste shapes)

Copy the shape that matches what the ADR's *Contracts* section asks for, then rename to the ADR's
real types. Every body is a **not-implemented stub** that still compiles and lints (see
`conventions.md` — no `panic`, return `errors.New("not implemented: ADR-NNNN")`). Replace `NNNN`
with the ADR number throughout. Module path is `github.com/green-0-rabbit/funcd`.

## Table of contents
1. Port interface (the abstraction, dependency-light)
2. One-file driver in its own subpackage
3. Contract suite stub (one per port)
4. Functional-options facade (`pkg/funcd`)
5. Internal component (deps-struct constructor)
6. `api/fault` error usage
7. Typed IDs / enums
8. Scenario test skeleton (skipped)
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
third-party deps out of the port package — never an excuse to fan out into several files.

```go
// Package memory is the in-memory driver for the widget port.
package memory

import (
	"context"
	"errors"

	"github.com/green-0-rabbit/funcd/internal/widget"
)

// New returns the in-memory widget driver. Driver constructors return the PORT interface
// (so the facade can swap them), unlike other constructors which return a concrete struct.
func New() widget.Widget { return &store{} }

type store struct{ /* fields added at implementation time */ }

func (s *store) Get(ctx context.Context, id widget.ID) (widget.Item, error) {
	return widget.Item{}, errors.New("not implemented: ADR-NNNN")
}
func (s *store) Put(ctx context.Context, it widget.Item) error {
	return errors.New("not implemented: ADR-NNNN")
}
func (s *store) List(ctx context.Context) ([]widget.Item, error) {
	return nil, errors.New("not implemented: ADR-NNNN")
}
```

A driver wrapping one library that spans backends (e.g. `gocloud.dev/blob` → memory/file/S3) is
still ONE file; the backend is chosen by URL/config inside `New`, not by extra files.

## 3. Contract suite stub — `internal/<port>/<port>contract/contract.go`

One reusable suite per port; every driver's test calls it, proving the in-memory driver behaves like
the real one. Stub now, fill assertions at implementation.

```go
// Package widgetcontract is the shared conformance suite for any widget.Widget driver.
package widgetcontract

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/widget"
)

// RunContract runs the identical assertions against any driver. newWidget builds a fresh instance.
func RunContract(t *testing.T, newWidget func(t *testing.T) widget.Widget) {
	t.Helper()
	t.Skip("scaffold ADR-NNNN — contract assertions land at implementation")
	_ = newWidget // referenced so the signature is exercised and drifts loudly
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

	"github.com/green-0-rabbit/funcd/internal/widget"
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
	"errors"

	"github.com/green-0-rabbit/funcd/api/fault"
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

func (p *Platform) Run(ctx context.Context) error      { return errors.New("not implemented: ADR-NNNN") }
func (p *Platform) Shutdown(ctx context.Context) error { return errors.New("not implemented: ADR-NNNN") }
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

	"github.com/green-0-rabbit/funcd/internal/widget"
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
	return errors.New("not implemented: ADR-NNNN")
}
```

## 6. `api/fault` usage (you import it; you don't redefine error kinds)

```go
import "github.com/green-0-rabbit/funcd/api/fault"

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

If `api/fault` does not exist yet, ADR-0002 is not scaffolded — that is a prerequisite (Step 0).

## 7. Typed IDs / enums — `api/types/v1alpha1/{ids.go,enums.go}`

```go
package v1alpha1

import "github.com/green-0-rabbit/funcd/api/fault"

// IDs / names — no bare strings across boundaries.
type NamespaceName string
type FunctionName string

func (n NamespaceName) Validate() error { return nil } // real DNS-label rule at implementation

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

## 8. Scenario test skeleton (one per ADR Scenario, skipped) — `<pkg>/<thing>_test.go`

Name the test after the scenario so traceability is grep-able. Reference the real signatures so the
skeleton breaks loudly if the contract drifts — then skip.

```go
package funcd_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/pkg/funcd"
)

// scenario: facade-missing-dep  (ADR-NNNN)
func TestScenario_FacadeMissingDep(t *testing.T) {
	t.Skip("scaffold ADR-NNNN — scenario: facade-missing-dep")

	_, err := funcd.New() // no required deps
	if err == nil {
		t.Fatal("expected an error when a required dependency is missing")
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

Record the resolved versions in the scaffold report. Add only deps the ADR sanctioned.
