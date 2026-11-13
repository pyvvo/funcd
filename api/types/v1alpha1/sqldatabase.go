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
	Resources ResourceSpec `json:"resources,omitempty" doc:"The cpu and memory of the database's engine. An empty memory means 256Mi, and an empty cpu sets no cpu limit. A change replaces the running engine."`
	Owner     ObjectName   `json:"owner,omitempty" doc:"The one Function that may write the database, in its namespace, besides the writers that a RolesAssignment names. In a database that a Function declares by name, it defaults to that Function, and in a Workflow's sql it names a step. With no owner, only those writers can write it."`
	// Quota caps the database's total size (ADR-0292).
	Quota SizeQuota `json:"quota,omitzero" doc:"A cap on the database's total size, the bytes of its pages in use. Once it is reached, an execute gets 507 unless each of its statements is a DELETE or a DROP. A change keeps the engine running."`
}

// SQLDatabaseStatus is the observed state of a SQLDatabase. Bindings counts the Functions that bind it.
type SQLDatabaseStatus struct {
	Status   `json:",inline"`
	Bindings int `json:"bindings,omitempty" doc:"The number of Functions whose spec.bindings.sql names the database."`
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
