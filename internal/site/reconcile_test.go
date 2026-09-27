package site_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/site"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const (
	ns     = v1.NamespaceName("default")
	bucket = "reports"
)

// recorder wraps the shared substrate and records every Put key in order (the index-last proof).
type recorder struct {
	blob.Bucket
	mu   sync.Mutex
	puts []string
}

func (r *recorder) Put(ctx context.Context, key string, data []byte) error {
	r.mu.Lock()
	r.puts = append(r.puts, key)
	r.mu.Unlock()
	return r.Bucket.Put(ctx, key, data)
}

func (r *recorder) reset() { r.mu.Lock(); r.puts = nil; r.mu.Unlock() }

// harness is one test's world: a memory store, a recorded mem:// substrate resolved exactly the way
// pkg/funcd's s3BucketFor does (a Bucket must exist in the store), a local OCI layout, and the reconciler.
type harness struct {
	t      *testing.T
	ctx    context.Context
	st     store.Store
	shared *recorder
	layout string
	r      *site.Reconciler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	rec := &recorder{Bucket: shared}
	st := store.New(memory.New())
	resolve := func(n v1.NamespaceName, b string) (blob.Bucket, bool) {
		if _, gerr := st.Get(ctx, v1.KindBucket.GVK(), n, v1.ObjectName(b)); gerr != nil {
			return nil, false
		}
		return blob.Prefixed(rec, "s3/"+string(n)+"/"+b+"/"), true
	}
	return &harness{t: t, ctx: ctx, st: st, shared: rec, layout: filepath.Join(t.TempDir(), "layout"),
		r: site.New(site.Deps{Store: st, Buckets: resolve})}
}

// push writes files into a dir and pushes it as a site artifact under tag, returning the digest.
func (h *harness) push(tag string, files map[string]string) string {
	h.t.Helper()
	dir := filepath.Join(h.t.TempDir(), "site-"+tag)
	for name, body := range files {
		require.NoError(h.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(h.t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	digest, err := artifact.PushSite(h.ctx, h.ref(tag), dir)
	require.NoError(h.t, err)
	return digest
}

func (h *harness) ref(tag string) string { return "oci-layout://" + h.layout + ":" + tag }

func (h *harness) seedSite(name, tag string, mutate func(*v1.Site)) *v1.Site {
	h.t.Helper()
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	s.Spec = v1.SiteSpec{
		Image:   h.ref(tag),
		Bucket:  v1.SiteBucket{Name: bucket, Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "etl"}}},
		Prefix:  "bi",
		SPA:     true,
		Ingress: v1.SiteIngress{Host: "bi.example.com", Public: true, Rules: []v1.SiteRule{{Path: "/data", Prefix: "gold"}}},
	}
	if mutate != nil {
		mutate(s)
	}
	created, err := h.st.Create(h.ctx, s)
	require.NoError(h.t, err)
	return created.(*v1.Site)
}

func (h *harness) reconcile(name string) controller.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(h.ctx, controller.Request{GVK: v1.KindSite.GVK(), Namespace: ns, Name: v1.ObjectName(name)})
	require.NoError(h.t, err)
	return res
}

func (h *harness) site(name string) *v1.Site {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindSite.GVK(), ns, v1.ObjectName(name))
	require.NoError(h.t, err)
	return obj.(*v1.Site)
}

func (h *harness) route(name string) *v1.Route {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindRoute.GVK(), ns, v1.ObjectName(name))
	require.NoError(h.t, err)
	return obj.(*v1.Route)
}

func (h *harness) bucket() *v1.Bucket {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindBucket.GVK(), ns, bucket)
	require.NoError(h.t, err)
	return obj.(*v1.Bucket)
}

// programRoute plays the Route reconciler: it stamps the owned Route's Ready condition for its current
// generation (True, or False with reason), so the Site's next reconcile sees a CURRENT condition.
func (h *harness) programRoute(name string, ready bool, reason string) {
	h.t.Helper()
	rt := h.route(name)
	status, phase := v1.ConditionTrue, v1.PhaseReady
	if !ready {
		status, phase = v1.ConditionFalse, v1.PhasePending
	}
	rt.Status.Phase = phase
	rt.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: status, Reason: reason, Message: reason, ObservedGeneration: rt.Generation})
	_, err := h.st.Update(h.ctx, rt)
	require.NoError(h.t, err)
}

