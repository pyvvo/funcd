package v1alpha1

import (
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Site is a namespaced, status-bearing static web deliverable (ADR-0139, F103): an immutable bundle
// materialized into a governed Bucket prefix and exposed at the edge, with its adjacent Bucket and
// Route declared inline and owned by it. Ready only once the bundle is materialized and its index is
// present, so a Site is never Ready over an empty prefix.
type Site struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SiteSpec   `json:"spec"`
	Status     SiteStatus `json:"status,omitempty"`
}

// SiteSpec is the desired state: which bundle, where it materializes, and how it is exposed.
type SiteSpec struct {
	// Image is the site bundle OCI ref (oci-layout://<dir>[:<tag>] or a registry ref; a tag or a
	// digest). A tag is resolved to a digest once per spec generation — the author never types one.
	Image string `json:"image"`
	// Bucket declares the Bucket the bundle materializes into: adopted if present, created if absent.
	Bucket SiteBucket `json:"bucket"`
	// Prefix is the Bucket sub-domain rooting the site; objects land under <Prefix>/<digest-slug>/.
	// Immutable after creation (the site-prefix-immutable admission).
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Index is the document served for "/" / a directory / (when SPA) a miss. Default "index.html".
	Index string `json:"index,omitempty"`
	// SPA serves Index (200) for an un-matched path under the bundle rule so a client-side router owns
	// routing. It NEVER applies to a data mount (Ingress.Rules), whose miss is always 404.
	SPA bool `json:"spa,omitempty"`
	// Ingress declares the Site-owned Route (named after the Site). Required in V1.
	Ingress SiteIngress `json:"ingress"`
}

// SiteBucket declares the target Bucket (the WorkflowKVStore pattern): adopted if present, created if
// absent; Deletion governs teardown only.
type SiteBucket struct {
	// Name is the Bucket name; a DNS-1123 label.
	Name ObjectName `json:"name"`
	// Deletion is "retain" (default — the Bucket outlives the Site). "delete" is rejected in V1.
	Deletion DeletionPolicy `json:"deletion,omitempty"`
	// Prefixes are additional sub-domains materialized on the Bucket verbatim (owners included). The
	// Site's own Prefix is added implicitly with NO owner and must not be repeated here.
	Prefixes []BucketPrefix `json:"prefixes,omitempty"`
}

// SiteIngress declares the Site-owned Route. The bundle is served at EffectivePath; Rules add sibling
// prefixes.
type SiteIngress struct {
	// Host is the exact host match on the owned Route; "" matches any host (ADR-0110 requires a
	// non-empty host in an `explicit`-mode namespace).
	Host string `json:"host,omitempty"`
	// Path is the edge path the bundle is served at (ADR-0140). Empty ⇒ "/" when Host is set, else
	// "/site/<metadata.name>" — so several host-less Sites share one listener without colliding. It
	// begins "/" and carries no trailing slash unless it is exactly "/". Mutable: a change re-programs
	// the Route with no re-upload, but the bundle must be BUILT for the new base path (funcd never
	// rewrites served content).
	Path string `json:"path,omitempty"`
	// Public sets static.public on every compiled backend (ADR-0120 §4 precedence preserved).
	Public bool `json:"public,omitempty"`
	// Auth is the edge auth stance copied onto the owned Route; nil ⇒ inherit the namespace default.
	Auth *RouteAuth `json:"auth,omitempty"`
	// Rules are additional bucket prefixes served alongside the app (the data the app fetches). A rule
	// MAY nest under Path (e.g. /bi/data under /bi) — longest-path-first gives the nested rule priority.
	Rules []SiteRule `json:"rules,omitempty"`
}

