package funcd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/store"
)

// startSite runs an in-memory platform, creates objs and then the Site "bi" deploying files as its bundle,
// and returns the platform's store and a reader of the Site's Ready condition.
func startSite(t *testing.T, files map[string]string, objs ...v1.Object) (store.Store, func() v1.Condition) {
	t.Helper()
	ctx := context.Background()
	dist := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dist, name), []byte(body), 0o600))
	}
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	_, err := artifact.PushSite(ctx, ref, dist)
	require.NoError(t, err)

	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = p.Shutdown(context.Background())
	})
	st := p.cfg.store

	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = "bi", "default", "rg1"
	s.Spec = v1.SiteSpec{Image: ref, Bucket: v1.SiteBucket{Name: "reports"}, Prefix: "bi", Ingress: v1.SiteIngress{Host: "bi.example.com"}}
	for _, o := range append(objs, s) {
		_, err = st.Create(ctx, o)
		require.NoError(t, err)
	}
	return st, func() v1.Condition {
		obj, gerr := st.Get(ctx, v1.KindSite.GVK(), "default", "bi")
		require.NoError(t, gerr)
		c, _ := obj.(*v1.Site).Status.Conditions.Get("Ready")
		return c
	}
}

// A Site's status follows later changes to its owned Route's readiness (ADR-0139 §6): an earlier-named
// Route taking the same host and path turns the Ready Site NotReady, and removing it turns the Site
// Ready again, with no write to the Site.
func TestIssue301_SiteStatusFollowsRouteReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, ready := startSite(t, map[string]string{"index.html": "<title>bi</title>"})
	require.Eventually(t, func() bool { return ready().Status == v1.ConditionTrue }, 10*time.Second, 20*time.Millisecond,
		"site Ready; last %+v", ready())
	// Outlast the Site's pending-Route poll (2 s), so only a Route change can re-run its reconcile.
	time.Sleep(3 * time.Second)

	earlier := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	earlier.Name, earlier.Namespace, earlier.ResourceGroup = "a", "default", "rg1"
	earlier.Spec = v1.RouteSpec{Host: "bi.example.com", Rules: []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "reports", Prefix: "other/"}}}}}
	_, err := st.Create(ctx, earlier)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		c := ready()
		return c.Status == v1.ConditionFalse && c.Reason == "RouteNotReady"
	}, 5*time.Second, 20*time.Millisecond, "an earlier Route on the same host and path makes the Site NotReady; last %+v", ready())
	require.Contains(t, ready().Message, "RouteConflict")

	require.NoError(t, st.Delete(ctx, v1.KindRoute.GVK(), "default", "a", ""))
	require.Eventually(t, func() bool { return ready().Status == v1.ConditionTrue }, 5*time.Second, 20*time.Millisecond,
		"the Site is Ready again once its Route is; last %+v", ready())
}

// A bundle object over the Bucket's maxObjectBytes leaves the Site NotReady naming the object and the cap,
// not hot-retrying; raising the cap deploys it with no write to the Site, since a Bucket change re-runs
// the Sites declaring it.
func TestSite_RaisedBucketCapDeploysTheRefusedBundle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	b.Name, b.Namespace, b.ResourceGroup = "reports", "default", "rg1"
	b.Spec.MaxObjectBytes = 64
	st, ready := startSite(t, map[string]string{"index.html": "<title>bi</title>", "big.bin": strings.Repeat("x", 100)}, b)
	require.Eventually(t, func() bool { return ready().Reason == "MaterializeFailed" }, 10*time.Second, 20*time.Millisecond,
		"an over-cap object is a status; last %+v", ready())
	require.Contains(t, ready().Message, "/big.bin")
	require.Contains(t, ready().Message, "maxObjectBytes (64)")

	obj, err := st.Get(ctx, v1.KindBucket.GVK(), "default", "reports")
	require.NoError(t, err)
	raised := obj.(*v1.Bucket)
	raised.Spec.MaxObjectBytes = 0
	_, err = st.Update(ctx, raised)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return ready().Status == v1.ConditionTrue }, 10*time.Second, 20*time.Millisecond,
		"the raised cap deploys the Site; last %+v", ready())
}
