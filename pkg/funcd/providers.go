package funcd

import (
	"context"

	"github.com/green-0-rabbit/funcd/internal/provider"
)

// providerCatalog is the platform's static provider catalog (ADR-0082): the single source of
// truth for which capability providers funcd offers and their tier. It is declared here at the
// composition root, which already wires every provider, so internal/provider stays a leaf.
//
// Built-ins are in-daemon / pure-Go / always-on; add-ons are out-of-daemon deployed service
// functions. s3 (ADR-0080) and log-ingest (ADR-0081) are Accepted built-ins whose code is still
// pending; the add-ons (catalog/query F48, observability-serving F54) are idea-stage — the catalog
// classifies recognized providers, not only running ones. `bus` is intentionally absent: it is the
// internal-plane messaging substrate, never function-bindable, so it is not a provider.
func providerCatalog() (*provider.Catalog, error) {
	return provider.New(
		// built-in data-plane services (function-bindable)
		provider.Descriptor{Name: "kv", Kind: provider.Builtin, Port: "kvstore.KV", Bindings: []string{"spec.kv"}, Summary: "namespaced key-value store"},
		provider.Descriptor{Name: "blob", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.blob"}, Summary: "opaque-byte object substrate"},
		provider.Descriptor{Name: "secrets", Kind: provider.Builtin, Port: "secrets.Resolver", Summary: "secret resolution + at-rest encryption"},
		provider.Descriptor{Name: "eventing", Kind: provider.Builtin, Summary: "CloudEvents trigger delivery (EventSource)"},
		provider.Descriptor{Name: "invoke", Kind: provider.Builtin, Bindings: []string{"spec.links"}, Summary: "synchronous fn-to-fn RPC (context.invoke)"},
		// built-in protocol gateways (infra; no function-facing binding)
		provider.Descriptor{Name: "ingress", Kind: provider.Builtin, Port: "gateway.Gateway", Summary: "HTTP ingress gateway"},
		provider.Descriptor{Name: "egress", Kind: provider.Builtin, Summary: "egress gateway (default-deny + audit)"},
		// built-in providers — Accepted, implementation pending
		provider.Descriptor{Name: "s3", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.blob"}, Summary: "S3-protocol frontend over blob (ADR-0080)"},
		provider.Descriptor{Name: "log-ingest", Kind: provider.Builtin, Summary: "function-telemetry side channel (ADR-0081)"},
		// add-on providers — out-of-daemon service functions (idea-stage)
		provider.Descriptor{Name: "catalog-query", Kind: provider.Addon, Summary: "DuckLake catalog/query service (F48)"},
		provider.Descriptor{Name: "observability-serving", Kind: provider.Addon, Summary: "observability serving over DuckDB (F54)"},
	)
}

// Providers returns the platform's provider catalog (ADR-0082), available after New.
func (p *Platform) Providers() *provider.Catalog { return p.providers }

// logProviders emits the provider catalog once at startup — the first consumer of the catalog's
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
