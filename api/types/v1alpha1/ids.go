// Package v1alpha1 holds the v1alpha1 API types — shared between server, client,
// and SDK. It imports api/fault for validation errors and is itself a stdlib-only
// public-contract leaf (ADR-0002 §7: api/** imports nothing from internal/** / pkg/**).
package v1alpha1

import (
	"regexp"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// dnsLabel is the regex for a valid DNS label (RFC 1123).
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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
