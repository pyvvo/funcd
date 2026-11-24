package v1alpha1

import (
	"slices"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// WorkloadRef names a workload of the object's namespace by its kind and its name (ADR-0351 Decision 3, from
// ADR-0275's prefix owner): a Function, a Lake and a Job may share a name, so the name alone does not say which. Each
// field that takes one checks the kinds it accepts.
type WorkloadRef struct {
	Kind Kind       `json:"kind" enum:"Function,Lake,Job" doc:"The workload's kind. A Function, a Lake and a Job may share a name, so the kind says which one it is; each field names the kinds it accepts."`
	Name ObjectName `json:"name" doc:"The workload's name, in the object's namespace."`
}

// FunctionOwner names Function name.
func FunctionOwner(name ObjectName) WorkloadRef { return WorkloadRef{Kind: KindFunction, Name: name} }

// LakeOwner names Lake name.
func LakeOwner(name ObjectName) WorkloadRef { return WorkloadRef{Kind: KindLake, Name: name} }

// JobOwner names Job name.
func JobOwner(name ObjectName) WorkloadRef { return WorkloadRef{Kind: KindJob, Name: name} }

// IsZero reports whether no workload is named.
func (o WorkloadRef) IsZero() bool { return o == WorkloadRef{} }

// String is the workload as messages name it: its kind and its quoted name.
func (o WorkloadRef) String() string { return string(o.Kind) + " " + strconv.Quote(string(o.Name)) }

// validate checks a set ref: one of kinds, and a DNS-1123 label as its name.
func (o WorkloadRef) validate(op, field string, kinds ...Kind) error {
	if o.IsZero() {
		return nil
	}
	if !slices.Contains(kinds, o.Kind) {
		names := make([]string, len(kinds))
		for i, k := range kinds {
			names[i] = string(k)
		}
		return fault.Invalidf(op, "%s: kind %q is not %s", field, o.Kind, strings.Join(names, " or "))
	}
	if !dnsLabel.MatchString(string(o.Name)) {
		return fault.Invalidf(op, "%s: name %q is not a valid DNS-1123 label", field, o.Name)
	}
	return nil
}
