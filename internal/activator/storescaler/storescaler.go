// Package storescaler is the V1 store-backed activator.Scaler driver (ADR-0016): it
// records a function's scale intent as its partitioned status Phase — the wake edge
// Idle→Deploying for replicas>=1, the reclaim edge Ready/Degraded/empty→Idle for
// replicas==0 (activator.Reclaimable, ADR-0169, ADR-0185) — via store.Update. It is idempotent and
// edge-respecting (it never flips an off-diagram transition), and it re-reads and retries on
// the store's RV fault.Conflict so a concurrent controller (P-M) status write never drops the intent. A ref pinned
// to a held revision (ADR-0190 Decision 6) records the same edges on that Revision's status, never the Function's.
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

// ScaleTo records the partitioned Phase intent for fn (Idle→Deploying on wake, a
// Reclaimable phase→Idle on reclaim), retrying the read-modify-write on the store's RV
// conflict. A wake of a Failed function is refused with fault.Unavailable naming its state. A ref
// pinned to a held revision writes that Revision's phase instead; a pinned Function that is gone or
// has another UID is fault.NotFound, so no namesake is woken.
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
		if fn.Revision != "" && f.UID != fn.UID {
			return fault.NotFoundf(op, "pinned revision %q of function %s/%s (uid %s) is gone: the function has uid %s",
				fn.Revision, fn.Namespace, fn.Name, fn.UID, f.UID)
		}
		if activator.HeldRevision(f, fn) {
			return s.scaleRevision(ctx, fn, target)
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

// scaleRevision records target on the status of the held revision fn is pinned to, as ScaleTo does on a Function:
// a wake of a Failed revision is refused, and a Revision another Function controls is fault.NotFound.
func (s *scaler) scaleRevision(ctx context.Context, fn activator.FunctionRef, target v1.Phase) error {
	const op = "storescaler.ScaleTo"
	for range maxAttempts {
		obj, err := s.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, fn.Revision)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "get revision %s/%s", fn.Namespace, fn.Revision)
		}
		rev, ok := obj.(*v1.Revision)
		if !ok {
			return fault.Internalf(op, "object %s/%s is not a Revision", fn.Namespace, fn.Revision)
		}
		if !v1.ControlledBy(rev.OwnerReferences, v1.KindFunction, fn.UID) {
			return fault.NotFoundf(op, "pinned revision %q of function %s/%s (uid %s) belongs to another function",
				fn.Revision, fn.Namespace, fn.Name, fn.UID)
		}
		if target == v1.PhaseDeploying {
			if ferr := activator.RevisionFailedFault(op, fn, rev); ferr != nil {
				return ferr
			}
		}
		next, change := transition(rev.Status.Phase, target)
		if !change {
			return nil
		}
		rev.Status.Phase = next
		if _, err := s.store.Update(ctx, rev); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				continue
			}
			return fault.Wrapf(err, fault.KindOf(err), op, "update revision %s/%s", fn.Namespace, fn.Revision)
		}
		return nil
	}
	return fault.Unavailablef(op, "scale revision %s/%s did not converge after %d attempts (conflict)",
		fn.Namespace, fn.Revision, maxAttempts)
}

// targetPhase maps a replica target to the Phase the activator owns: 0 → Idle (sleep),
// ≥1 → Deploying (wake).
func targetPhase(replicas int) v1.Phase {
	if replicas <= 0 {
		return v1.PhaseIdle
	}
	return v1.PhaseDeploying
}

// transition returns the Phase to persist over the re-read phase cur of a Function or held
// Revision and whether a write is needed, honoring the activator's partition of the phase (ADR-0016
// C2): the wake edge fires only from a sleeping/initial phase (Idle/Pending/empty → Deploying) —
// never an off-diagram Ready→Deploying; the reclaim edge fires only from a phase
// activator.ReclaimablePhase admits (Ready/Degraded/empty → Idle), never from Pending, Failed,
// Deploying or Terminating (ADR-0169, ADR-0185). P-M owns Deploying→Ready/Failed.
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
	case v1.PhaseIdle: // reclaim (ADR-0169)
		if !activator.ReclaimablePhase(cur) {
			return cur, false
		}
		return v1.PhaseIdle, true
	default:
		return cur, false
	}
}