// apply plays `funcdctl apply`: a status-wiping PUT of the spec (mutate edits the spec; nil re-applies
// the same manifest, which leaves metadata.generation untouched).
func (h *harness) apply(name string, mutate func(*v1.Site)) {
	h.t.Helper()
	s := h.site(name)
	if mutate != nil {
		mutate(s)
	}
	s.Status = v1.SiteStatus{}
	_, err := h.st.Update(h.ctx, s)
	require.NoError(h.t, err)
}

// deploy drives a Site all the way to Ready: reconcile, program the Route, reconcile again.
func (h *harness) deploy(name string) *v1.Site {
	h.t.Helper()
	h.reconcile(name)
	h.programRoute(name, true, "Programmed")
	h.reconcile(name)
	s := h.site(name)
	require.Equal(h.t, v1.PhaseReady, s.Status.Phase, "site must be Ready: %+v", s.Status)
	return s
}

func (h *harness) exists(key string) bool {
	h.t.Helper()
	ok, err := h.shared.Exists(h.ctx, "s3/"+string(ns)+"/"+bucket+"/"+key)
	require.NoError(h.t, err)
	return ok
}

func readyCond(t *testing.T, s *v1.Site) v1.Condition {
	t.Helper()
	c, ok := s.Status.Conditions.Get("Ready")
	require.True(t, ok, "no Ready condition on %+v", s.Status)
	return c
}

func slug(digest string) string { return strings.ReplaceAll(digest, ":", "-") }

// bundleRule finds the bundle rule the way production does (ADR-0140 §5): by its digest-scoped key
// prefix, not by its edge path — so it works for a root mount and a path mount alike.
func bundleRule(t *testing.T, rt *v1.Route) *v1.StaticBackend {
	t.Helper()
	st, _ := bundleRuleAt(t, rt)
	return st
}

// bundleRuleAt also returns the edge path the bundle is mounted at.
func bundleRuleAt(t *testing.T, rt *v1.Route) (*v1.StaticBackend, string) {
	t.Helper()
	for i := range rt.Spec.Rules {
		st := rt.Spec.Rules[i].Backend.Static
		if st != nil && strings.Contains(st.Prefix, "/sha256-") {
			return st, rt.Spec.Rules[i].Path
		}
	}
	t.Fatalf("no bundle rule on route %+v", rt.Spec)
	return nil, ""
}

// siteA is the canonical three-file bundle (index + a nested asset + a script) the scenarios deploy first.
func siteA() map[string]string {
	return map[string]string{"index.html": "<title>A</title>", "img/logo.png": "png-A", "app.js": "js-A"}
}

// scenario: owned-bucket-and-route-materialized (ADR-0139) — one reconcile materializes the Bucket (site
// prefix ownerless + the declared prefix verbatim) and the Route (named after the Site, owner-stamped,
// one data-mount rule with NO index/spa + the bundle rule at "/" over <prefix>/<digest-slug>/), and the
// bundle objects land under that key.
func TestScenarioOwnedBucketAndRouteMaterialized(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	s := h.seedSite("bi", "v1", nil)
	h.reconcile("bi")

	b := h.bucket()
	require.Equal(t, []v1.BucketPrefix{{Name: "bi"}, {Name: "gold", Owner: "etl"}}, b.Spec.Prefixes)
	rt := h.route("bi")
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindSite, Namespace: ns, Name: "bi"}, UID: s.UID, Controller: true, BlockOwnerDeletion: true}}, rt.OwnerReferences)
	require.Equal(t, "bi.example.com", rt.Spec.Host)
	require.Len(t, rt.Spec.Rules, 2)
	require.Equal(t, v1.RouteRule{Path: "/data", Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: bucket, Prefix: "gold/", Public: true}}}, rt.Spec.Rules[0], "a data mount carries no index/spa")
	require.Equal(t, &v1.StaticBackend{Bucket: bucket, Prefix: "bi/" + slug(digest) + "/", Index: "index.html", SPA: true, Public: true}, bundleRule(t, rt))
	for name := range siteA() {
		require.True(t, h.exists("bi/"+slug(digest)+"/"+name), "object %s materialized", name)
	}
	st := h.site("bi")
	require.Equal(t, digest, st.Status.Digest)
	require.Equal(t, "bi/"+slug(digest)+"/", st.Status.ServingPrefix)
	require.Equal(t, 3, st.Status.Objects)
	require.Equal(t, int64(len("<title>A</title>")+len("png-A")+len("js-A")), st.Status.Bytes)
}

