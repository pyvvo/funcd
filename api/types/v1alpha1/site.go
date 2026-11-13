package v1alpha1

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
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
	Image string `json:"image" doc:"The OCI artifact of the Site's static files, which funcdctl push --site makes, named by a tag or a digest. funcd resolves a tag to a digest when the spec changes, so a tag that moves after a deploy changes nothing until the spec changes again. Each deployed bundle stays in the Bucket, so pointing image back at an earlier one serves it again without an upload. A bundle with a _runtime entry at its root does not deploy: funcd keeps that name for the files it generates."`
	// Buckets declares exactly the one Bucket its bindings.blob names (ADR-0248 Decision 7): by name the Site creates
	// it with its served prefix and no owner, and refuses an existing one without its marker; by ref the served prefix
	// must exist without an owner, and the Site never edits the Bucket. deletion: delete is refused (ADR-0139).
	Buckets []BucketEntry `json:"buckets,omitempty" doc:"Exactly the one Bucket that bindings.blob names, never with deletion: delete; a Site in an App sets none and refers to that Bucket by ref. By name, the Site creates the Bucket with that prefix, without an owner, and the prefixes listed here, which may not repeat it; a Bucket of that name that the Site did not create leaves the Site not Ready. By ref, the Site is Ready only once the Bucket holds that prefix without an owner, and it never changes the Bucket."`
	// Bindings names the served prefix: one alias-free {bucket, prefix}; objects land under <prefix>/<digest-slug>/.
	// The prefix is immutable after creation (the site-prefix-immutable admission).
	Bindings SiteBindings `json:"bindings,omitzero" doc:"Where the Site stores and serves its bundles: one Bucket prefix, in blob."`
	// Index is the document served for "/" / a directory / (when SPA) a miss. Default "index.html".
	Index string `json:"index,omitempty" doc:"The bundle's file served for /, by its file name for any other path that ends in /, and with spa for a path that matches no file. It is a path in the bundle, with no leading /; a bundle without it does not deploy, and the previous one keeps serving. Empty means site.defaultIndex, index.html by default."`
	// SPA serves Index (200) for an un-matched path under the bundle rule so a client-side router owns
	// routing. It NEVER applies to a data mount (Ingress.Rules), whose miss is always 404.
	SPA bool `json:"spa,omitempty" doc:"Serve the index, with status 200, for a path under the bundle's path that matches no file, so that a client-side router handles it. ingress.rules never fall back to it: a missing file there is 404."`
	// Ingress declares the Site-owned Route (named after the Site). Required in V1.
	Ingress SiteIngress `json:"ingress" doc:"Where the edge serves the Site: its own address and its rules, through a Route of the Site's name that the Site writes. A Route of that name that the Site does not own leaves the Site not Ready."`
	// RuntimeConfig makes the Site serve one generated config.json beside its bundle (ADR-0239). Not the function
	// runtime.
	RuntimeConfig *SiteRuntimeConfig `json:"runtimeConfig,omitempty" doc:"Values each deployment sets without a rebuild, served as config.json beside the bundle. The file is public: never put a secret in it."`
}

// SiteRuntimeConfig holds the members of a Site's generated config.json (ADR-0239).
type SiteRuntimeConfig struct {
	// Values are keys and their string values; a key here wins over the same key from ConfigMap.
	Values map[string]string `json:"values,omitempty" doc:"Keys and the string values that config.json gives them; a key here wins over the same key from configMap. Each key is an environment variable name: ASCII letters, digits and _, not starting with a digit. The file holds at most 32768 bytes (32 KiB): values that alone pass it are refused, and a file that configMap takes past it leaves the Site not Ready, serving its last config.json, if any."`
	// ConfigMap names a ConfigMap of the Site's namespace whose data adds keys.
	ConfigMap ObjectName `json:"configMap,omitempty" doc:"A ConfigMap of the Site's namespace whose data adds keys to config.json; a key of values wins over the same key here. In an App, it may name one of the App's configMaps entries. While it does not exist, the Site is not Ready, though its bundle still deploys and its last config.json, if any, keeps serving; a change to it rewrites only the file."`
}

const (
	// SiteRuntimeConfigFile is the name of the file a Site's runtimeConfig generates (ADR-0239).
	SiteRuntimeConfigFile = "config.json"
	// SiteRuntimeConfigMaxBytes caps the generated file, which the app waits for before it starts (ADR-0239).
	SiteRuntimeConfigMaxBytes = 32 << 10
)

