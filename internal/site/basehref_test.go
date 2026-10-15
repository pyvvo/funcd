package site_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/site"
)

// Decision 10: every <base> goes, and one, keeping the first one's target, goes first in <head> (after a <meta charset>
// that opens it), else right after <html>, else before the first element; every other byte stays.
func TestWithBase(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"after head", `<!doctype html><html><head><title>A</title></head></html>`, `<!doctype html><html><head><base href="/bi/"><title>A</title></head></html>`},
		{"after a meta charset that opens head", `<head><meta charset="utf-8"><title>A</title></head>`, `<head><meta charset="utf-8"><base href="/bi/"><title>A</title></head>`},
		{"head with attributes, upper case", `<HTML><HEAD lang="en"><title>A</title>`, `<HTML><HEAD lang="en"><base href="/bi/"><title>A</title>`},
		{"replaces a base", `<head><base href="/"><title>A</title></head>`, `<head><base href="/bi/"><title>A</title></head>`},
		{"replaces a self-closing base", `<head><base href="/" /></head>`, `<head><base href="/bi/"></head>`},
		{"keeps the first base's target", `<head><title>A</title><base target="_top"><base href="/x/"></head>`, `<head><base href="/bi/" target="_top"><title>A</title></head>`},
		{"keeps the first target among the bases", `<head><base href="/"><base target="_top"></head>`, `<head><base href="/bi/" target="_top"></head>`},
		{"after html without head", `<html><body>x</body></html>`, `<html><base href="/bi/"><body>x</body></html>`},
		{"after the doctype, before the first element", `<!doctype html><title>A</title>`, `<!doctype html><base href="/bi/"><title>A</title>`},
		{"no element", `hello`, `<base href="/bi/">hello`},
		{"ignores a head in a comment", `<!-- <head> --><html><head></head>`, `<!-- <head> --><html><head><base href="/bi/"></head>`},
		{"ignores a base in a script", `<head><script>var s = "<base href='x'>";</script></head>`, `<head><base href="/bi/"><script>var s = "<base href='x'>";</script></head>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, string(site.WithBase([]byte(tc.doc), "/bi/")))
		})
	}
	require.Equal(t, `<head><base href="/a&#34;b/"></head>`, string(site.WithBase([]byte(`<head></head>`), `/a"b/`)))
}

// The base is the mount ending with "/", percent-encoded as a URL path; the copy's key names the Site and a hash of
// its mount.
func TestBaseHrefOfAndCopyKey(t *testing.T) {
	at := func(path string) *v1.Site {
		s := &v1.Site{}
		s.Name, s.Spec.Ingress = "web", v1.SiteIngress{Host: "web.example", Path: path}
		return s
	}
	require.Equal(t, "/", site.BaseHrefOf(at("")))
	require.Equal(t, "/bi/", site.BaseHrefOf(at("/bi")))
	require.Equal(t, "/a%20b%23c/", site.BaseHrefOf(at("/a b#c")))
	require.Regexp(t, `^_runtime/web/[0-9a-f]{12}/index\.html$`, site.IndexCopyRel(at("/bi"), "index.html"))
	require.NotEqual(t, site.IndexCopyRel(at("/bi"), "index.html"), site.IndexCopyRel(at("/b2"), "index.html"))
	require.NotEqual(t, site.IndexCopyRel(at(""), "bi/index.html"), site.IndexCopyRel(at("/bi"), "index.html"))
}

const relIndex = `<!doctype html><html><head><title>A</title></head><body><script src="assets/app.js"></script></body></html>`

func baseHref(path string) func(*v1.Site) {
	return func(s *v1.Site) {
		s.Spec.Ingress.Path = path
		s.Spec.Ingress.BaseHref = true
	}
}

