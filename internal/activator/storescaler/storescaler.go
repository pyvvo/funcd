// Package storescaler is the V1 store-backed activator.Scaler driver (ADR-0016): it
// records a function's scale intent as its partitioned status Phase — the wake edge
// Idle→Deploying for replicas>=1, the reclaim edge *→Idle for replicas==0 — via
// store.Update. It is idempotent and edge-respecting (it never flips an off-diagram
// transition), and it re-reads and retries on the store's RV fault.Conflict so a
// concurrent controller (P-M) status write never drops the intent.
package storescaler

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/store"
)

// maxAttempts bounds the optimistic-concurrency retry loop on fault.Conflict.
const maxAttempts = 5

type scaler struct {
	store store.Store
}

// New returns a store-backed activator.Scaler over st.
func New(st store.Store) activator.Scaler {
	return &scaler{store: st}
}

// ScaleTo records the partitioned Phase intent for fn (Idle→Deploying on wake,
// *→Idle on reclaim), retrying the read-modify-write on the store's RV conflict.
// A wake of a Failed function is refused with fault.Unavailable naming its state.
func (s *scaler) ScaleTo(ctx context.Context, fn activator.FunctionRef, replicas int) error {
	const op = "storescaler.ScaleTo"
	gvk := v1.KindFunction.GVK()
	target := targetPhase(replicas)

	for range maxAttempts {
		obj, err := s.store.Get(ctx, gvk, fn.Namespace, fn.Name)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "get function %s/%s", fn.Namespace, fn.Name)
		}
		f, ok := obj.(*v1.Function)
		if !ok {
			return fault.Internalf(op, "object %s/%s is not a Function", fn.Namespace, fn.Name)
		}
		if target == v1.PhaseDeploying {
			if ferr := activator.FailedFault(op, f); ferr != nil {
				return ferr
			}
		}
		next, change := transition(f.Status.Phase, target)
		if !change {
			return nil // idempotent / edge-respecting no-op
		}
		f.Status.Phase = next
		if _, err := s.store.Update(ctx, f); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				continue // RV race with a concurrent writer — re-read and retry
			}
			return fault.Wrapf(err, fault.KindOf(err), op, "update function %s/%s", fn.Namespace, fn.Name)
		}
		return nil
	}
	return fault.Unavailablef(op, "scale %s/%s did not converge after %d attempts (conflict)",
		fn.Namespace, fn.Name, maxAttempts)
}

// targetPhase maps a replica target to the Phase the activator owns: 0 → Idle (sleep),
// ≥1 → Deploying (wake).
func targetPhase(replicas int) v1.Phase {
	if replicas <= 0 {
		return v1.PhaseIdle
	}
	return v1.PhaseDeploying
}

// transition returns the Phase to persist and whether a write is needed, honoring the
// activator's partition of Function.Status.Phase (ADR-0016 C2): the wake edge fires
// only from a sleeping/initial phase (Idle/Pending/empty → Deploying) — never an
// off-diagram Ready→Deploying; the reclaim edge fires from any live phase (→ Idle) but
// not from Terminating (a delete in progress). P-M owns Deploying→Ready/Failed.
func transition(cur, target v1.Phase) (v1.Phase, bool) {
	if cur == target {
		return cur, false
	}
	switch target {
	case v1.PhaseDeploying: // wake
		if cur == v1.PhaseIdle || cur == v1.PhasePending || cur == "" {
			return v1.PhaseDeploying, true
		}
		return cur, false // already Deploying/Ready/… — buffering, not a Phase flip, covers a stale miss
	case v1.PhaseIdle: // reclaim
		if cur == v1.PhaseTerminating {
			return cur, false
		}
		return v1.PhaseIdle, true
	default:
		return cur, false
	}
}
