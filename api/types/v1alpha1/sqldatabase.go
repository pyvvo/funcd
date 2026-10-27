package v1alpha1

import (
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

// SQLDatabaseSpec is the desired state of a SQLDatabase: the engine's cpu and memory bounds and the Function that may
// write (ADR-0233). An empty memory bound means the reconciler's default (256Mi); the spec is not rewritten.
type SQLDatabaseSpec struct {
	Resources ResourceSpec `json:"resources,omitempty"`
	Owner     ObjectName   `json:"owner,omitempty"`
	// Quota caps the database's total size (ADR-0292).
	Quota SizeQuota `json:"quota,omitzero"`
}

// SQLDatabaseStatus is the observed state of a SQLDatabase. Bindings counts the Functions that bind it.
type SQLDatabaseStatus struct {
	Status   `json:",inline"`
	Bindings int `json:"bindings,omitempty"`
}

// GroupVersionKind returns the constant GVK for SQLDatabase.
func (d *SQLDatabase) GroupVersionKind() GroupVersionKind { return KindSQLDatabase.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject, so an App judges its database's Ready
// (ADR-0236 Decision 3).
func (d *SQLDatabase) GetStatus() *Status { return &d.Status.Status }

// Validate performs envelope validation, then refuses a cpu or memory value the runtime cannot parse, so a bound is
// never silently ignored (ADR-0232 Decision 1).
func (d *SQLDatabase) Validate() error {
	const op = "SQLDatabase.Validate"
	if err := validateMeta(d.TypeMeta, &d.ObjectMeta, KindSQLDatabase); err != nil {
		return err
	}
	if err := d.Spec.Quota.Validate(op, "spec.quota"); err != nil {
		return err
	}
	if err := d.Spec.Resources.Validate(op, "spec.resources"); err != nil {
		return err
	}
	if d.Spec.Owner != "" && !dnsLabel.MatchString(string(d.Spec.Owner)) {
		return fault.Invalidf(op, "spec.owner %q is not a valid Function name", d.Spec.Owner)
	}
	return nil
}
