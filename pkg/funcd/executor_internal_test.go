package funcd

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: providers-boot-in-order
// scenario: boot-log-names-shared-hooks
// Run logs its providers in boot order with the hooks each one has, the shared hooks after the lifecycle ones. A
// wrapper that hid a hook would show here: a hook found by type assertion disappears behind a wrapper, as a lost
// StopGrace once cut the close budget (#453).
func TestPhases_ListTheProvidersWithTheirHooks(t *testing.T) {
	t.Parallel()
	log := &syncBuffer{}
	p, err := New(InMemory(), WithLogger(slog.New(slog.NewJSONHandler(log, nil))))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	var logged struct{ Providers []string }
	require.Eventually(t, func() bool {
		for line := range strings.Lines(log.String()) {
			if strings.Contains(line, `"msg":"platform provider hooks"`) {
				return json.Unmarshal([]byte(line), &logged) == nil
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "Run logs its providers' hooks")
	require.Equal(t, []string{
		"executor: start, run, before stop, stop",
		"kv: start, reclaim, stop, quotas, admissions, dependencies, facets",
		"blob: start, reclaim, stop, quotas, admissions, dependencies, facets",
		"sql: reclaim, run, stop, quotas, admissions, dependencies, facets",
		"lake: before stop",
		"eventing: run",
		"secrets: facets",
		"config: ",
		"invoke: dependencies",
	}, logged.Providers)
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