// scenario: retain-stamps-no-owner-reference (ADR-0139 §5) — under retain (the V1 default) the Bucket
// carries NO OwnerReference (it outlives the Site) while the owned Route always carries one.
func TestScenarioRetainStampsNoOwnerReference(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Bucket.Deletion = v1.DeletionRetain })
	h.reconcile("bi")
	require.Empty(t, h.bucket().OwnerReferences, "a retain Bucket is never stamped")
	require.Len(t, h.route("bi").OwnerReferences, 1, "the Route is always stamped")
}

// scenario: adopted-bucket-prefixes-preserved (ADR-0139 §5) — adopting a pre-existing Bucket leaves every
// entry the Site did not declare byte-identical (and the per-object policy); the reconciler only ADDS.
func TestScenarioAdoptedBucketPrefixesPreserved(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	pre := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	pre.Name, pre.Namespace, pre.ResourceGroup = bucket, ns, "rg1"
	pre.Spec = v1.BucketSpec{MaxObjectBytes: 4096, Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "lakehouse-etl"}, {Name: "bronze", Owner: "ingest"}}}
	_, err := h.st.Create(h.ctx, pre)
	require.NoError(t, err)

	h.seedSite("bi", "v1", nil) // declares gold{owner: etl} — an entry that already exists with another owner
	h.reconcile("bi")
	b := h.bucket()
	require.Equal(t, int64(4096), b.Spec.MaxObjectBytes)
	require.Equal(t, []v1.BucketPrefix{{Name: "gold", Owner: "lakehouse-etl"}, {Name: "bronze", Owner: "ingest"}, {Name: "bi"}}, b.Spec.Prefixes,
		"existing entries untouched (gold keeps its owner), the site prefix appended ownerless")
	require.Equal(t, v1.PhaseReady, h.deploy("bi").Status.Phase)
}

// scenario: adopted-prefix-with-owner-rejected (ADR-0139 §5) — a Bucket already declaring spec.prefix
// WITH an owner is never taken over: NotReady PrefixOwned, the entry untouched, nothing materialized.
func TestScenarioAdoptedPrefixWithOwnerRejected(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	pre := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	pre.Name, pre.Namespace, pre.ResourceGroup = bucket, ns, "rg1"
	pre.Spec.Prefixes = []v1.BucketPrefix{{Name: "bi", Owner: "publisher"}}
	_, err := h.st.Create(h.ctx, pre)
	require.NoError(t, err)

	h.seedSite("bi", "v1", nil)
	h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, "PrefixOwned", readyCond(t, s).Reason)
	require.Empty(t, s.Status.Digest)
	require.Equal(t, []v1.BucketPrefix{{Name: "bi", Owner: "publisher"}}, h.bucket().Spec.Prefixes)
	require.False(t, h.exists("bi/"+slug(digest)+"/index.html"))
	require.Empty(t, h.shared.puts)
	_, gerr := h.st.Get(h.ctx, v1.KindRoute.GVK(), ns, "bi")
	require.Error(t, gerr, "no Route is materialized")
}

