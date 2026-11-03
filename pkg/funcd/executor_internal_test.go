package funcd

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: providers-boot-in-order
// The platform lists its providers in boot order with the hooks each one has. A wrapper that hid a hook would show
// here: a hook found by type assertion disappears behind a wrapper, as a lost StopGrace once cut the close budget
// (#453).
func TestPhases_ListTheProvidersWithTheirHooks(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.Equal(t, []string{
		"executor: start, run, before stop, stop",
		"kv: start, stop",
		"blob: start, stop",
		"sql: start, run, stop",
		"lake: before stop",
		"eventing: run",
		"secrets: ",
		"config: ",
		"invoke: ",
	}, p.phases.Hooks())
}

// scenario: the-catalog-names-every-binding
// Every binding a Function can declare names a provider of the catalog, which is built from the providers that run: a
// new binding, or a provider left out of the list, fails here instead of drifting (ADR-0082).
func TestCatalog_NamesEveryFunctionBinding(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	named := map[string]bool{}
	for _, d := range p.Providers().All() {
		for _, b := range d.Bindings {
			named[b] = true
		}
	}
	bindings := reflect.TypeFor[v1.FunctionBindings]()
	for i := range bindings.NumField() {
		tag, _, _ := strings.Cut(bindings.Field(i).Tag.Get("json"), ",")
		require.True(t, named["spec.bindings."+tag], "no provider in the catalog names spec.bindings.%s", tag)
	}
}
