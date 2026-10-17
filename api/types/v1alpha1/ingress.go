package v1alpha1

import (
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Ingress is the edge exposure of every exposable kind: Function, Site and CatalogService (ADR-0241).
type Ingress struct {
	// Enabled turns the resource's own address on or off. Unset: a Function or a Site follows its namespace's
	// defaultExposure (implicit on, explicit off) and a CatalogService is off.
	Enabled *bool `json:"enabled,omitempty"`
	// Host is the exact host of the resource's own address; "" is the namespace host (ADR-0240).
	Host string `json:"host,omitempty"`
	// Path is the edge path of the resource's own address; "" is "/" when Host is set, else
	// /function/<name>, /site/<name> or /catalogservice/<name>.
	Path string `json:"path,omitempty"`
	// Auth is the edge stance of the resource's own address; nil is the namespace default.
	Auth *RouteAuth `json:"auth,omitempty"`
}

// EnabledIn reports whether a Function's or a Site's own address is on in a namespace of mode (ADR-0241 Decision 2).
func (i *Ingress) EnabledIn(mode ExposureMode) bool {
	if i != nil && i.Enabled != nil {
		return *i.Enabled
	}
	return mode.Normalized() != ExposureExplicit
}

// AuthMode is the stance of the resource's own address; "" is the namespace default.
func (i *Ingress) AuthMode() AuthMode {
	if i == nil || i.Auth == nil {
		return ""
	}
	return i.Auth.Mode
}

// IngressPath is a resource's own edge path (ADR-0241 Decision 3): ingress.path, else "/" when ingress.host is set,
// else /<kind>/<name>, kind lowercased.
func IngressPath(i *Ingress, kind Kind, name ObjectName) string {
	switch {
	case i != nil && i.Path != "":
		return i.Path
	case i != nil && i.Host != "":
		return "/"
	default:
		return "/" + strings.ToLower(string(kind)) + "/" + string(name)
	}
}

// validate checks the stance and the path shape of ADR-0140: rooted, with no trailing slash unless it is "/".
func (i *Ingress) validate(op, field string) error {
	if i == nil {
		return nil
	}
	if i.Auth != nil {
		if err := validateAuthMode(i.Auth.Mode, field+".auth.mode"); err != nil {
			return err
		}
	}
	if i.Path == "" {
		return nil
	}
	if i.Path[0] != '/' {
		return fault.Invalidf(op, "%s.path %q must begin with '/'", field, i.Path)
	}
	if i.Path != "/" && strings.HasSuffix(i.Path, "/") {
		return fault.Invalidf(op, "%s.path %q must not end with '/'", field, i.Path)
	}
	return nil
}
