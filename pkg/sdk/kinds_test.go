package sdk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
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
	// The builders refuse the {namespace} and {name} templates, which are not DNS labels (issue #698): build with
	// labels, then put the templates back.
	route := strings.NewReplacer("/namespaces/ns/", "/namespaces/{namespace}/")
	for _, k := range v1.AllKinds() {
		col, err := c.collectionURL(k, "ns")
		if err != nil {
			t.Fatalf("collectionURL(%s): %v", k, err)
		}
		item, err := c.itemURL(k, "ns", "name")
		if err != nil {
			t.Fatalf("itemURL(%s): %v", k, err)
		}
		if item != col+"/name" {
			t.Fatalf("itemURL(%s) = %q, want %q", k, item, col+"/name")
		}
		col = route.Replace(col)
		item = col + "/{name}"
		for _, r := range []struct {
			path    string
			method  string
			write   bool
			operate func(*huma.PathItem) *huma.Operation
		}{
			{col, http.MethodGet, false, func(p *huma.PathItem) *huma.Operation { return p.Get }},
			{col, http.MethodPost, true, func(p *huma.PathItem) *huma.Operation { return p.Post }},
			{item, http.MethodGet, false, func(p *huma.PathItem) *huma.Operation { return p.Get }},
			{item, http.MethodPut, true, func(p *huma.PathItem) *huma.Operation { return p.Put }},
			{item, http.MethodDelete, true, func(p *huma.PathItem) *huma.Operation { return p.Delete }},
		} {
			p := paths[r.path]
			served := p != nil && r.operate(p) != nil
			switch {
			case r.write && ReadOnlyKind(k) && served:
				t.Errorf("kind %s is read-only, yet the server routes %s %s", k, r.method, r.path)
			case (!r.write || !ReadOnlyKind(k)) && !served:
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

// ADR-0200 Decision 2: ReadOnlyWriter names the writer of each read-only kind, and only of those.
func TestReadOnlyWriter(t *testing.T) {
	t.Parallel()
	for _, k := range v1.AllKinds() {
		want := map[v1.Kind]string{
			v1.KindRevision:    "the Function reconciler",
			v1.KindAppRevision: "the App reconciler",
		}[k]
		if got := ReadOnlyWriter(k); got != want || ReadOnlyKind(k) != (want != "") {
			t.Errorf("ReadOnlyWriter(%s) = %q, ReadOnlyKind = %v; want %q", k, got, ReadOnlyKind(k), want)
		}
	}
}

// ADR-0200 Decision 2: Apply, Create and Delete refuse an AppRevision with fault.Invalid before any request, naming
// the App reconciler.
func TestSDKAppRevisionWritesRefusedBeforeRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindAppRevision)
	rev := obj.(*v1.AppRevision)
	rev.Name, rev.Namespace = "todo-1", "team-a"
	_, applyErr := c.Apply(ctx, rev)
	_, createErr := c.Create(ctx, rev)
	for op, err := range map[string]error{
		"Apply":  applyErr,
		"Create": createErr,
		"Delete": c.Delete(ctx, v1.KindAppRevision, "team-a", "todo-1"),
	} {
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "AppRevision is read-only: the App reconciler writes it") {
			t.Errorf("%s: err = %v, want fault.Invalid with the read-only message", op, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the refused writes sent %d requests, want 0", n)
	}
}

// ADR-0172 Decision 2: Apply, Create and Delete refuse a Revision with fault.Invalid before any request.
func TestSDKRevisionWritesRefusedBeforeRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindRevision)
	rev := obj.(*v1.Revision)
	rev.Name, rev.Namespace = "greeter-1", "team-a"
	_, applyErr := c.Apply(ctx, rev)
	_, createErr := c.Create(ctx, rev)
	for op, err := range map[string]error{
		"Apply":  applyErr,
		"Create": createErr,
		"Delete": c.Delete(ctx, v1.KindRevision, "team-a", "greeter-1"),
	} {
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "Revision is read-only: the Function reconciler writes it") {
			t.Errorf("%s: err = %v, want fault.Invalid with the read-only message", op, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the refused writes sent %d requests, want 0", n)
	}
}
