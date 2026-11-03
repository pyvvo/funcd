package funcd

import (
	"context"

	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
)

// describer is a provider that names its own entries in the provider catalog (ADR-0082).
type describer interface {
	Descriptors() []provider.Descriptor
}

// providerCatalog builds the platform's provider catalog (ADR-0082) from its providers, each naming its own entries,
// so a provider added to the list is in the catalog and the catalog cannot drift from what runs. The ingress is the
// Platform's own and stays listed until its place is decided. `bus` is intentionally absent: it is the internal-plane
// messaging substrate, never function-bindable. The F54 observability read path (ADR-0084) is in-daemon substrate
// serving the log-ingest capability, not a provider of its own.
func providerCatalog(ph *lifecycle.Phases) (*provider.Catalog, error) {
	ds := []provider.Descriptor{{Name: "ingress", Kind: provider.Builtin, Port: "gateway.Gateway", Summary: "HTTP ingress gateway"}}
	for _, pr := range ph.Providers() {
		if d, ok := pr.(describer); ok {
			ds = append(ds, d.Descriptors()...)
		}
	}
	return provider.New(ds...)
}

// Providers returns the platform's provider catalog (ADR-0082), available after New.
func (p *Platform) Providers() *provider.Catalog { return p.providers }

// logProviders emits the provider catalog once at startup — the first consumer of the provider catalog's
// legibility (ADR-0082): built-in vs add-on provider names.
func (p *Platform) logProviders(ctx context.Context) {
	p.logger.InfoContext(ctx, "platform providers",
		"builtin", providerNames(p.providers.ByKind(provider.Builtin)),
		"addon", providerNames(p.providers.ByKind(provider.Addon)),
	)
}

func providerNames(ds []provider.Descriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}
