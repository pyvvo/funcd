// Package sdk is the typed Go client over the funcd control-plane REST API
// (ADR-0024). It speaks the code-first huma surface (ADR-0005/0018) at
// /apis/funcd.io/v1alpha1/..., exposing a kind-parameterized CRUD surface
// (Apply/Get/List/Delete) typed via the v1.Object interface — no generics, no
// `any`. It is on the public surface and depends only on api/** + stdlib.
package sdk

import (
	"regexp"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ReadOnlyKind reports whether the API serves only get and list for k, a kind a reconciler alone writes (ADR-0172,
// ADR-0200, ADR-0339).
func ReadOnlyKind(k v1.Kind) bool { return ReadOnlyWriter(k) != "" }

// ReadOnlyWriter names the writer of a read-only kind, for the refusal of a write to it; "" if k is writable.
func ReadOnlyWriter(k v1.Kind) string {
	switch k {
	case v1.KindRevision:
		return "the Function reconciler"
	case v1.KindAppRevision:
		return "the App reconciler"
	case v1.KindAddonRevision:
		return "the Addon reconciler"
	default:
		return ""
	}
}

// errReadOnly is the refusal of a write to a read-only kind, made before any request (ADR-0172).
func errReadOnly(op string, k v1.Kind) error {
	return fault.Invalidf(op, "%s is read-only: %s writes it", k, ReadOnlyWriter(k))
}

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
	v1.KindNamespace:       {"namespaces", false},
	v1.KindResourceGroup:   {"resourcegroups", true},
	v1.KindFunction:        {"functions", true},
	v1.KindRevision:        {"revisions", true},
	v1.KindRoute:           {"routes", true},
	v1.KindService:         {"services", true},
	v1.KindEventSource:     {"eventsources", true},
	v1.KindConfigMap:       {"configmaps", true},
	v1.KindSecret:          {"secrets", true},
	v1.KindGrant:           {"grants", true},
	v1.KindEgressPolicy:    {"egresspolicies", true},
	v1.KindInvocation:      {"invocations", true},
	v1.KindRuntimeClass:    {"runtimeclasses", false},
	v1.KindWorkerNode:      {"workernodes", false},
	v1.KindGateway:         {"gateways", false},
	v1.KindKVStore:         {"kvstores", true},
	v1.KindBucket:          {"buckets", true},
	v1.KindLake:            {"lakes", true},
	v1.KindSQLDatabase:     {"sqldatabases", true},
	v1.KindPolicy:          {"policies", true},
	v1.KindWorkflow:        {"workflows", true},
	v1.KindWorkflowRun:     {"workflowruns", true},
	v1.KindSensor:          {"sensors", true},
	v1.KindIdentity:        {"identities", true},       // ADR-0135, F100
	v1.KindRole:            {"roles", true},            // ADR-0136, F101
	v1.KindRolesAssignment: {"rolesassignments", true}, // ADR-0136, F101
	v1.KindSite:            {"sites", true},            // ADR-0139, F103
	v1.KindApp:             {"apps", true},             // ADR-0199, F113
	v1.KindAppRevision:     {"apprevisions", true},     // ADR-0200, F114
	v1.KindBackupSchedule:  {"backupschedules", true},  // ADR-0222, F111
	v1.KindBackup:          {"backups", true},          // ADR-0222, F111
	v1.KindRestore:         {"restores", true},         // ADR-0222, F111
	v1.KindAddon:           {"addons", false},          // ADR-0332, F258
	v1.KindAddonRevision:   {"addonrevisions", false},  // ADR-0339, F265
	v1.KindOIDCProvider:    {"oidcproviders", true},    // ADR-0335, F261
	v1.KindJob:             {"jobs", true},             // ADR-0347, F137
	v1.KindJobRun:          {"jobruns", true},          // ADR-0347, F137
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

// KindFromToken resolves a CLI token (plural, singular, or alias) to a v1.Kind. A token that names no kind of funcd's
// is an add-on's kind (ADR-0332 Decision 2), kept as typed: the server answers 404 when no Addon registers it.
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
	if k := strings.TrimSpace(token); v1.IsAddonKind(k) || addonToken.MatchString(k) {
		return v1.Kind(k), true
	}
	return "", false
}

// EngineKind reports whether the objects of kind run an engine whose lines funcd keeps (ADR-0342 Decision 1): a Lake,
// a SQLDatabase, or a kind funcd does not define, an add-on's, whose server answers 404 when no Addon's engines serve it.
func EngineKind(kind v1.Kind) bool {
	if kind == v1.KindLake || kind == v1.KindSQLDatabase {
		return true
	}
	_, funcds := kindDescriptors[kind]
	return !funcds
}

// addonToken is an add-on's kind typed in lower case, its singular or its plural.
var addonToken = regexp.MustCompile(`^[a-z][a-z0-9]{1,63}$`)

// descriptorFor returns the path descriptor for a kind. A kind funcd does not define is an add-on's, namespaced, at its
// plural, with no code per kind (ADR-0332 Decision 2): a kind name's lower case with an s, or a lower-case token as
// it is when it ends in s, else with an s.
func descriptorFor(kind v1.Kind) (kindDescriptor, bool) {
	if d, ok := kindDescriptors[kind]; ok {
		return d, true
	}
	switch k := string(kind); {
	case v1.IsAddonKind(k):
		return kindDescriptor{plural: v1.AddonPlural(k), namespaced: true}, true
	case addonToken.MatchString(k) && strings.HasSuffix(k, "s"):
		return kindDescriptor{plural: k, namespaced: true}, true
	case addonToken.MatchString(k):
		return kindDescriptor{plural: k + "s", namespaced: true}, true
	}
	return kindDescriptor{}, false
}