// RenderSiteRuntimeConfig is the body of a Site's config.json (ADR-0239): one JSON object of the members, in key
// order, since encoding/json sorts map keys. Admission and the reconciler share it, so both measure the same bytes.
func RenderSiteRuntimeConfig(members map[string]string) []byte {
	if members == nil {
		members = map[string]string{}
	}
	b, _ := json.Marshal(members) // a map[string]string always marshals
	return b
}

// ServedBucket is the Bucket the Site serves from: its bindings.blob entry's bucket, "" when it has none.
func (s SiteSpec) ServedBucket() ObjectName {
	if len(s.Bindings.Blob) == 0 {
		return ""
	}
	return s.Bindings.Blob[0].Bucket
}

// ServedPrefix is the prefix the Site serves from: its bindings.blob entry's prefix, "" when it has none.
func (s SiteSpec) ServedPrefix() string {
	if len(s.Bindings.Blob) == 0 {
		return ""
	}
	return s.Bindings.Blob[0].Prefix
}

// BucketEntry is the Site's one buckets entry, nil when it has none.
func (s SiteSpec) BucketEntry() *BucketEntry {
	if len(s.Buckets) == 0 {
		return nil
	}
	return &s.Buckets[0]
}

// Declared is the name an entry declares or refers to: Name, else Ref.
func (e BucketEntry) Declared() ObjectName {
	if e.Name != "" {
		return e.Name
	}
	return e.Ref
}

// SiteIngress declares the Site-owned Route (ADR-0241): the shared Ingress, its path being where the bundle is served
// (EffectivePath, ADR-0140); a path change re-programs the Route with no re-upload, and the bundle must be built for
// the new base path unless BaseHref is set. Rules add sibling prefixes.
type SiteIngress struct {
	Ingress `json:",inline"`
	// BaseHref makes the Site serve a copy of its index whose <base href> is its mount (ADR-0239 Decision 10).
	BaseHref bool `json:"baseHref,omitempty" doc:"Serve a copy of the bundle's index whose <base href> is the Site's mount, so a bundle built with relative URLs runs at any path, deep links included."`
	// Rules are additional bucket prefixes served alongside the app (the data the app fetches). A rule
	// MAY nest under Path (e.g. /bi/data under /bi) — longest-path-first gives the nested rule priority.
	Rules []SiteRule `json:"rules,omitempty" doc:"More Bucket prefixes the Site serves, each at its own path, with the bundle's host, auth and enabled. A rule may sit below the bundle's path, since the longest matching path wins, and a missing file there is 404, never the bundle's index."`
}

// SiteRule serves one bucket prefix at an edge path. Never SPA: a miss is 404, not the app shell.
type SiteRule struct {
	// Path is the edge path; must begin "/" and must not be "/" (that is the bundle's).
	Path string `json:"path" doc:"The path where the prefix is served, matching it and every path below it. It begins with / and may not be /, another rule's path or the bundle's path, which is ingress.path or its default. With runtimeConfig, it may not be the path of config.json either."`
	// Bucket names a Bucket in this Site's namespace; "" ⇒ the Site's own Bucket.
	Bucket ObjectName `json:"bucket,omitempty" doc:"A Bucket of the Site's namespace, which buckets need not declare; empty means the Bucket of bindings.blob. While it does not exist, the Site's Route serves nothing, the bundle included, and the Site is not Ready."`
	// Prefix is the Bucket sub-domain served at Path.
	Prefix string `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" doc:"The prefix of that Bucket served at path: <path>/<key> reads the object <prefix>/<key> as the Bucket holds it, since nothing is copied."`
}

// SiteStatus is the observed state: which build is live and where it is served from. Every field is
// DERIVED on each reconcile — Digest/ServingPrefix from the owned Route's bundle rule (what is
// actually served) — never read back as durable: the store is last-writer-wins on status and an apply
// wipes it. Status.ObservedGeneration gates tag resolution (ADR-0139 Decision §2).
type SiteStatus struct {
	Status `json:",inline"`
	// Digest is the manifest digest the owned Route currently serves.
	Digest string `json:"digest,omitempty" pattern:"^sha256:[a-f0-9]{64}$" doc:"The digest of the bundle that the Site's Route is set to serve, empty before the first deploy. A deploy that fails leaves the previous bundle's digest."`
	// ServingPrefix is the digest-scoped key prefix the owned Route is programmed with.
	ServingPrefix string `json:"servingPrefix,omitempty" doc:"Where that bundle's files are in the Bucket: <prefix>/sha256-<hex>/, from the prefix of spec.bindings.blob and the digest."`
	// Objects and Bytes are the materialized bundle's observed size.
	Objects int   `json:"objects,omitempty" doc:"The number of that bundle's files in the Bucket, without the files funcd generates for the Site. It reads 0 while the Site cannot resolve its image or use its Bucket."`
	Bytes   int64 `json:"bytes,omitempty" doc:"The total size, in bytes, of the files that objects counts."`
}