// scenario: redeploy-swaps-atomically (ADR-0139 §2/§3) — a Ready Site serving A; the spec moves to B
// (a status-wiping apply): B is fully materialized, index last, and only then is the Route's bundle rule
// re-programmed to B; A's objects are never removed.
func TestScenarioRedeploySwapsAtomically(t *testing.T) {
	h := newHarness(t)
	a := h.push("v1", siteA())
	b := h.push("v2", map[string]string{"index.html": "<title>B</title>", "app.js": "js-B", "css/x.css": "css-B"})
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")
	require.Equal(t, "bi/"+slug(a)+"/", bundleRule(t, h.route("bi")).Prefix)

	h.shared.reset()
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v2") })
	res := h.reconcile("bi")
	require.Equal(t, 2*time.Second, res.RequeueAfter, "the re-programmed Route is pending → requeue")
	puts := h.shared.puts
	require.Len(t, puts, 3)
	require.True(t, strings.HasSuffix(puts[len(puts)-1], "/index.html"), "the index is written LAST: %v", puts)
	rt := h.route("bi")
	require.Equal(t, "bi/"+slug(b)+"/", bundleRule(t, rt).Prefix, "the Route swapped to B once complete")
	require.True(t, h.exists("bi/"+slug(a)+"/index.html"), "A's objects are never removed")
	s := h.site("bi")
	require.Equal(t, b, s.Status.Digest)
	require.Equal(t, "RouteNotReady", readyCond(t, s).Reason)
	h.programRoute("bi", true, "Programmed")
	h.reconcile("bi")
	s = h.site("bi")
	require.Equal(t, v1.PhaseReady, s.Status.Phase)
	require.Equal(t, b, s.Status.Digest)
	require.Equal(t, s.Generation, s.Status.ObservedGeneration)
}

// scenario: partial-unpack-recovers (ADR-0139 §3) — a digest prefix holding some objects but NO index (a
// crash mid-unpack) is treated as incomplete: the next reconcile re-uploads, writes the index last, and
// only then swaps the Route; the stale partial object is overwritten with the bundle's bytes.
func TestScenarioPartialUnpackRecovers(t *testing.T) {
	h := newHarness(t)
	a := h.push("v1", siteA())
	b := h.push("v2", map[string]string{"index.html": "<title>B</title>", "app.js": "js-B"})
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")
	// The crash: app.js landed (with stale bytes) but index.html never did.
	partial := "s3/" + string(ns) + "/" + bucket + "/bi/" + slug(b) + "/app.js"
	require.NoError(t, h.shared.Put(h.ctx, partial, []byte("stale")))
	h.shared.reset()

	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v2") })
	h.reconcile("bi")
	puts := h.shared.puts
	require.Equal(t, []string{
		"s3/default/reports/bi/" + slug(b) + "/app.js",
		"s3/default/reports/bi/" + slug(b) + "/index.html",
	}, puts, "re-uploaded, index last")
	got, err := h.shared.Get(h.ctx, partial)
	require.NoError(t, err)
	require.Equal(t, "js-B", string(got))
	require.Equal(t, "bi/"+slug(b)+"/", bundleRule(t, h.route("bi")).Prefix)
	_ = a
}

// scenario: not-ready-until-index-present (ADR-0139 §3) — a redeploy whose bundle has no index leaves the
// PREVIOUS digest serving: NotReady IndexMissing, status.digest recovered from the Route (the apply wiped
// it), the Route not re-programmed, nothing uploaded; and a FIRST deploy failing the same way stays
// digest-less with no Route at all.
func TestScenarioNotReadyUntilIndexPresent(t *testing.T) {
	h := newHarness(t)
	a := h.push("v1", siteA())
	noIndex := h.push("broken", map[string]string{"app.js": "js-only"})
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")

	h.shared.reset()
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("broken") })
	res := h.reconcile("bi")
	require.Zero(t, res.RequeueAfter)
	s := h.site("bi")
	require.Equal(t, "IndexMissing", readyCond(t, s).Reason)
	require.Equal(t, a, s.Status.Digest, "status.digest still names the previous serving digest")
	require.Equal(t, "bi/"+slug(a)+"/", bundleRule(t, h.route("bi")).Prefix, "the Route is not re-programmed")
	require.Empty(t, h.shared.puts, "an index-less bundle is not uploaded")
	require.False(t, h.exists("bi/"+slug(noIndex)+"/app.js"))
	require.NotEqual(t, s.Generation, s.Status.ObservedGeneration, "a failed generation stays unobserved")

	h.seedSite("fresh", "broken", func(s *v1.Site) { s.Spec.Prefix = "fresh" })
	h.reconcile("fresh")
	f := h.site("fresh")
	require.Equal(t, "IndexMissing", readyCond(t, f).Reason)
	require.Empty(t, f.Status.Digest)
	_, gerr := h.st.Get(h.ctx, v1.KindRoute.GVK(), ns, "fresh")
	require.Error(t, gerr, "no Route is materialized for a first deploy that never became servable")
}

