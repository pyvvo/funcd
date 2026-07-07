// Package provider is the platform provider catalog (ADR-0082): the blueprint provider model
// in code. A provider is a shared platform capability endpoint — funcd's wasmCloud-style
// capability provider (a binding is the link, a port + ≥2 drivers the contract) — in two tiers:
// built-in (in-daemon, pure-Go, trusted core) and add-on (an out-of-daemon deployed service
// function). This package only CLASSIFIES: a Descriptor names a provider's existing ADR-0019
// four-part shape (CRD + facade + controller + port + drivers); it replaces no facade/port/driver
// and changes no runtime behavior. It is a leaf — it imports nothing from internal/ (fault lives
// at api/fault), so cataloguing the providers adds no import edge to the packages it names.
package provider

import (
	"sort"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Kind is a provider's deployment tier (the blueprint provider model).
type Kind string

const (
	// Builtin is in-daemon, pure-Go, always-on, trusted core.
	Builtin Kind = "built-in"
	// Addon is out-of-daemon: a deployed service function (cgo / heavy engine).
	Addon Kind = "add-on"
)

// Valid reports whether k is a known tier.
func (k Kind) Valid() bool { return k == Builtin || k == Addon }

// Descriptor names ONE platform provider — the umbrella over its ADR-0019 four-part shape
// (CRD + facade + controller + port + drivers). It is classification metadata, not a runtime
// handle: a descriptor in the catalog means the provider is recognized, not that it is running.
type Descriptor struct {
	Name     string   // stable id, e.g. "kv", "blob", "ingress", "s3", "log-ingest"
	Kind     Kind     // built-in | add-on
	Port     string   // the contract: the port/interface name (e.g. "kvstore.KV"); "" for an infra provider with no function-facing port
	Bindings []string // how a function consumes it (e.g. ["spec.kv"]); nil for infra (ingress/egress)
	Summary  string   // one-line human description
}

// Validate checks a descriptor is well-formed: a non-empty Name and a valid Kind.
func (d Descriptor) Validate() error {
	const op = "provider.Descriptor.Validate"
	if d.Name == "" {
		return fault.Invalidf(op, "provider descriptor has an empty Name")
	}
	if !d.Kind.Valid() {
		return fault.Invalidf(op, "provider %q has an invalid Kind %q", d.Name, d.Kind)
	}
	return nil
}

// Catalog is the immutable registry of platform providers (ADR-0002: no package-level mutable
// state). Build it with New; query it with All / ByKind / Get.
type Catalog struct {
	descriptors []Descriptor // sorted by Name
	byName      map[string]Descriptor
}

// New builds a Catalog: it runs Validate on each descriptor, then rejects a duplicate Name —
// fault.Invalid on either. Descriptors are stored sorted by Name so All / ByKind are stable.
func New(ds ...Descriptor) (*Catalog, error) {
	const op = "provider.New"
	byName := make(map[string]Descriptor, len(ds))
	sorted := make([]Descriptor, 0, len(ds))
	for _, d := range ds {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if _, dup := byName[d.Name]; dup {
			return nil, fault.Invalidf(op, "duplicate provider Name %q", d.Name)
		}
		byName[d.Name] = d
		sorted = append(sorted, d)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	return &Catalog{descriptors: sorted, byName: byName}, nil
}

// All returns every descriptor in stable Name order.
func (c *Catalog) All() []Descriptor {
	out := make([]Descriptor, len(c.descriptors))
	copy(out, c.descriptors)
	return out
}

// ByKind returns the descriptors of one tier, in stable Name order.
func (c *Catalog) ByKind(k Kind) []Descriptor {
	out := make([]Descriptor, 0, len(c.descriptors))
	for _, d := range c.descriptors {
		if d.Kind == k {
			out = append(out, d)
		}
	}
	return out
}

// Get returns the descriptor for name, or ok=false if absent.
func (c *Catalog) Get(name string) (Descriptor, bool) {
	d, ok := c.byName[name]
	return d, ok
}