// EffectivePath is where the bundle is served (ADR-0140): spec.ingress.path, else "/" when
// spec.ingress.host is set, else "/site/<metadata.name>". It lives HERE, not in internal/site, because
// Validate must check it too and api/** cannot import internal/** — one definition, two callers.
func (s *Site) EffectivePath() string {
	return IngressPath(&s.Spec.Ingress.Ingress, KindSite, s.Name)
}

// RuntimeConfigPath is the edge path of the config.json that spec.runtimeConfig generates: under EffectivePath
// (ADR-0239), so it moves with the bundle.
func (s *Site) RuntimeConfigPath() string {
	if p := s.EffectivePath(); p != "/" {
		return p + "/" + SiteRuntimeConfigFile
	}
	return "/" + SiteRuntimeConfigFile
}

// RuntimeConfigPrefix is the key prefix of a Site's generated config.json: <prefix>/_runtime/<site>/, one per Site so
// two Sites on one prefix never share it (ADR-0239). "_runtime" is never a digest slug, so Served never takes it for
// the bundle.
func (s *Site) RuntimeConfigPrefix() string {
	return s.Spec.ServedPrefix() + "/_runtime/" + string(s.Name) + "/"
}

// digestSlugRE recognizes the digest-scoped key segment the bundle rule is programmed with.
var digestSlugRE = regexp.MustCompile(`^sha256-[a-f0-9]{64}$`)

// SlugDigest is the digest a digest-scoped key segment names (sha256-<hex> → sha256:<hex>); "" when the segment is
// not one.
func SlugDigest(slug string) string {
	if !digestSlugRE.MatchString(slug) {
		return ""
	}
	return strings.Replace(slug, "-", ":", 1)
}

// Served returns the bundle backend the Site's own Route serves and its config.json backend (nil when the Site has
// none); ok is false when the Site does not control rt with its current UID or rt serves no build (ADR-0242 Decision
// 3). The bundle rule is found by its digest-scoped prefix <prefix>/<slug>/, never by its edge path, so it survives an
// ingress.path change (ADR-0140 Decision 5). One definition for the Site reconciler and the Route reconciler.
func (s *Site) Served(rt *Route) (bundle, config *StaticBackend, ok bool) {
	if rt == nil || !ControlledBy(rt.OwnerReferences, KindSite, s.UID) {
		return nil, nil, false
	}
	cfg := s.RuntimeConfigPrefix()
	for i := range rt.Spec.Rules {
		st := rt.Spec.Rules[i].Backend.Static
		if st == nil {
			continue
		}
		if st.Prefix == cfg && config == nil {
			c := *st
			config = &c
			continue
		}
		rest, found := strings.CutPrefix(st.Prefix, s.Spec.ServedPrefix()+"/")
		if found && bundle == nil && SlugDigest(strings.TrimSuffix(rest, "/")) != "" {
			b := *st
			bundle = &b
		}
	}
	return bundle, config, bundle != nil
}

