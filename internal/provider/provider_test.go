package provider_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/provider"
)

// sampleCatalog builds a small catalog mirroring the real built-in/add-on split.
func sampleCatalog(t *testing.T) *provider.Catalog {
	t.Helper()
	c, err := provider.New(
		provider.Descriptor{Name: "kv", Kind: provider.Builtin, Port: "kvstore.KV", Bindings: []string{"spec.kv"}, Summary: "key-value store"},
		provider.Descriptor{Name: "blob", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.blob"}, Summary: "blob substrate"},
		provider.Descriptor{Name: "ingress", Kind: provider.Builtin, Port: "gateway.Gateway", Summary: "ingress gateway"},
		provider.Descriptor{Name: "catalog-query", Kind: provider.Addon, Summary: "DuckLake catalog/query (F48)"},
		provider.Descriptor{Name: "observability-serving", Kind: provider.Addon, Summary: "observability serving (F54)"},
	)
	require.NoError(t, err)
	return c
}

func namesOf(ds []provider.Descriptor) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Name
	}
	return out
}

// scenario: catalog-enumerates-builtins — ByKind(Builtin) returns the built-ins, each tagged
// Kind=built-in, in stable Name order.
func TestScenarioCatalogEnumeratesBuiltins(t *testing.T) {
	c := sampleCatalog(t)
	builtins := c.ByKind(provider.Builtin)
	for _, d := range builtins {
		require.Equal(t, provider.Builtin, d.Kind)
	}
	require.Equal(t, []string{"blob", "ingress", "kv"}, namesOf(builtins)) // stable Name order
}

// scenario: catalog-classifies-addons — ByKind(Addon) returns the add-ons, distinct from built-ins.
func TestScenarioCatalogClassifiesAddons(t *testing.T) {
	c := sampleCatalog(t)
	addons := c.ByKind(provider.Addon)
	require.Equal(t, []string{"catalog-query", "observability-serving"}, namesOf(addons))
	for _, d := range addons {
		require.Equal(t, provider.Addon, d.Kind)
	}
	require.NotContains(t, namesOf(addons), "kv") // distinct from built-ins
}

// scenario: descriptor-names-the-shape — a built-in descriptor names the port (contract) and the
// binding (link) of its existing four-part shape.
func TestScenarioDescriptorNamesTheShape(t *testing.T) {
	c := sampleCatalog(t)
	kv, ok := c.Get("kv")
	require.True(t, ok)
	require.Equal(t, "kvstore.KV", kv.Port)            // names the port (contract)
	require.Equal(t, []string{"spec.kv"}, kv.Bindings) // names the link (binding)

	_, ok = c.Get("nope")
	require.False(t, ok)
}

// scenario: duplicate-name-rejected — New rejects two descriptors with the same Name (fault.Invalid).
func TestScenarioDuplicateNameRejected(t *testing.T) {
	_, err := provider.New(
		provider.Descriptor{Name: "kv", Kind: provider.Builtin},
		provider.Descriptor{Name: "kv", Kind: provider.Builtin},
	)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// New runs Validate on each descriptor (empty Name / bad Kind → fault.Invalid).
func TestNewValidatesDescriptors(t *testing.T) {
	_, err := provider.New(provider.Descriptor{Name: "", Kind: provider.Builtin})
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	_, err = provider.New(provider.Descriptor{Name: "x", Kind: provider.Kind("weird")})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func TestDescriptorValidateAndKind(t *testing.T) {
	require.True(t, provider.Builtin.Valid())
	require.True(t, provider.Addon.Valid())
	require.False(t, provider.Kind("nope").Valid())

	require.NoError(t, provider.Descriptor{Name: "x", Kind: provider.Builtin}.Validate())
	require.Error(t, provider.Descriptor{Name: "", Kind: provider.Builtin}.Validate())
	require.Error(t, provider.Descriptor{Name: "x", Kind: provider.Kind("weird")}.Validate())
}
