// Package sdk is the typed Go client over the funcd control-plane REST API
// (ADR-0024). It speaks the code-first huma surface (ADR-0005/0018) at
// /apis/funcd.io/v1alpha1/..., exposing a kind-parameterized CRUD surface
// (Apply/Get/List/Delete) typed via the v1.Object interface — no generics, no
// `any`. It is on the public surface and depends only on api/** + stdlib.
package sdk

import (
	"strings"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// kindDescriptor is the client's knowledge of one kind's REST path shape.
type kindDescriptor struct {
	plural     string
	namespaced bool
}

// kindDescriptors maps every v1 kind to its path plural + scope. The plurals are
// taken verbatim from the server's route registration (ADR-0018) — not naively
// derived (note egresspolicies, runtimeclasses).
//
//nolint:gochecknoglobals // lookup table, effectively constant
var kindDescriptors = map[v1.Kind]kindDescriptor{
	v1.KindNamespace:     {"namespaces", false},
	v1.KindResourceGroup: {"resourcegroups", true},
	v1.KindFunction:      {"functions", true},
	v1.KindRevision:      {"revisions", true},
	v1.KindRoute:         {"routes", true},
	v1.KindService:       {"services", true},
	v1.KindEventSource:   {"eventsources", true},
	v1.KindConfig:        {"configs", true},
	v1.KindSecret:        {"secrets", true},
	v1.KindGrant:         {"grants", true},
	v1.KindEgressPolicy:  {"egresspolicies", true},
	v1.KindInvocation:    {"invocations", true},
	v1.KindRuntimeClass:  {"runtimeclasses", false},
	v1.KindWorkerNode:    {"workers", false},
	v1.KindGateway:       {"gateways", false},
	v1.KindKVStore:       {"kvstores", true},
	v1.KindPolicy:        {"policies", true},
}

// kindAliases are short CLI tokens (kubectl-style) for a few common kinds.
//
//nolint:gochecknoglobals // lookup table, effectively constant
var kindAliases = map[string]v1.Kind{
	"fn":  v1.KindFunction,
	"fns": v1.KindFunction,
	"ns":  v1.KindNamespace,
	"rg":  v1.KindResourceGroup,
	"rgs": v1.KindResourceGroup,
	"svc": v1.KindService,
}

// KindFromToken resolves a CLI token (plural, singular, or alias) to a v1.Kind.
func KindFromToken(token string) (v1.Kind, bool) {
	t := strings.ToLower(strings.TrimSpace(token))
	if k, ok := kindAliases[t]; ok {
		return k, true
	}
	for kind, d := range kindDescriptors {
		if t == d.plural || t == strings.ToLower(string(kind)) {
			return kind, true
		}
	}
	return "", false
}

// descriptorFor returns the path descriptor for a kind.
func descriptorFor(kind v1.Kind) (kindDescriptor, bool) {
	d, ok := kindDescriptors[kind]
	return d, ok
}