// validateBucket checks ADR-0248 Decision 7: exactly one buckets entry, by name or by ref, never deletion: delete (the
// shared DeletionPolicy enum still advertises it; ADR-0139 has no drain-then-delete teardown); exactly one bindings.blob
// entry naming that bucket, with a DNS-1123 prefix; extra prefixes only on a named Bucket, unique, and never the
// served one, which is added implicitly with no owner.
func (s SiteSpec) validateBucket(op string) error {
	if len(s.Buckets) != 1 {
		return fault.Invalidf(op, "spec.buckets must declare exactly the one Bucket spec.bindings.blob names, not %d", len(s.Buckets))
	}
	e := s.Buckets[0]
	if (e.Name == "") == (e.Ref == "") {
		return fault.Invalidf(op, "spec.buckets[0] must set exactly one of name or ref")
	}
	if err := e.Declared().Validate(); err != nil {
		return fault.Invalidf(op, "spec.buckets[0]: %v", err)
	}
	switch e.Deletion {
	case "", DeletionRetain:
	case DeletionDelete:
		return fault.Invalidf(op, "spec.buckets[0].deletion %q is not implemented in V1 (needs a drain-then-delete teardown, ADR-0139); use %q", DeletionDelete, DeletionRetain)
	default:
		return fault.Invalidf(op, "spec.buckets[0].deletion %q must be %q", e.Deletion, DeletionRetain)
	}
	if len(s.Bindings.Blob) != 1 {
		return fault.Invalidf(op, "spec.bindings.blob must name exactly one {bucket, prefix}, not %d", len(s.Bindings.Blob))
	}
	b := s.Bindings.Blob[0]
	if b.Bucket != e.Declared() {
		return fault.Invalidf(op, "spec.bindings.blob[0].bucket %q is not the Bucket spec.buckets declares (%q)", b.Bucket, e.Declared())
	}
	if !dnsLabel.MatchString(b.Prefix) {
		return fault.Invalidf(op, "spec.bindings.blob[0].prefix %q is not a valid DNS-1123 label", b.Prefix)
	}
	if e.Ref != "" && len(e.Prefixes) > 0 {
		return fault.Invalidf(op, "spec.buckets[0] refers to Bucket %q, which the Site never edits, so it lists no prefixes", e.Ref)
	}
	seen := make(map[string]bool, len(e.Prefixes))
	for _, p := range e.Prefixes {
		if !dnsLabel.MatchString(p.Name) {
			return fault.Invalidf(op, "spec.buckets[0].prefixes name %q is not a valid DNS-1123 label", p.Name)
		}
		if p.Name == b.Prefix {
			return fault.Invalidf(op, "spec.buckets[0].prefixes must not repeat the served prefix %q (it is added implicitly with no owner)", p.Name)
		}
		if seen[p.Name] {
			return fault.Invalidf(op, "spec.buckets[0].prefixes name %q is duplicated", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// GroupVersionKind returns the constant GVK for Site.
func (s *Site) GroupVersionKind() GroupVersionKind { return KindSite.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *Site) GetStatus() *Status { return &s.Status.Status }

// Validate performs envelope + semantic validation: Image non-empty; the one buckets entry and the one
// bindings.blob entry naming it (validateBucket); Index a relative path (no leading '/'); ingress.path, when set, begins "/" and is "/" or carries no trailing slash, and the
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
	if strings.HasPrefix(s.Spec.Index, "/") {
		return fault.Invalidf(op, "spec.index %q must be a relative path (no leading '/')", s.Spec.Index)
	}
	if err := s.Spec.validateBucket(op); err != nil {
		return err
	}
	ing := &s.Spec.Ingress
	if err := ing.validate(op, "spec.ingress"); err != nil {
		return err
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
	return s.validateRuntimeConfig()
}

// validateRuntimeConfig checks spec.runtimeConfig (ADR-0239 Decision 7): its keys follow the ConfigMap key rule, its
// configMap is an object name, no ingress rule claims the generated file's path (a trailing slash matches the same
// path), and its values alone render within the cap. A ConfigMap's keys are checked when it is written, and what it
// adds to the size is the reconciler's to report.
func (s *Site) validateRuntimeConfig() error {
	const op = "Site.Validate"
	rc := s.Spec.RuntimeConfig
	if rc == nil {
		return nil
	}
	for _, k := range slices.Sorted(maps.Keys(rc.Values)) {
		if err := validateEnvKey(op, "spec.runtimeConfig.values key", k); err != nil {
			return err
		}
	}
	if rc.ConfigMap != "" {
		if err := rc.ConfigMap.Validate(); err != nil {
			return fault.Invalidf(op, "spec.runtimeConfig.configMap: %v", err)
		}
	}
	file := s.RuntimeConfigPath()
	for i, r := range s.Spec.Ingress.Rules {
		if strings.TrimRight(r.Path, "/") == file {
			return fault.Invalidf(op, "spec.ingress.rules[%d].path %q is where spec.runtimeConfig serves %s (ADR-0239)", i, r.Path, file)
		}
	}
	if n := len(RenderSiteRuntimeConfig(rc.Values)); n > SiteRuntimeConfigMaxBytes {
		return fault.Invalidf(op, "spec.runtimeConfig.values render %d bytes, over the %d-byte cap of %s (ADR-0239)", n, SiteRuntimeConfigMaxBytes, SiteRuntimeConfigFile)
	}
	return nil
}
