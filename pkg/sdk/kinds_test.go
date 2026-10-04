package sdk

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
)

// scenario: kinddescriptor-covers-all-kinds (the drift guard against the server routes).
func TestKindDescriptorCoversAllKinds(t *testing.T) {
	t.Parallel()
	for _, k := range v1.AllKinds() {
		if _, ok := kindDescriptors[k]; !ok {
			t.Errorf("kind %q has no kindDescriptor (route plural missing)", k)
		}
	}
}

// TestSDKKindPaths_MatchServerRoutes builds each kind's paths with the client's own URL builders and
// requires the control plane to serve every method the client sends there; the server registers its
// routes per kind by hand, so a plural can drift from them (WorkerNode once used "workers").
func TestSDKKindPaths_MatchServerRoutes(t *testing.T) {
	t.Parallel()
	paths := controlplane.NewAPI(chi.NewRouter(), controlplane.NewStubHandlers()).OpenAPI().Paths
	c := &Client{}
	for _, k := range v1.AllKinds() {
		col, err := c.collectionURL(k, "{namespace}")
		if err != nil {
			t.Fatalf("collectionURL(%s): %v", k, err)
		}
		item, err := c.itemURL(k, "{namespace}", "{name}")
		if err != nil {
			t.Fatalf("itemURL(%s): %v", k, err)
		}
		for _, r := range []struct {
			path    string
			method  string
			operate func(*huma.PathItem) *huma.Operation
		}{
			{col, http.MethodGet, func(p *huma.PathItem) *huma.Operation { return p.Get }},
			{col, http.MethodPost, func(p *huma.PathItem) *huma.Operation { return p.Post }},
			{item, http.MethodGet, func(p *huma.PathItem) *huma.Operation { return p.Get }},
			{item, http.MethodPut, func(p *huma.PathItem) *huma.Operation { return p.Put }},
			{item, http.MethodDelete, func(p *huma.PathItem) *huma.Operation { return p.Delete }},
		} {
			if p := paths[r.path]; p == nil || r.operate(p) == nil {
				t.Errorf("kind %s: the server has no route for %s %s", k, r.method, r.path)
			}
		}
	}
}

func TestKindFromToken(t *testing.T) {
	t.Parallel()
	cases := map[string]v1.Kind{
		"functions":      v1.KindFunction,
		"function":       v1.KindFunction,
		"fn":             v1.KindFunction,
		"namespaces":     v1.KindNamespace,
		"egresspolicies": v1.KindEgressPolicy,
		"runtimeclasses": v1.KindRuntimeClass,
	}
	for token, want := range cases {
		got, ok := KindFromToken(token)
		if !ok || got != want {
			t.Errorf("KindFromToken(%q) = %q,%v; want %q,true", token, got, ok, want)
		}
	}
	if _, ok := KindFromToken("frobnicate"); ok {
		t.Errorf("KindFromToken(unknown) should be !ok")
	}
}
