package site

import (
	"regexp"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// defaultIndex is the built-in fallback for Deps.DefaultIndex (the platform config's site.defaultIndex):
// the document served for "/" and asserted present when a Site's spec.index is empty. It mirrors the
// ADR-0120 static handler's default so the bundle rule and the index assertion agree.
const defaultIndex = "index.html"

// digestSlugRE recognizes the digest-scoped key segment the bundle rule is programmed with.
var digestSlugRE = regexp.MustCompile(`^sha256-[a-f0-9]{64}$`)

// digestSlug is the digest with ':' replaced by '-' — a clean object-key segment (Decision §2).
func digestSlug(digest string) string { return strings.ReplaceAll(digest, ":", "-") }

// slugDigest is digestSlug's inverse; "" when the segment is not a digest slug.
func slugDigest(slug string) string {
	if !digestSlugRE.MatchString(slug) {
		return ""
	}
	return strings.Replace(slug, "-", ":", 1)
}

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

// ownerRef is the OwnerReference stamped on the owned Route (always) — and on the Bucket only under
// `delete`, which V1 rejects, so a V1 Bucket never carries it (Decision §5).
func ownerRef(s *v1.Site) v1.OwnerReference {
	return v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindSite, Namespace: s.Namespace, Name: s.Name},
		UID:                s.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
}

// ownedBy reports whether refs carry an OwnerReference to this Site (kind + name + uid).
func ownedBy(refs []v1.OwnerReference, s *v1.Site) bool {
	for _, r := range refs {
		if r.Kind == v1.KindSite && r.Name == s.Name && r.Namespace == s.Namespace && r.UID == s.UID {
			return true
		}
	}
	return false
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
// 404 by construction) plus the bundle rule at s.EffectivePath() over the digest-scoped prefix.
// ingress.public sets static.public on every backend; ingress.auth maps to spec.auth.
func compileRoute(s *v1.Site, digest, index string) v1.RouteSpec {
	ing := &s.Spec.Ingress
	rules := make([]v1.RouteRule, 0, len(ing.Rules)+1)
	for _, r := range ing.Rules {
		bucket := r.Bucket
		if bucket == "" {
			bucket = s.Spec.Bucket.Name
		}
		rules = append(rules, v1.RouteRule{
			Path:    r.Path,
			Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: bucket, Prefix: r.Prefix + "/", Public: ing.Public}},
		})
	}
	rules = append(rules, v1.RouteRule{
		Path: s.EffectivePath(),
		Backend: v1.RouteBackend{Static: &v1.StaticBackend{
			Bucket: s.Spec.Bucket.Name,
			Prefix: servingPrefix(s.Spec.Prefix, digest),
			Index:  index,
			SPA:    s.Spec.SPA,
			Public: ing.Public,
		}},
	})
	spec := v1.RouteSpec{Host: ing.Host, Rules: rules}
	if ing.Auth != nil {
		auth := *ing.Auth
		spec.Auth = &auth
	}
	return spec
}

// servingDigest is compileRoute's inverse — the durable record of what is served (ADR-0139 §3). It
// identifies the bundle rule by its DIGEST-SCOPED KEY PREFIX ("<prefix>/<slug>/" with a well-formed
// slug), never by its edge path, so the record survives an ingress.path change (ADR-0140 §5). A data
// mount's prefix is never digest-scoped, so the match is unambiguous.
func servingDigest(rt *v1.Route, prefix string) string {
	if rt == nil {
		return ""
	}
	for i := range rt.Spec.Rules {
		st := rt.Spec.Rules[i].Backend.Static
		if st == nil {
			continue
		}
		rest, ok := strings.CutPrefix(st.Prefix, prefix+"/")
		if !ok {
			continue
		}
		if d := slugDigest(strings.TrimSuffix(rest, "/")); d != "" {
			return d
		}
	}
	return ""
}
