package site

import (
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// defaultIndex is the built-in fallback for Deps.DefaultIndex (the platform config's site.defaultIndex):
// the document served for "/" and asserted present when a Site's spec.index is empty. It mirrors the
// ADR-0120 static handler's default so the bundle rule and the index assertion agree.
const defaultIndex = "index.html"

// digestSlug is the digest with ':' replaced by '-' — a clean object-key segment (Decision §2).
func digestSlug(digest string) string { return strings.ReplaceAll(digest, ":", "-") }

// servingPrefix is the digest-scoped key prefix the bundle materializes under: <prefix>/<slug>/.
func servingPrefix(prefix, digest string) string { return prefix + "/" + digestSlug(digest) + "/" }

// indexOf is the document asserted present and served for "/": spec.index, else the configured default.
func indexOf(s *v1.Site, configured string) string {
	if s.Spec.Index != "" {
		return s.Spec.Index
	}
	if configured != "" {
		return configured
	}
	return defaultIndex
}

// newBucket compiles the Bucket a Site creates when none exists: the site prefix with NO owner plus every
// declared prefix verbatim. Never stamped with an OwnerReference in V1 (retain-only).
func newBucket(s *v1.Site) *v1.Bucket {
	b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	b.Name, b.Namespace, b.ResourceGroup = s.Spec.Bucket.Name, s.Namespace, s.ResourceGroup
	b.Spec.Prefixes = append([]v1.BucketPrefix{{Name: s.Spec.Prefix}}, s.Spec.Bucket.Prefixes...)
	return b
}

// adoptBucket bounds what the reconciler may touch on an adopted Bucket (Decision §5): it only ADDS the
// site prefix (ownerless) and the declared prefixes that are absent, never removing or rewriting an
// entry it did not add. It reports whether the Bucket changed, and prefixOwned when the Bucket already
// declares the site prefix with an owner — the one collision, resolved in favour of the existing writer.
func adoptBucket(b *v1.Bucket, s *v1.Site) (changed, prefixOwned bool) {
	have := make(map[string]v1.BucketPrefix, len(b.Spec.Prefixes))
	for _, p := range b.Spec.Prefixes {
		have[p.Name] = p
	}
	if p, ok := have[s.Spec.Prefix]; ok {
		if p.Owner != "" {
			return false, true
		}
	} else {
		b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: s.Spec.Prefix})
		changed = true
	}
	for _, p := range s.Spec.Bucket.Prefixes {
		if _, ok := have[p.Name]; ok {
			continue
		}
		b.Spec.Prefixes = append(b.Spec.Prefixes, p)
		changed = true
	}
	return changed, false
}

// compileRoute builds the owned Route's desired spec for a target digest (ADR-0139 §8, path per
// ADR-0140): one data-mount rule per spec.ingress.rules[] (a static arm with NO index/spa — a miss is
// 404 by construction) plus the bundle rule at s.EffectivePath() over the digest-scoped prefix, and, when config
// is true, the exact GET/HEAD rule that serves the generated config.json as the index of its own prefix (ADR-0239).
// ingress.enabled maps to spec.enabled and ingress.auth to spec.auth (ADR-0241 Decision 5).
func compileRoute(s *v1.Site, digest, index string, config bool) v1.RouteSpec {
	ing := &s.Spec.Ingress
	rules := make([]v1.RouteRule, 0, len(ing.Rules)+1)
	for _, r := range ing.Rules {
		bucket := r.Bucket
		if bucket == "" {
			bucket = s.Spec.Bucket.Name
		}
		rules = append(rules, v1.RouteRule{
			Path:    r.Path,
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: bucket, Prefix: r.Prefix + "/"}},
		})
	}
	rules = append(rules, v1.RouteRule{
		Path:    s.EffectivePath(),
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: s.Spec.Bucket.Name, Prefix: servingPrefix(s.Spec.Prefix, digest), Index: index, SPA: s.Spec.SPA}},
	})
	if config {
		rules = append(rules, v1.RouteRule{
			Path:     s.RuntimeConfigPath(),
			PathType: v1.PathTypeExact,
			Methods:  []v1.HTTPMethod{v1.MethodGet, v1.MethodHead},
			Backend:  v1.RouteBackend{Static: &v1.StaticBackend{Bucket: s.Spec.Bucket.Name, Prefix: s.RuntimeConfigPrefix(), Index: v1.SiteRuntimeConfigFile}},
		})
	}
	spec := v1.RouteSpec{Host: ing.Host, Rules: rules, Enabled: ing.Enabled}
	if ing.Auth != nil {
		auth := *ing.Auth
		spec.Auth = &auth
	}
	return spec
}

// servingDigest is compileRoute's inverse — the durable record of what is served (ADR-0139 §3): the digest of the
// bundle rule v1.Site.Served finds on the Site's own Route, or "".
func servingDigest(rt *v1.Route, s *v1.Site) string {
	bundle, _, ok := s.Served(rt)
	if !ok {
		return ""
	}
	return v1.SlugDigest(strings.TrimSuffix(strings.TrimPrefix(bundle.Prefix, s.Spec.Prefix+"/"), "/"))
}
