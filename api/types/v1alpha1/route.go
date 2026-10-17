package v1alpha1

import (
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// Route is a namespaced resource that declares the edge exposure of one or more Functions
// (ADR-0110, F79): which Functions are reachable at the edge (host/path → Function) and — via
// the per-route policy fields added by F74–F78 — how. Status-bearing; Ready once every rule's
// backend Function exists and the route is programmed into the edge router.
type Route struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RouteSpec   `json:"spec"`
	Status     RouteStatus `json:"status,omitempty"`
}

// RouteSpec is the declarative edge exposure. V1 carries matching + backend + the F77 auth stance;
// the remaining policy fields (tls/limits/observability/cors) are added by F74/F75/F76/F78.
type RouteSpec struct {
	// Host is an exact host match. "" is the namespace host <namespace>.<baseDomain>, or for default any host but
	// reserved ones (ADR-0240); an `explicit` default requires a non-empty Host (ADR-0110).
	Host string `json:"host,omitempty"`
	// Rules lists the path rules, matched longest-prefix-first. At least one is required.
	Rules []RouteRule `json:"rules"`
	// Auth is the edge authentication stance for this Route's rules (ADR-0113, F77); a rule's own auth beats it, and
	// nil inherits the namespace default, else open (ADR-0241 Decision 8).
	Auth *RouteAuth `json:"auth,omitempty"`
	// Enabled, false, keeps the Route Disabled: it serves and claims nothing. Unset is true, except for a Route a
	// Site or a Function owns in an explicit namespace (ADR-0241 Decision 6).
	Enabled *bool `json:"enabled,omitempty"`
}

// RouteAuth is the F77 auth stance on a Route.
type RouteAuth struct {
	Mode AuthMode `json:"mode,omitempty"`
}

// AuthMode selects whether the edge authenticates the caller for a target (ADR-0113, F77).
type AuthMode string

const (
	// AuthAuthenticated requires a valid bearer + a PDP allow.
	AuthAuthenticated AuthMode = "authenticated"
	// AuthOpen allows anonymous access (the zero/default stance, phased).
	AuthOpen AuthMode = "open"
)

// Schema renders the huma enum for AuthMode (empty accepted, normalizes to open).
func (AuthMode) Schema(huma.Registry) *huma.Schema {
	return enumSchema("", string(AuthAuthenticated), string(AuthOpen))
}

// RouteRule matches a path (+ optional methods) and names the backend Function.
type RouteRule struct {
	Path     string       `json:"path"`               // must begin "/"
	PathType PathType     `json:"pathType,omitempty"` // Exact | Prefix (default Prefix)
	Methods  []HTTPMethod `json:"methods,omitempty"`  // empty ⇒ all methods
	Backend  RouteBackend `json:"backend"`
	// Auth is this rule's stance; nil is the Route's spec.auth (ADR-0241 Decision 8).
	Auth *RouteAuth `json:"auth,omitempty"`
}

// RouteBackend is an exactly-one-of union (ADR-0120, F82, ADR-0242): a Function backend (the activator hop), a Static
// backend (a Bucket prefix served directly through the edge) or a Site backend (the Site's current build). Validate
// enforces the one-of. Routes are never shared across namespaces (blueprint.md:467).
type RouteBackend struct {
	// Function is the target Function in this Route's namespace. Optional since F82: a static-only
	// rule sets no function (the `omitempty` reflects the union — exactly one arm is set).
	Function ObjectName `json:"function,omitempty"`
	// Static serves a Bucket-prefix static site directly through the edge (F82); nil ⇒ a function backend.
	Static *StaticBackend `json:"static,omitempty"`
	// Site serves the current build of the named Site in this Route's namespace (ADR-0242).
	Site ObjectName `json:"site,omitempty" doc:"A Site of this Route's namespace: the rule serves the build that Site serves now, and its config.json at <path>/config.json, following each new build. A Prefix rule; the Route may not carry the Site's own name."`
}

