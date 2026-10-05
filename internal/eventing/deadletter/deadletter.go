// Package deadletter is the eventing dead-letter queue PORT (ADR-0118, F85): the durable park for a
// Sensor action-delivery that failed past the bounded retry cap, or that was parked before its attempts ran
// out (ADR-0156: the Sensor's queue was full, the Sensor changed, or the daemon shut down). It is deliberately
// BUS-DRIVER-INDEPENDENT — the same store serves the in-memory and NATS buses, so the reliability guarantee
// never hinges on a JetStream feature (this package imports NO bus). Records are engine-minted, high-volume operational data
// (like the ADR-0094 run store), NOT a CRD: they live in a dedicated Badger instance surfaced by a read +
// replay/discard control-plane surface, never the metastore.
//
// The port has two V1 drivers, each in its own subpackage, both held to the shared Contract: badger
// (in-memory mode for tests/dev, on-disk for production) and memory (a small pure-map driver for
// cross-platform tests). See deadletter/badger and deadletter/memory.
package deadletter

import (
	"context"
	"encoding/json"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// DeadLetter is a terminally-undeliverable Sensor action, or one parked before its attempts ran out (ADR-0156),
// kept for inspection/replay. It carries the full firing CloudEvent (Payload) so replay can re-inject it
// verbatim, plus the provenance to re-find the action on the LIVE Sensor spec. It is built from the Sensor's in-memory delivery unit (the dependency's
// source/event tuple), never by re-parsing the CloudEvent.
type DeadLetter struct {
	ID        string           `json:"id"`        // ULID — time-sortable within a namespace (cheap cap eviction)
	Namespace v1.NamespaceName `json:"namespace"` //
	Sensor    v1.ObjectName    `json:"sensor"`    // the Sensor whose action failed
	Source    v1.ObjectName    `json:"source"`    // the EventSource — from the delivery unit's dependency tuple
	Event     v1.ObjectName    `json:"event"`     // the event name — from the delivery unit's dependency tuple
	Action    string           `json:"action"`    // the Sensor action name (do[].name)
	Payload   json.RawMessage  `json:"payload"`   // the full CloudEvent JSON (re-injected verbatim on replay)
	Attempts  int              `json:"attempts"`  // delivery attempts made before dead-lettering (0 if never attempted)
	Reason    string           `json:"reason"`    // the terminal delivery error, or the park reason (ADR-0156)
	FailedAt  time.Time        `json:"failedAt"`  //
}

// Store is the dead-letter queue port (ADR-0002 §1) — bus-driver-independent. Records are engine-owned
// operational data, not a CRD. Implementations are the single source of truth for parked failures.
type Store interface {
	// Put creates or replaces a dead-letter record.
	Put(ctx context.Context, dl DeadLetter) error
	// Update replaces an existing record, or returns fault.NotFound if it is absent: replay re-parks with it,
	// so a discard or a retention sweep made during the replay stays in effect.
	Update(ctx context.Context, dl DeadLetter) error
	// List returns a namespace's dead letters, newest first (ID descending).
	List(ctx context.Context, ns v1.NamespaceName) ([]DeadLetter, error)
	// Get returns one record, or fault.NotFound if absent.
	Get(ctx context.Context, ns v1.NamespaceName, id string) (DeadLetter, error)
	// Delete removes a record (discard / replay-success); absent is not an error.
	Delete(ctx context.Context, ns v1.NamespaceName, id string) error
	// SweepExpired is store-global (no ns) and iterates ALL namespaces: the TTL is global — it evicts
	// entries older than retention across every namespace — while the count cap is enforced per-namespace,
	// by grouping keys under each dl/<ns>/ prefix and evicting the oldest over-cap in each. retention<=0
	// disables the TTL; maxPerNS<=0 disables the cap. Returns the number of records evicted.
	SweepExpired(ctx context.Context, retention time.Duration, maxPerNS int) (int, error)
	// Close releases the driver's resources.
	Close() error
}

// Clone deep-copies a record so drivers never alias caller-owned memory.
func Clone(dl DeadLetter) (DeadLetter, error) {
	b, err := json.Marshal(dl)
	if err != nil {
		return DeadLetter{}, err
	}
	var out DeadLetter
	if err := json.Unmarshal(b, &out); err != nil {
		return DeadLetter{}, err
	}
	return out, nil
}
