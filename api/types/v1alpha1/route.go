package v1alpha1

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
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
	// Host is an exact host match; "" matches any host. In an `explicit`-mode namespace the
	// reconciler requires a non-empty Host (the tenant discriminator, ADR-0110).
	Host string `json:"host,omitempty"`
	// Rules lists the path rules, matched longest-prefix-first. At least one is required.
	Rules []RouteRule `json:"rules"`
	// Auth is the edge authentication stance for this Route (ADR-0113, F77). nil ⇒ inherit the
	// namespace default; unset overall ⇒ open (phased). Applies to all Rules (per-rule auth is V2).
	Auth *RouteAuth `json:"auth,omitempty"`
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
}

// RouteBackend names the target Function (in this Route's namespace — routes are never shared
// across namespaces, blueprint.md:467).
type RouteBackend struct {
	Function ObjectName `json:"function"`
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

// RouteStatus holds the observed state (Ready / NotReady with reason BackendNotFound |
// HostRequired | RouteConflict, set by the reconciler).
type RouteStatus struct {
	Status `json:",inline"`
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
		if !dnsLabel.MatchString(string(rule.Backend.Function)) {
			return fault.Invalidf(op, "spec.rules[%d].backend.function %q is not a valid DNS-1123 label", i, rule.Backend.Function)
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