// StaticBackend serves a Bucket prefix as a static site (ADR-0120, F82): index resolution,
// content-types, a weak (ModTime,Size) ETag + conditional GET, HTTP Range, an optional SPA
// fallback, and a pinned three-tier Cache-Control — served through the FEAT-0006 edge with no
// function code. The bytes are the same per-namespace Bucket view the S3 frontend serves (ADR-0080).
type StaticBackend struct {
	// Bucket is the Bucket in THIS Route's namespace whose objects are served (binding-as-grant:
	// the Route reads only this declared Bucket; the reconciler validates it exists → BucketNotFound).
	Bucket ObjectName `json:"bucket"`
	// Prefix is the key prefix within the Bucket that roots the site; it ends with "/" (e.g. "bi/") so it
	// never also matches a sibling prefix; "" ⇒ the bucket root.
	Prefix string `json:"prefix,omitempty"`
	// Index is the document served for "/" / a directory / (when SPA) a miss. Default "index.html".
	Index string `json:"index,omitempty"`
	// SPA, when true, serves Index (200) for any un-matched path so a client-side router owns routing;
	// when false, an un-matched path is 404.
	SPA bool `json:"spa,omitempty"`
}

// PathType selects exact vs segment-prefix matching.
type PathType string

const (
	PathTypePrefix PathType = "Prefix"
	PathTypeExact  PathType = "Exact"
)

// Schema renders the huma enum for PathType (empty is accepted and defaults to Prefix).
func (PathType) Schema(huma.Registry) *huma.Schema {
	return enumSchema("", string(PathTypePrefix), string(PathTypeExact))
}

// HTTPMethod is an allowed request method.
type HTTPMethod string

const (
	MethodGet     HTTPMethod = "GET"
	MethodHead    HTTPMethod = "HEAD"
	MethodPost    HTTPMethod = "POST"
	MethodPut     HTTPMethod = "PUT"
	MethodPatch   HTTPMethod = "PATCH"
	MethodDelete  HTTPMethod = "DELETE"
	MethodOptions HTTPMethod = "OPTIONS"
)

func allMethods() []HTTPMethod {
	return []HTTPMethod{MethodGet, MethodHead, MethodPost, MethodPut, MethodPatch, MethodDelete, MethodOptions}
}

// Schema renders the huma enum for HTTPMethod.
func (HTTPMethod) Schema(huma.Registry) *huma.Schema {
	vals := allMethods()
	s := make([]string, len(vals))
	for i, m := range vals {
		s[i] = string(m)
	}
	return enumSchema(s...)
}

// RouteStatus holds the observed state (Ready / NotReady with reason BackendNotFound | BucketNotFound |
// SiteNotFound | SiteNotReady | Disabled | HostRequired | HostReserved | RouteConflict, set by the reconciler).
type RouteStatus struct {
	Status `json:",inline"`
	// Host is the host the Route claims: spec.host, else its namespace host, else "" (any host but reserved ones)
	// (ADR-0240).
	Host string `json:"host,omitempty"`
}

// GroupVersionKind returns the constant GVK for Route.
func (r *Route) GroupVersionKind() GroupVersionKind { return KindRoute.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (r *Route) GetStatus() *Status { return &r.Status.Status }

// Validate performs envelope + semantic validation. It is namespace-agnostic: the
// host-required-in-explicit and cross-namespace (host,path,method) collision rules depend on the
// namespace mode and the full Route set, so the reconciler enforces those (→ NotReady), not here.
func (r *Route) Validate() error {
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindRoute); err != nil {
		return err
	}
	const op = "Route.Validate"
	if len(r.Spec.Rules) == 0 {
		return fault.Invalidf(op, "spec.rules must list at least one rule")
	}
	seen := make(map[string]bool, len(r.Spec.Rules))
	for i := range r.Spec.Rules {
		rule := &r.Spec.Rules[i]
		if rule.Path == "" || rule.Path[0] != '/' {
			return fault.Invalidf(op, "spec.rules[%d].path %q must begin with '/'", i, rule.Path)
		}
		switch rule.PathType {
		case "", PathTypePrefix, PathTypeExact:
		default:
			return fault.Invalidf(op, "spec.rules[%d].pathType %q must be Prefix or Exact", i, rule.PathType)
		}
		if err := validateBackend(r, rule, i, op); err != nil {
			return err
		}
		if rule.Auth != nil {
			if err := validateAuthMode(rule.Auth.Mode, fmt.Sprintf("spec.rules[%d].auth.mode", i)); err != nil {
				return err
			}
		}
		for _, m := range rule.Methods {
			if !validMethod(m) {
				return fault.Invalidf(op, "spec.rules[%d] has invalid method %q", i, m)
			}
		}
		// Uniqueness within this Route: no two rules with the same (path, method-set).
		key := rule.Path + "|" + methodSetKey(rule.Methods)
		if seen[key] {
			return fault.Invalidf(op, "spec.rules[%d] duplicates the (path, methods) of an earlier rule", i)
		}
		seen[key] = true
	}
	if r.Spec.Auth != nil {
		if err := validateAuthMode(r.Spec.Auth.Mode, "spec.auth.mode"); err != nil {
			return err
		}
	}
	return nil
}