// scenario: site-config-base-href (the reconciler half) — the copy carrying the mount lands in the digest folder, before
// the Route, which serves it as its index; the bundle's index is unchanged and the status counts only the bundle. A new
// mount writes a new copy and moves the Route's index with it; without baseHref the index is the bundle's again.
func TestScenarioSiteConfigBaseHrefCopy(t *testing.T) {
	h := newHarness(t)
	digest := h.push("v1", map[string]string{"index.html": relIndex, "assets/app.js": "js"})
	h.seedSite("bi", "v1", baseHref("/bi"))

	h.reconcile("bi")
	rel := site.IndexCopyRel(h.site("bi"), "index.html")
	require.Contains(t, h.body("bi/"+slug(digest)+"/"+rel), `<head><base href="/bi/"><title>A</title>`)
	require.Equal(t, relIndex, h.body("bi/"+slug(digest)+"/index.html"), "no bundle file changes")
	require.Equal(t, rel, bundleRule(t, h.route("bi")).Index, "the first Route already serves the copy")
	require.False(t, h.exists("bi/_runtime/bi/config.json"), "baseHref alone generates no config.json")
	h.programRoute("bi", true, "Programmed")
	h.reconcile("bi")
	s := h.site("bi")
	require.Equal(t, v1.PhaseReady, s.Status.Phase)
	require.Equal(t, 2, s.Status.Objects, "the copy is not counted as a bundle object")

	h.apply("bi", baseHref("/portal"))
	h.redeploy("bi")
	moved := site.IndexCopyRel(h.site("bi"), "index.html")
	require.NotEqual(t, rel, moved)
	require.Contains(t, h.body("bi/"+slug(digest)+"/"+moved), `<base href="/portal/">`)
	require.Equal(t, moved, bundleRule(t, h.route("bi")).Index)

	h.apply("bi", func(s *v1.Site) { s.Spec.Ingress.BaseHref = false })
	h.redeploy("bi")
	require.Equal(t, "index.html", bundleRule(t, h.route("bi")).Index)
}

// A Site at the root carries base "/"; a new digest gets its own copy, which switches with the Route's prefix in one
// write.
func TestSiteConfigBaseHrefRootAndNewDigest(t *testing.T) {
	h := newHarness(t)
	h.push("v1", map[string]string{"index.html": relIndex})
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Ingress.BaseHref = true })
	h.deploy("bi")
	v2 := h.push("v2", map[string]string{"index.html": relIndex, "assets/app.js": "js-2"})
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v2") })
	h.reconcile("bi")
	rel := site.IndexCopyRel(h.site("bi"), "index.html")
	require.Contains(t, h.body("bi/"+slug(v2)+"/"+rel), `<head><base href="/"><title>A</title>`)
	st := bundleRule(t, h.route("bi"))
	require.Equal(t, "bi/"+slug(v2)+"/", st.Prefix)
	require.Equal(t, rel, st.Index)
}

// scenario: site-config-runtime-reserved (the reconciler half) — a Site without baseHref deploying a bundle with a root
// _runtime entry materializes nothing and names the entry, while the previous digest keeps serving.
func TestScenarioSiteConfigRuntimeReserved(t *testing.T) {
	h := newHarness(t)
	v1d := h.push("v1", siteA())
	h.seedSite("bi", "v1", nil)
	h.deploy("bi")
	v2d := h.push("v2", map[string]string{"index.html": "<title>B</title>", "_runtime/x.txt": "x"})
	h.apply("bi", func(s *v1.Site) { s.Spec.Image = h.ref("v2") })
	h.reconcile("bi")
	s := h.site("bi")
	c := readyCond(t, s)
	require.Equal(t, "MaterializeFailed", c.Reason)
	require.Contains(t, c.Message, `"_runtime/x.txt"`)
	require.Equal(t, v1d, s.Status.Digest, "the previous digest keeps serving")
	require.False(t, h.exists("bi/"+slug(v2d)+"/index.html"), "nothing materialized")
}

// Decision 10: a copy the Bucket refuses fails the deploy like a bundle object: MaterializeFailed, and the Route keeps
// serving the bundle's index.
func TestSiteConfigBaseHrefCopyOverBucketCap(t *testing.T) {
	h := newHarness(t)
	h.push("v1", map[string]string{"index.html": "<head></head>" + strings.Repeat("x", 17)})
	pre := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	pre.Name, pre.Namespace, pre.ResourceGroup = bucket, ns, "rg1"
	pre.Spec.MaxObjectBytes = 35
	_, err := h.st.Create(h.ctx, pre)
	require.NoError(t, err)
	h.seedSite("bi", "v1", func(s *v1.Site) { s.Spec.Ingress.Rules = nil })
	h.deploy("bi")
	digest := h.site("bi").Status.Digest

	h.apply("bi", baseHref("/bi"))
	h.reconcile("bi")
	s := h.site("bi")
	c := readyCond(t, s)
	require.Equal(t, "MaterializeFailed", c.Reason)
	require.Contains(t, c.Message, "maxObjectBytes (35)")
	require.Equal(t, digest, s.Status.Digest)
	require.Equal(t, "index.html", bundleRule(t, h.route("bi")).Index)
}
