// Package lifecycle runs the platform's providers through boot and stop in one order (ADR-0305). A provider implements
// Provider and any of Starter, Runner, BeforeStopper and Stopper; Phases runs each hook a provider has: Start in list
// order at boot, Run once every Start passed, BeforeStop and Stop in reverse at stop. It follows the idiom of the
// invoke chain (ADR-0302): a required interface, and optional ones a type assertion finds.
package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
)

// Provider is one platform provider; it also implements none or more of Starter, BeforeStopper and Stopper.
type Provider interface {
	Name() string
}

// Starter starts a provider at boot, before the platform starts its own loops. An error ends the boot there.
type Starter interface {
	Provider
	Start(ctx context.Context) error
}

// Runner runs a provider's long-running work on the platform's run context, from after every Start until that context
// ends. The platform waits for it with its own loops, before any provider stops.
type Runner interface {
	Provider
	Run(ctx context.Context)
}

// BeforeStopper runs when the daemon is about to close, before any provider stops.
type BeforeStopper interface {
	Provider
	BeforeStop(ctx context.Context) error
}

// Stopper releases what a provider holds.
type Stopper interface {
	Provider
	Stop(ctx context.Context) error
}

// Phases runs the hooks of its providers in their order. BeforeStop and Stop run for every provider, booted or not, so
// each must be safe when its Start never ran or failed: a New that fails stops the platform it was building, and a
// failed Start stops the providers after it too. It is safe for concurrent use; once stopping has begun, Start starts
// no further provider.
type Phases struct {
	providers []Provider

	mu       sync.Mutex
	stopping bool
}

// New returns the phases of providers, in their boot order.
func New(providers ...Provider) *Phases {
	return &Phases{providers: slices.Clone(providers)}
}

// Start runs each Starter in list order and returns the first error, naming its provider.
func (ph *Phases) Start(ctx context.Context) error {
	const op = "lifecycle.Start"
	for _, p := range ph.providers {
		ph.mu.Lock()
		stopping := ph.stopping
		ph.mu.Unlock()
		if stopping {
			return nil
		}
		if s, ok := p.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "start %s", p.Name())
			}
		}
	}
	return nil
}

// Run starts each Runner on ctx in its own goroutine, counted in wg, unless stopping has begun.
func (ph *Phases) Run(ctx context.Context, wg *sync.WaitGroup) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if ph.stopping {
		return
	}
	for _, p := range ph.providers {
		if r, ok := p.(Runner); ok {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.Run(ctx)
			}()
		}
	}
}

// Providers returns the providers in list order.
func (ph *Phases) Providers() []Provider {
	return slices.Clone(ph.providers)
}

// BeforeStop runs every BeforeStopper in reverse order and joins their errors.
func (ph *Phases) BeforeStop(ctx context.Context) error {
	return stopEach(ctx, ph, "lifecycle.BeforeStop", "before stop", BeforeStopper.BeforeStop)
}

// Stop runs every Stopper in reverse order and joins their errors.
func (ph *Phases) Stop(ctx context.Context) error {
	return stopEach(ctx, ph, "lifecycle.Stop", "stop", Stopper.Stop)
}

// stopEach marks ph stopping, then runs hook on every provider that implements H, in reverse order, and joins the
// errors.
func stopEach[H Provider](ctx context.Context, ph *Phases, op, verb string, hook func(H, context.Context) error) error {
	ph.mu.Lock()
	ph.stopping = true
	ph.mu.Unlock()
	var errs []error
	for i := len(ph.providers) - 1; i >= 0; i-- {
		if h, ok := ph.providers[i].(H); ok {
			if err := hook(h, ctx); err != nil {
				errs = append(errs, fault.Wrapf(err, fault.KindOf(err), op, "%s %s", verb, h.Name()))
			}
		}
	}
	return errors.Join(errs...)
}

// Hooks names each provider with the hooks it has, in list order, for the boot log: "executor: start, run, before
// stop, stop".
func (ph *Phases) Hooks() []string {
	out := make([]string, 0, len(ph.providers))
	for _, p := range ph.providers {
		var hooks []string
		if _, ok := p.(Starter); ok {
			hooks = append(hooks, "start")
		}
		if _, ok := p.(Runner); ok {
			hooks = append(hooks, "run")
		}
		if _, ok := p.(BeforeStopper); ok {
			hooks = append(hooks, "before stop")
		}
		if _, ok := p.(Stopper); ok {
			hooks = append(hooks, "stop")
		}
		out = append(out, p.Name()+": "+strings.Join(hooks, ", "))
	}
	return out
}
