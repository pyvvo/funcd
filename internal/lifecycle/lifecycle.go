// Package lifecycle runs the platform's providers through boot and stop in one order (ADR-0305). A provider implements
// Provider and any of Starter, Reclaimer, Runner, BeforeStopper and Stopper; Phases runs each hook a provider has:
// Start, then Reclaim, in list order at boot, Run once every Start passed, BeforeStop and Stop in reverse at stop. It
// follows the idiom of the invoke chain (ADR-0302): a required interface, and optional ones a type assertion finds. A
// shared hook is such an optional interface of the package that builds the list, walked with Each and named in the
// boot log with Named (ADR-0308).
package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
)

// Provider is one platform provider; it also implements none or more of Starter, Reclaimer, Runner, BeforeStopper and
// Stopper.
type Provider interface {
	Name() string
}

// Starter starts a provider at boot, before the platform starts its own loops. An error ends the boot there.
type Starter interface {
	Provider
	Start(ctx context.Context) error
}

// Reclaimer reclaims at boot, after every Start, what its provider's resources deleted before their reconcile ran
// still hold (ADR-0307). The platform runs it only on a boot that is not held (ADR-0206 Decision 6).
type Reclaimer interface {
	Provider
	Reclaim(ctx context.Context) error
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
// failed Start stops the providers after it too. It is safe for concurrent use; once stopping has begun, Start and
// Reclaim run no further provider.
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
	return forEach(ctx, ph, "lifecycle.Start", "start", Starter.Start, true)
}

// Reclaim runs each Reclaimer in list order and joins their errors, naming each provider.
func (ph *Phases) Reclaim(ctx context.Context) error {
	return forEach(ctx, ph, "lifecycle.Reclaim", "reclaim", Reclaimer.Reclaim, false)
}

// forEach runs hook on every provider that implements H, in list order, until stopping begins. With first it returns
// the first error, else every error joined.
func forEach[H Provider](ctx context.Context, ph *Phases, op, verb string, hook func(H, context.Context) error, first bool) error {
	var errs []error
	for _, p := range ph.providers {
		if ph.isStopping() {
			break
		}
		h, ok := p.(H)
		if !ok {
			continue
		}
		if err := hook(h, ctx); err != nil {
			err = fault.Wrapf(err, fault.KindOf(err), op, "%s %s", verb, h.Name())
			if first {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// isStopping reports whether a stop phase has begun.
func (ph *Phases) isStopping() bool {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return ph.stopping
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

// Each returns the providers that implement H, in list order: the walk a shared hook of another package runs.
func Each[H Provider](ph *Phases) []H {
	var out []H
	for _, p := range ph.providers {
		if h, ok := p.(H); ok {
			out = append(out, h)
		}
	}
	return out
}

// Hook is an optional provider interface under its boot-log name.
type Hook struct {
	name string
	has  func(Provider) bool
}

// Named is the hook H, logged as name.
func Named[H Provider](name string) Hook {
	return Hook{name: name, has: func(p Provider) bool {
		_, ok := p.(H)
		return ok
	}}
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

// Hooks names each provider with the hooks it has, then each of shared it implements, in list order, for the boot log:
// "executor: start, run, before stop, stop".
func (ph *Phases) Hooks(shared ...Hook) []string {
	all := append([]Hook{Named[Starter]("start"), Named[Reclaimer]("reclaim"), Named[Runner]("run"),
		Named[BeforeStopper]("before stop"), Named[Stopper]("stop")}, shared...)
	out := make([]string, 0, len(ph.providers))
	for _, p := range ph.providers {
		var hooks []string
		for _, h := range all {
			if h.has(p) {
				hooks = append(hooks, h.name)
			}
		}
		out = append(out, p.Name()+": "+strings.Join(hooks, ", "))
	}
	return out
}
