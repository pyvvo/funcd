package funcd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
)

// A Site's status follows later changes to its owned Route's readiness (ADR-0139 §6): an earlier-named
// Route taking the same host and path turns the Ready Site NotReady, and removing it turns the Site
// Ready again, with no write to the Site.
func TestIssue301_SiteStatusFollowsRouteReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dist := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dist, "index.html"), []byte("<title>bi</title>"), 0o600))
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
	_, err = st.Create(ctx, s)
	require.NoError(t, err)
	ready := func() v1.Condition {
		obj, gerr := st.Get(ctx, v1.KindSite.GVK(), "default", "bi")
		require.NoError(t, gerr)
		c, _ := obj.(*v1.Site).Status.Conditions.Get("Ready")
		return c
	}
	require.Eventually(t, func() bool { return ready().Status == v1.ConditionTrue }, 10*time.Second, 20*time.Millisecond,
		"site Ready; last %+v", ready())
	// Outlast the Site's pending-Route poll (2 s), so only a Route change can re-run its reconcile.
	time.Sleep(3 * time.Second)

	earlier := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	earlier.Name, earlier.Namespace, earlier.ResourceGroup = "a", "default", "rg1"
	earlier.Spec = v1.RouteSpec{Host: "bi.example.com", Rules: []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "reports", Prefix: "other/"}}}}}
	_, err = st.Create(ctx, earlier)
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