// scenario: tag-resolved-once-per-generation (ADR-0139 §2) — the registry tag moves under a Ready Site:
// a reconcile with no spec change keeps serving the pinned digest; a re-apply (status wiped, generation
// unchanged) resolves the tag again and materializes the new digest.
func TestScenarioTagResolvedOncePerGeneration(t *testing.T) {
	h := newHarness(t)
	a := h.push("live", siteA())
	h.seedSite("bi", "live", nil)
	h.deploy("bi")
	gen := h.site("bi").Generation

	b := h.push("live", map[string]string{"index.html": "<title>B</title>"}) // the tag moves
	require.NotEqual(t, a, b)
	h.shared.reset()
	h.reconcile("bi")
	require.Equal(t, a, h.site("bi").Status.Digest, "a self-triggered reconcile never re-resolves the tag")
	require.Equal(t, "bi/"+slug(a)+"/", bundleRule(t, h.route("bi")).Prefix)
	require.Empty(t, h.shared.puts)

	h.apply("bi", nil) // deploy intent: same manifest, status wiped
	require.Equal(t, gen, h.site("bi").Generation, "an unchanged spec keeps its generation")
	h.reconcile("bi")
	require.Equal(t, b, h.site("bi").Status.Digest, "an apply re-resolves the tag")
	require.Equal(t, "bi/"+slug(b)+"/", bundleRule(t, h.route("bi")).Prefix)
	require.True(t, h.exists("bi/"+slug(b)+"/index.html"))
}

// scenario: rollback-to-previous-digest (ADR-0139 §2) — after serving A then B, pointing spec.image back
// at A serves A again with NO re-upload (its objects were never removed).
func TestScenarioRollbackToPreviousDigest(t *testing.T) {
	h := newHarness(t)
	a := h.push("v1", siteA())
	b := h.push("v2", map[string]string{"index.html": "<title>B</title>"})
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v2") })
	h.deploy("bi")
	require.Equal(t, b, h.site("bi").Status.Digest)

	h.shared.reset()
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v1") })
	h.deploy("bi")
	s := h.site("bi")
	require.Equal(t, a, s.Status.Digest)
	require.Equal(t, "bi/"+slug(a)+"/", bundleRule(t, h.route("bi")).Prefix)
	require.Empty(t, h.shared.puts, "rollback re-points; it never re-uploads")
	require.Equal(t, 3, s.Status.Objects)
}

// scenario: foreign-route-not-adopted (ADR-0139 §5) — a same-named Route carrying no OwnerReference to
// this Site is foreign: NotReady RouteNotOwned, the Route untouched, nothing materialized.
func TestScenarioForeignRouteNotAdopted(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	foreign := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	foreign.Name, foreign.Namespace, foreign.ResourceGroup = "bi", ns, "rg1"
	foreign.Spec = v1.RouteSpec{Host: "other.example.com", Rules: []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: "api"}}}}
	created, err := h.st.Create(h.ctx, foreign)
	require.NoError(t, err)

	h.seedSite("bi", "v1", nil)
	h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, "RouteNotOwned", readyCond(t, s).Reason)
	require.Equal(t, created.(*v1.Route).Spec, h.route("bi").Spec, "the foreign Route is left untouched")
	require.Empty(t, h.route("bi").OwnerReferences)
	_, gerr := h.st.Get(h.ctx, v1.KindBucket.GVK(), ns, bucket)
	require.Error(t, gerr, "nothing materialized")
	require.Empty(t, h.shared.puts)
}