// SiteRule serves one bucket prefix at an edge path. Never SPA: a miss is 404, not the app shell.
type SiteRule struct {
	// Path is the edge path; must begin "/" and must not be "/" (that is the bundle's).
	Path string `json:"path"`
	// Bucket names a Bucket in this Site's namespace; "" ⇒ the Site's own Bucket.
	Bucket ObjectName `json:"bucket,omitempty"`
	// Prefix is the Bucket sub-domain served at Path.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// SiteStatus is the observed state: which build is live and where it is served from. Every field is
// DERIVED on each reconcile — Digest/ServingPrefix from the owned Route's bundle rule (what is
// actually served) — never read back as durable: the store is last-writer-wins on status and an apply
// wipes it. Status.ObservedGeneration gates tag resolution (ADR-0139 Decision §2).
type SiteStatus struct {
	Status `json:",inline"`
	// Digest is the manifest digest the owned Route currently serves.
	Digest string `json:"digest,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
	// ServingPrefix is the digest-scoped key prefix the owned Route is programmed with.
	ServingPrefix string `json:"servingPrefix,omitempty"`
	// Objects and Bytes are the materialized bundle's observed size.
	Objects int   `json:"objects,omitempty"`
	Bytes   int64 `json:"bytes,omitempty"`
}

// EffectivePath is where the bundle is served (ADR-0140): spec.ingress.path, else "/" when
// spec.ingress.host is set, else "/site/<metadata.name>". It lives HERE, not in internal/site, because
// Validate must check it too and api/** cannot import internal/** — one definition, two callers.
func (s *Site) EffectivePath() string {
	if p := s.Spec.Ingress.Path; p != "" {
		return p
	}
	if s.Spec.Ingress.Host != "" {
		return "/"
	}
	return "/site/" + string(s.Name)
}

// GroupVersionKind returns the constant GVK for Site.
func (s *Site) GroupVersionKind() GroupVersionKind { return KindSite.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *Site) GetStatus() *Status { return &s.Status.Status }

// Validate performs envelope + semantic validation: Image non-empty; Prefix a DNS-1123 label;
// Index a relative path (no leading '/'); Deletion ∈ {"", retain} — `delete` is Invalid in V1 with an
// explicit "not implemented" message (the DeletionPolicy enum is shared with Workflow, so the generated
// OpenAPI still advertises `delete`); spec.bucket.prefixes must not repeat spec.prefix and must have
// unique names; ingress.path, when set, begins "/" and is "/" or carries no trailing slash, and the
// RESOLVED path (EffectivePath, derived or explicit) does not collide with an ingress rule — validating
// only the written field would let a derived collision reach the reconciler as an unmapped store error
// (ADR-0140); every ingress rule Path begins "/" and is neither "/" nor a duplicate;
// ingress.public together with ingress.auth.mode == authenticated is Invalid (the ADR-0120 §4
// contradiction, caught here so the reconciler never writes a Route the store's Validate refuses).
// Cross-resource state (does the Bucket exist) is the RECONCILER's (→ NotReady), never Validate.
func (s *Site) Validate() error {
	const op = "Site.Validate"
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindSite); err != nil {
		return err
	}
	if s.Spec.Image == "" {
		return fault.Invalidf(op, "spec.image is required")
	}
	if !dnsLabel.MatchString(s.Spec.Prefix) {
		return fault.Invalidf(op, "spec.prefix %q is not a valid DNS-1123 label", s.Spec.Prefix)
	}
	if strings.HasPrefix(s.Spec.Index, "/") {
		return fault.Invalidf(op, "spec.index %q must be a relative path (no leading '/')", s.Spec.Index)
	}
	if err := s.Spec.Bucket.Name.Validate(); err != nil {
		return fault.Invalidf(op, "spec.bucket.name: %v", err)
	}
	switch s.Spec.Bucket.Deletion {
	case "", DeletionRetain:
	case DeletionDelete:
		return fault.Invalidf(op, "spec.bucket.deletion %q is not implemented in V1 (no owner-reference collector exists); use %q", DeletionDelete, DeletionRetain)
	default:
		return fault.Invalidf(op, "spec.bucket.deletion %q must be %q", s.Spec.Bucket.Deletion, DeletionRetain)
	}
	seenPrefix := make(map[string]bool, len(s.Spec.Bucket.Prefixes))
	for _, p := range s.Spec.Bucket.Prefixes {
		if !dnsLabel.MatchString(p.Name) {
			return fault.Invalidf(op, "spec.bucket.prefixes name %q is not a valid DNS-1123 label", p.Name)
		}
		if p.Name == s.Spec.Prefix {
			return fault.Invalidf(op, "spec.bucket.prefixes must not repeat spec.prefix %q (it is added implicitly with no owner)", p.Name)
		}
		if seenPrefix[p.Name] {
			return fault.Invalidf(op, "spec.bucket.prefixes name %q is duplicated", p.Name)
		}
		seenPrefix[p.Name] = true
	}
	ing := &s.Spec.Ingress
	if ing.Auth != nil {
		if err := validateAuthMode(ing.Auth.Mode, "spec.ingress.auth.mode"); err != nil {
			return err
		}
		if ing.Public && ing.Auth.Mode == AuthAuthenticated {
			return fault.Invalidf(op, "spec.ingress.public conflicts with spec.ingress.auth.mode=authenticated: public must not override an explicit authenticated stance")
		}
	}
	if ing.Path != "" {
		if ing.Path[0] != '/' {
			return fault.Invalidf(op, "spec.ingress.path %q must begin with '/'", ing.Path)
		}
		if ing.Path != "/" && strings.HasSuffix(ing.Path, "/") {
			return fault.Invalidf(op, "spec.ingress.path %q must not end with '/'", ing.Path)
		}
	}
	mount := s.EffectivePath()
	seenPath := make(map[string]bool, len(ing.Rules))
	for i, r := range ing.Rules {
		if r.Path == "" || r.Path[0] != '/' {
			return fault.Invalidf(op, "spec.ingress.rules[%d].path %q must begin with '/'", i, r.Path)
		}
		if r.Path == "/" {
			return fault.Invalidf(op, "spec.ingress.rules[%d].path must not be \"/\" (the bundle is served there)", i)
		}
		if r.Path == mount {
			return fault.Invalidf(op, "spec.ingress.rules[%d].path %q collides with the bundle mount %q (spec.ingress.path, or its default)", i, r.Path, mount)
		}
		if seenPath[r.Path] {
			return fault.Invalidf(op, "spec.ingress.rules[%d].path %q duplicates an earlier rule", i, r.Path)
		}
		seenPath[r.Path] = true
		if r.Bucket != "" {
			if err := r.Bucket.Validate(); err != nil {
				return fault.Invalidf(op, "spec.ingress.rules[%d].bucket: %v", i, err)
			}
		}
		if !dnsLabel.MatchString(r.Prefix) {
			return fault.Invalidf(op, "spec.ingress.rules[%d].prefix %q is not a valid DNS-1123 label", i, r.Prefix)
		}
	}
	return nil
}
