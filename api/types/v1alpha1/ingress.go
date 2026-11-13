package v1alpha1

import (
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Ingress is the edge exposure of every exposable kind: Function, Site and Lake (ADR-0241).
type Ingress struct {
	// Enabled turns the resource's own address on or off. Unset: a Function or a Site follows its namespace's
	// defaultExposure (implicit on, explicit off) and a Lake is off.
	Enabled *bool `json:"enabled,omitempty" doc:"Turns the resource's own address on or off. Unset, it is on unless the namespace's spec.defaultExposure is explicit; a Lake's address is on only when this is true."`
	// Host is the exact host of the resource's own address; "" is the namespace host (ADR-0240).
	Host string `json:"host,omitempty" doc:"The exact host of the resource's own address. Empty is the namespace's host, <namespace>.<server.baseDomain>, or, in the default namespace, any host that no other namespace reserves."`
	// Path is the edge path of the resource's own address; "" is "/" when Host is set, else
	// /function/<name>, /site/<name> or /lake/<name>.
	Path string `json:"path,omitempty" doc:"The edge path of the resource's own address, which begins with / and does not end with / unless it is /. Empty is / when host is set, else /function/<name>, /site/<name> or /lake/<name> by kind."`
	// Auth is the edge stance of the resource's own address; nil is the namespace default.
	Auth *RouteAuth `json:"auth,omitempty" doc:"How the edge checks callers at the resource's own address; unset, the namespace's spec.edgeDefaults.auth decides, else open. A Lake refuses it, since the Lake gateway checks each caller."`
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