// scenario: site-not-ready-when-route-not-ready (ADR-0139 §6) — a freshly written Route (condition not
// yet observing its generation) is pending ⇒ RouteNotReady + requeue; a CURRENT NotReady Route (an
// ADR-0110 HostRequired) propagates as RouteNotReady with no requeue; a Site is never Ready while
// unreachable even though its bundle materialized.
func TestScenarioSiteNotReadyWhenRouteNotReady(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	h.seedSite("bi", "v1", nil)

	res := h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, v1.PhasePending, s.Status.Phase)
	require.Equal(t, "RouteNotReady", readyCond(t, s).Reason)
	require.Equal(t, 2*time.Second, res.RequeueAfter, "pending ⇒ poll the Route")
	require.True(t, h.exists("bi/"+slug(digest)+"/index.html"), "the bundle materialized regardless")
	require.Equal(t, digest, s.Status.Digest)

	h.programRoute("bi", false, "HostRequired")
	res = h.reconcile("bi")
	s = h.site("bi")
	require.Equal(t, "RouteNotReady", readyCond(t, s).Reason)
	require.Contains(t, readyCond(t, s).Message, "HostRequired")
	require.Zero(t, res.RequeueAfter, "a current NotReady is not polled")

	h.programRoute("bi", true, "Programmed")
	h.reconcile("bi")
	require.Equal(t, v1.PhaseReady, h.site("bi").Status.Phase)
}

// scenario: deleted-site-reclaims-nothing (ADR-0139 §5) — reconciling a Site that no longer exists is a
// no-op: no collector exists, the owned Route survives, and no error is raised.
func TestScenarioDeletedSiteReclaimsNothing(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")
	require.NoError(t, h.st.Delete(h.ctx, v1.KindSite.GVK(), ns, "bi", ""))
	res := h.reconcile("bi")
	require.Zero(t, res.RequeueAfter)
	require.NotNil(t, h.route("bi"), "the owned Route is left behind (pre-existing platform gap)")
}

// scenario: configured-default-index (ADR-0139; the platform config's site.defaultIndex) — a Site with no
// spec.index serves and asserts the CONFIGURED default, not a hard-coded one; spec.index still wins.
func TestScenarioConfiguredDefaultIndex(t *testing.T) {
	h := newHarness(t)
	h.r = site.New(site.Deps{Store: h.st, Buckets: func(n v1.NamespaceName, b string) (blob.Bucket, bool) {
		if _, gerr := h.st.Get(h.ctx, v1.KindBucket.GVK(), n, v1.ObjectName(b)); gerr != nil {
			return nil, false
		}
		return blob.Prefixed(h.shared, "s3/"+string(n)+"/"+b+"/"), true
	}, DefaultIndex: "home.htm"})
	digest := h.push("v1", map[string]string{"home.htm": "<title>home</title>", "index.html": "<title>not-the-index</title>"})

	h.seedSite("bi", "v1", nil)
	h.reconcile("bi")
	require.Equal(t, "home.htm", bundleRule(t, h.route("bi")).Index, "the configured default is compiled onto the bundle rule")
	require.True(t, h.exists("bi/"+slug(digest)+"/home.htm"))

	h.push("noindex", map[string]string{"index.html": "<title>x</title>"})
	h.seedSite("other", "noindex", func(s *v1.Site) { s.Spec.Prefix = "other" })
	h.reconcile("other")
	require.Equal(t, "IndexMissing", readyCond(t, h.site("other")).Reason, "the configured default is what is asserted present")

	h.seedSite("explicit", "noindex", func(s *v1.Site) { s.Spec.Prefix = "explicit"; s.Spec.Index = "index.html" })
	h.reconcile("explicit")
	require.Equal(t, "index.html", bundleRule(t, h.route("explicit")).Index, "spec.index wins over the configured default")
}

// scenario: hosted-site-defaults-to-root (ADR-0140) — a Site with a host and no ingress.path mounts the
// bundle at "/", byte-identical to ADR-0139's output.
func TestScenarioHostedSiteDefaultsToRoot(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	h.seedSite("bi", "v1", nil) // seedSite sets host bi.example.com
	h.reconcile("bi")
	st, mount := bundleRuleAt(t, h.route("bi"))
	require.Equal(t, "/", mount)
	require.Equal(t, "bi/"+slug(digest)+"/", st.Prefix)
}