// validateBackend enforces the RouteBackend exactly-one-of union (ADR-0120, ADR-0242): each rule sets exactly
// one of backend.function / backend.static / backend.site. A function arm keeps the DNS-1123 label check; a static
// arm requires a DNS-1123 bucket, a "/"-terminated prefix and a relative index. A site arm (ADR-0242) is a Prefix rule
// naming a Site other than the Route's own name, which is that Site's own Route (ADR-0241 Decision 5).
func validateBackend(r *Route, rule *RouteRule, i int, op string) error {
	arms := 0
	for _, set := range []bool{rule.Backend.Function != "", rule.Backend.Static != nil, rule.Backend.Site != ""} {
		if set {
			arms++
		}
	}
	if arms != 1 {
		return fault.Invalidf(op, "spec.rules[%d].backend must set exactly one of function, static or site", i)
	}
	hasFn := rule.Backend.Function != ""
	if site := rule.Backend.Site; site != "" {
		switch {
		case !dnsLabel.MatchString(string(site)):
			return fault.Invalidf(op, "spec.rules[%d].backend.site %q is not a valid DNS-1123 label", i, site)
		case rule.PathType == PathTypeExact:
			return fault.Invalidf(op, "spec.rules[%d].backend.site needs a Prefix rule", i)
		case site == r.Name:
			return fault.Invalidf(op, "spec.rules[%d].backend.site %q names this Route's own name, which is that Site's own Route; give the Route another name", i, site)
		}
		return nil
	}
	if hasFn {
		if !dnsLabel.MatchString(string(rule.Backend.Function)) {
			return fault.Invalidf(op, "spec.rules[%d].backend.function %q is not a valid DNS-1123 label", i, rule.Backend.Function)
		}
		return nil
	}
	st := rule.Backend.Static
	if !dnsLabel.MatchString(string(st.Bucket)) {
		return fault.Invalidf(op, "spec.rules[%d].backend.static.bucket %q is not a valid DNS-1123 label", i, st.Bucket)
	}
	if st.Prefix != "" && !strings.HasSuffix(st.Prefix, "/") {
		return fault.Invalidf(op, "spec.rules[%d].backend.static.prefix %q must end with '/'", i, st.Prefix)
	}
	if strings.HasPrefix(st.Index, "/") {
		return fault.Invalidf(op, "spec.rules[%d].backend.static.index %q must be a relative path (no leading '/')", i, st.Index)
	}
	return nil
}

func validMethod(m HTTPMethod) bool {
	for _, a := range allMethods() {
		if m == a {
			return true
		}
	}
	return false
}

// methodSetKey builds a stable key for a rule's method set ("" ⇒ all methods).
func methodSetKey(ms []HTTPMethod) string {
	if len(ms) == 0 {
		return "*"
	}
	// small, fixed set — a stable ordered join over the canonical method order.
	key := ""
	for _, a := range allMethods() {
		for _, m := range ms {
			if m == a {
				key += string(a) + ","
			}
		}
	}
	return key
}
