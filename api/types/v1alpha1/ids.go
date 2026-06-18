// Package v1alpha1 holds the v1alpha1 API types — shared between server, client,
// and SDK. It imports api/fault for validation errors and github.com/danielgtaylor/huma/v2
// for the SchemaProvider methods that carry leaf constraints into the generated OpenAPI
// (ADR-0048). It remains a public-contract leaf: the ADR-0002 §7 depguard rule still holds —
// api/** imports nothing from internal/** / pkg/** (huma is a third-party SDK, allowed).
package v1alpha1

import (
	"regexp"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// DNSLabel is the RFC-1123 DNS-label pattern — the SINGLE source for both server-side validation
// (dnsLabel below) and the generated OpenAPI schema (the SchemaProvider methods), so the published
// contract can never drift from the admission check.
const DNSLabel = `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`

// dnsLabel is the compiled DNSLabel pattern, used by every typed-ID Validate().
var dnsLabel = regexp.MustCompile(DNSLabel)

// dnsLabelSchema is the OpenAPI fragment a DNS-label typed ID contributes (huma SchemaProvider).
// Every field of such a type inherits the pattern + length by reflection — one declaration, all sites.
func dnsLabelSchema() *huma.Schema {
	mn, mx := 1, 63
	return &huma.Schema{Type: huma.TypeString, Pattern: DNSLabel, MinLength: &mn, MaxLength: &mx}
}

// toAny widens a []string into the []any huma's Schema.Enum requires (the library boundary —
// huma's Enum field is []any). One declaration; enumSchema and every typed-enum Schema() use it.
//
//nolint:forbidigo // huma's Schema.Enum is []any — this unexported helper is that library boundary.
func toAny(ss []string) []any {
	out := make([]any, len(ss)) //nolint:forbidigo // huma Schema.Enum is []any (see above)
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// enumSchema is the OpenAPI fragment a typed string-enum contributes (huma SchemaProvider):
// a string constrained to the given values. One declaration, referenced by every enum leaf.
func enumSchema(vals ...string) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: toAny(vals)}
}

// Schema makes ObjectName carry its DNS-1123 constraint into the generated OpenAPI (huma
// SchemaProvider) — the same pattern Validate() enforces. Every field typed ObjectName inherits it.
func (ObjectName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }

// Schema carries NamespaceName's DNS-1123 constraint into the generated OpenAPI.
func (NamespaceName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }

// Schema carries ResourceGroupName's DNS-1123 constraint into the generated OpenAPI.
func (ResourceGroupName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }

// Schema carries FunctionName's DNS-1123 constraint into the generated OpenAPI.
func (FunctionName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }

// NamespaceName is a DNS-label-qualified namespace identifier.
type NamespaceName string

// Validate checks the NamespaceName against DNS-label rules.
func (n NamespaceName) Validate() error {
	if n == "" {
		return fault.Invalidf("NamespaceName.Validate", "namespace name must not be empty")
	}
	if !dnsLabel.MatchString(string(n)) {
		return fault.Invalidf("NamespaceName.Validate", "%q is not a valid DNS label", n)
	}
	return nil
}

// FunctionName is a DNS-label-qualified function identifier.
type FunctionName string

// Validate checks the FunctionName against DNS-label rules.
func (n FunctionName) Validate() error {
	if n == "" {
		return fault.Invalidf("FunctionName.Validate", "function name must not be empty")
	}
	if !dnsLabel.MatchString(string(n)) {
		return fault.Invalidf("FunctionName.Validate", "%q is not a valid DNS label", n)
	}
	return nil
}

// RuntimeName is a DNS-label-qualified RuntimeClass name reference (FunctionSpec.Runtime,
// RevisionSpec.Runtime) — ADR-0048. Typed so its DNS-1123 constraint reaches both the
// generated OpenAPI (Schema) and the admission check (Validate) from one declaration.
type RuntimeName string

// Validate checks the RuntimeName is non-empty and a valid DNS label.
func (n RuntimeName) Validate() error {
	if n == "" {
		return fault.Invalidf("RuntimeName.Validate", "runtime name must not be empty")
	}
	if !dnsLabel.MatchString(string(n)) {
		return fault.Invalidf("RuntimeName.Validate", "%q is not a valid DNS label", n)
	}
	return nil
}

// Schema carries RuntimeName's DNS-1123 constraint into the generated OpenAPI.
func (RuntimeName) Schema(huma.Registry) *huma.Schema { return dnsLabelSchema() }

// RevisionID is a unique identifier for a function revision.
type RevisionID string

// Validate checks that the RevisionID is non-empty.
func (r RevisionID) Validate() error {
	if r == "" {
		return fault.Invalidf("RevisionID.Validate", "revision id must not be empty")
	}
	return nil
}

// ObjectName is a DNS-1123-label-qualified resource name.
type ObjectName string

// Validate checks the ObjectName against DNS-label rules.
func (n ObjectName) Validate() error {
	if n == "" {
		return fault.Invalidf("ObjectName.Validate", "object name must not be empty")
	}
	if !dnsLabel.MatchString(string(n)) {
		return fault.Invalidf("ObjectName.Validate", "%q is not a valid DNS label", n)
	}
	return nil
}

// ResourceGroupName is a DNS-1123-label-qualified resource group identifier.
type ResourceGroupName string

// Validate checks the ResourceGroupName against DNS-label rules.
func (n ResourceGroupName) Validate() error {
	if n == "" {
		return fault.Invalidf("ResourceGroupName.Validate", "resource group name must not be empty")
	}
	if !dnsLabel.MatchString(string(n)) {
		return fault.Invalidf("ResourceGroupName.Validate", "%q is not a valid DNS label", n)
	}
	return nil
}

// UID is a server-assigned opaque unique identifier.
type UID string

// Tags is an optional free-form set of key-value labels on a resource.
// map[string]string is allowed under ADR-0002: the any-ban targets interface{}/any/map[string]any.
type Tags map[string]string