// scenario: two-hostless-sites-coexist (ADR-0140) — two host-less Sites default to /site/<name>, so their
// edge claims are distinct: neither takes "/" and neither can conflict with the other.
func TestScenarioTwoHostlessSitesCoexist(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.push("v2", map[string]string{"index.html": "<title>docs</title>"})
	hostless := func(s *v1.Site) { s.Spec.Ingress = v1.SiteIngress{} }
	h.seedSite("bi", "v1", hostless)
	h.seedSite("docs", "v2", func(s *v1.Site) { hostless(s); s.Spec.Prefix = "docs" })
	h.reconcile("bi")
	h.reconcile("docs")

	_, biMount := bundleRuleAt(t, h.route("bi"))
	_, docsMount := bundleRuleAt(t, h.route("docs"))
	require.Equal(t, "/site/bi", biMount)
	require.Equal(t, "/site/docs", docsMount)
	require.NotEqual(t, biMount, docsMount, "distinct claims ⇒ no RouteConflict")
	for _, n := range []string{"bi", "docs"} {
		for _, r := range h.route(n).Spec.Rules {
			require.NotEqual(t, "/", r.Path, "a host-less Site must not claim the root")
		}
	}
}

// scenario: existing-hostless-site-moves-on-upgrade (ADR-0140) — a host-less Site that served at "/"
// before this ADR now mounts at /site/<name>; setting ingress.path: "/" pins the old URL.
func TestScenarioExistingHostlessSiteMovesOnUpgrade(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Ingress = v1.SiteIngress{} })
	h.reconcile("bi")
	_, mount := bundleRuleAt(t, h.route("bi"))
	require.Equal(t, "/site/bi", mount, "the host-less default moved off the root")

	h.apply("bi", func(s *v1.Site) { s.Spec.Ingress.Path = "/" })
	h.reconcile("bi")
	_, pinned := bundleRuleAt(t, h.route("bi"))
	require.Equal(t, "/", pinned, "ingress.path: / pins the pre-ADR-0140 URL")
}

// scenario: path-change-reprograms-without-reupload (ADR-0140 §5/§6) — moving the mount re-programs the
// Route, keeps status.digest (the serving digest is recovered by key prefix, not by path), and uploads
// nothing: ingress.path is pure exposure.
func TestScenarioPathChangeReprogramsWithoutReupload(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", siteA())
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Ingress = v1.SiteIngress{Path: "/bi"} })
	h.deploy("bi")
	_, mount := bundleRuleAt(t, h.route("bi"))
	require.Equal(t, "/bi", mount)

	h.shared.reset()
	h.apply("bi", func(s *v1.Site) { s.Spec.Ingress.Path = "/reports" })
	h.deploy("bi")
	st, moved := bundleRuleAt(t, h.route("bi"))
	require.Equal(t, "/reports", moved)
	require.Equal(t, "bi/"+slug(digest)+"/", st.Prefix, "the same digest, at a new path")
	require.Equal(t, digest, h.site("bi").Status.Digest)
	require.Empty(t, h.shared.puts, "a path change re-programs; it never re-uploads")
}

// scenario: explicit-namespace-still-requires-host (ADR-0140 §7) — a path mount is not a tenancy
// discriminator: in an `explicit`-mode namespace a host-less Site is still NotReady via its Route.
func TestScenarioExplicitNamespaceStillRequiresHost(t *testing.T) {
	h := newHarness(t)
	h.push("v1", siteA())
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Ingress = v1.SiteIngress{Path: "/bi"} })
	h.reconcile("bi")
	// ADR-0110 gates this on the Route: the Site propagates whatever its owned Route reports.
	h.programRoute("bi", false, "HostRequired")
	h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, v1.PhasePending, s.Status.Phase)
	require.Equal(t, "RouteNotReady", readyCond(t, s).Reason)
	require.Contains(t, readyCond(t, s).Message, "HostRequired")
}
