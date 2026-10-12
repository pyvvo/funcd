package v1alpha1

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// SQLDatabase is a SQL database served by its own rqlite engine (ADR-0232, FEAT-0011/F128): the SQL reconciler runs
// one engine container per database through the add-on runtime, with a durable host directory (ADR-0231), and
// reports it Ready once the engine answers its readiness probe.
type SQLDatabase struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SQLDatabaseSpec   `json:"spec"`
	Status     SQLDatabaseStatus `json:"status,omitempty"`
}

// SQLDatabaseSpec is the desired state of a SQLDatabase: the engine's cpu and memory bounds. An empty memory bound
// means the reconciler's default (256Mi); the spec is not rewritten.
type SQLDatabaseSpec struct {
	Resources ResourceSpec `json:"resources,omitempty"`
}

// SQLDatabaseStatus is the observed state of a SQLDatabase. Bindings counts the Functions that bind it.
type SQLDatabaseStatus struct {
	Status   `json:",inline"`
	Bindings int `json:"bindings,omitempty"`
}

// GroupVersionKind returns the constant GVK for SQLDatabase.
func (d *SQLDatabase) GroupVersionKind() GroupVersionKind { return KindSQLDatabase.GVK() }

// memoryQuantity is the size grammar the runtime parses (internal/platform/units.ParseBytes): an integer with an
// optional Ki, Mi, Gi or Ti suffix.
var memoryQuantity = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi|Ti)?$`)

// Validate performs envelope validation, then refuses a cpu or memory value the runtime cannot parse, so a bound is
// never silently ignored (ADR-0232 Decision 1).
func (d *SQLDatabase) Validate() error {
	const op = "SQLDatabase.Validate"
	if err := validateMeta(d.TypeMeta, &d.ObjectMeta, KindSQLDatabase); err != nil {
		return err
	}
	if m := strings.TrimSpace(d.Spec.Resources.Memory); m != "" && !memoryQuantity.MatchString(m) {
		return fault.Invalidf(op, "spec.resources.memory %q is not a size such as 64Mi or 1Gi", d.Spec.Resources.Memory)
	}
	if c := strings.TrimSpace(d.Spec.Resources.CPU); c != "" {
		if n, err := strconv.ParseFloat(c, 64); err != nil || n <= 0 {
			return fault.Invalidf(op, "spec.resources.cpu %q is not a positive number of cores", d.Spec.Resources.CPU)
		}
	}
	return nil
}
