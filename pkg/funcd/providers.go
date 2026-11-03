package funcd

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// describer is a provider that names its own entries in the provider catalog (ADR-0082).
type describer interface {
	lifecycle.Provider
	Descriptors() []provider.Descriptor
}

// The shared hooks (ADR-0308): each returns a provider's part of a cross-cutting feature, which the Platform puts in
// place in list order, as providerCatalog puts the descriptors. Quotas, Dependencies and Facet run once, while New
// builds the platform, after the last store facade exists; Admissions runs when the control-plane chain is built and on
// every App write. No hook runs at boot or at stop.

// quotaHook is a provider whose stores a size quota caps: the quota Source of each kind it serves (ADR-0292).
type quotaHook interface {
	lifecycle.Provider
	Quotas() map[v1.Kind]quota.Source
}

// admissionHook is a provider whose resources a write must pass rules for: its admissions over r, the store or an
// App's view of it (ADR-0199 Decision 3). An App write builds them again, so Admissions only builds values from parts
// New set: no I/O, no state, safe to call concurrently.
type admissionHook interface {
	lifecycle.Provider
	Admissions(r admission.StoreReader) []admission.Admission
}

// dependencyHook is a provider whose bindings a worker's dependency check reads (ADR-0215 Decision 3).
type dependencyHook interface {
	lifecycle.Provider
	Dependencies() []health.Binding
}

// facetHook is a provider whose binding a worker calls on its local API: its facet there (ADR-0310), which only
// internal/workernode/local builds.
type facetHook interface {
	lifecycle.Provider
	Facet() local.Facet // the zero Facet when the provider's facade is nil
}

// sharedHooks names the shared hooks in the boot log.
func sharedHooks() []lifecycle.Hook {
	return []lifecycle.Hook{lifecycle.Named[quotaHook]("quotas"), lifecycle.Named[admissionHook]("admissions"),
		lifecycle.Named[dependencyHook]("dependencies"), lifecycle.Named[facetHook]("facets")}
}

// registerQuotas registers each provider's quota Sources in the Book.
func (p *Platform) registerQuotas() {
	for _, h := range lifecycle.Each[quotaHook](p.phases) {
		for kind, src := range h.Quotas() {
			p.quotas.Register(kind, src)
		}
	}
}

// providerAdmissions are the providers' admissions over r, in list order.
func (p *Platform) providerAdmissions(r admission.StoreReader) []admission.Admission {
	var out []admission.Admission
	for _, h := range lifecycle.Each[admissionHook](p.phases) {
		out = append(out, h.Admissions(r)...)
	}
	return out
}

// dependencyChecks are the providers' binding checks, in list order.
func (p *Platform) dependencyChecks() []health.Binding {
	var out []health.Binding
	for _, h := range lifecycle.Each[dependencyHook](p.phases) {
		out = append(out, h.Dependencies()...)
	}
	return out
}

// facets are the providers' facets of a worker's local API, in list order.
func (p *Platform) facets() []local.Facet {
	var out []local.Facet
	for _, h := range lifecycle.Each[facetHook](p.phases) {
		out = append(out, h.Facet())
	}
	return out
}

// providerCatalog builds the platform's provider catalog (ADR-0082) from its providers, each naming its own entries,
// so a provider added to the list is in the catalog and the catalog cannot drift from what runs. The ingress is the
// Platform's own and stays listed until its place is decided. `bus` is intentionally absent: it is the internal-plane
// messaging substrate, never function-bindable. The F54 observability read path (ADR-0084) is in-daemon substrate
// serving the log-ingest capability, not a provider of its own.
func providerCatalog(ph *lifecycle.Phases) (*provider.Catalog, error) {
	ds := []provider.Descriptor{{Name: "ingress", Kind: provider.Builtin, Port: "gateway.Gateway", Summary: "HTTP ingress gateway"}}
	for _, d := range lifecycle.Each[describer](ph) {
		ds = append(ds, d.Descriptors()...)
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
