package funcd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
)

// readyEngine is a CatalogService engine that is Ready at once at addr.
type readyEngine struct{ addr string }

func (e readyEngine) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Running: 1, Ready: true, Address: e.addr}, nil
}

func (readyEngine) Teardown(context.Context, provider.ProviderRef) error { return nil }

// TestCatalogWatchesFollowCatalogAndBinders: on a running platform, with no pass run by hand, a consumer's RevisionReady
// follows its catalog's delete and re-create (the CatalogService watch, ADR-0162 Decision 5), and a deleted catalog's
// listener is removed once its last binder is deleted (the Function watch, Decision 4). Each turn is awaited well inside
// the supervision period, so no periodic pass can stand in for a missing watch.
func TestCatalogWatchesFollowCatalogAndBinders(t *testing.T) {
	t.Parallel()
	const turn = 3 * time.Second
	ctx := context.Background()
	engine := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(engine.Close)
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	p, err := New(InMemory(), WithRuntime(rt), WithCatalogProviderRuntime(readyEngine{addr: strings.TrimPrefix(engine.URL, "http://")}))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	st := p.cfg.store

	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	art := filepath.Join(dir, "reader.mjs")
	require.NoError(t, os.WriteFile(art, []byte("export function handle() { return {}; }\n"), 0o600))

	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: "lake"}}}
	seedObjects(t, st, bucket, lakeCatalogService("default"))
	var url string
	require.Eventually(t, func() bool {
		var bound bool
		url, bound = p.catalogProxy.ProxyURL("default", "lake")
		obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
		return bound && gerr == nil && obj.(*v1.CatalogService).Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond, "catalog lake is Ready")

	reader := lakeFunction("default")
	reader.Name = "reader"
	reader.Spec.Image, reader.Spec.Handler = "file://"+art, "handle"
	reader.Spec.Scaling.MinReplicas = 1
	reader.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
	seedObjects(t, st, reader)
	readerIs := func(phase v1.Phase, rrStatus v1.ConditionStatus, rrReason string) func() bool {
		return func() bool {
			obj, gerr := st.Get(ctx, v1.KindFunction.GVK(), "default", "reader")
			if gerr != nil {
				return false
			}
			fn := obj.(*v1.Function)
			rr, ok := fn.Status.Conditions.Get("RevisionReady")
			return fn.Status.Phase == phase && ok && rr.Status == rrStatus && rr.Reason == rrReason
		}
	}
	require.Eventually(t, readerIs(v1.PhaseReady, v1.ConditionTrue, ""), 5*time.Second, 10*time.Millisecond, "reader is Ready")

	deleteLake := func() {
		t.Helper()
		require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
		require.Eventually(t, readerIs(v1.PhaseReady, v1.ConditionFalse, "CatalogNotReady"), turn, 10*time.Millisecond,
			"reader shows CatalogNotReady and keeps serving")
		require.Eventually(t, func() bool { return slices.Contains(p.catalogProxy.Released("default"), "lake") }, turn,
			10*time.Millisecond, "lake's listener is released, not closed, while reader binds it")
		resp, gerr := http.Get("http://" + url)
		require.NoError(t, gerr)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	}
	deleteLake()
	seedObjects(t, st, lakeCatalogService("default"))
	require.Eventually(t, readerIs(v1.PhaseReady, v1.ConditionTrue, ""), turn, 10*time.Millisecond,
		"reader re-resolves the re-created catalog")
	again, bound := p.catalogProxy.ProxyURL("default", "lake")
	require.True(t, bound)
	require.Equal(t, url, again)
	require.Len(t, rt.created()["reader"], 1, "reader's worker is not re-created")

	deleteLake()
	require.NoError(t, st.Delete(ctx, v1.KindFunction.GVK(), "default", "reader", ""))
	require.Eventually(t, func() bool {
		_, bound := p.catalogProxy.ProxyURL("default", "lake")
		return !bound && len(p.catalogProxy.Released("default")) == 0
	}, turn, 10*time.Millisecond, "lake's listener closes once reader, its last binder, is deleted")
}
