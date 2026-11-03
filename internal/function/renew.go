package function

import (
	"context"
	"slices"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// reasonRenewing and renewingMessage are a Function's Degraded reason while its only replica boots after a key renewal
// (ADR-0270).
const (
	reasonRenewing  = "Renewing"
	renewingMessage = "a replica is being replaced for a fresh S3 key pair"
)

// keysRotate reports whether derived S3 key pairs rotate, so workers that carry one are renewed (ADR-0270).
func (r *Reconciler) keysRotate() bool {
	return r.s3Gateway.Enabled && r.s3Gateway.Period > 0 && r.s3Gateway.RenewalAge != nil
}

// renewal is what a worker's key renewal asks of the pass (ADR-0270 Decision 6).
type renewal int

const (
	renewNone    renewal = iota
	renewIdle            // idle after half the period: renewed now
	renewDrained         // drained at its renewal age and now idle: renewed now
	renewForced          // still busy DrainGrace into its drain: renewed now
	renewDrain           // drains: it gets no call; come back for it
)

// now reports whether w renews the worker in this pass.
func (w renewal) now() bool { return w == renewIdle || w == renewDrained || w == renewForced }

// how names a renewal in its log line.
func (w renewal) how() string {
	switch w {
	case renewIdle:
		return "idle"
	case renewDrained:
		return "drained"
	case renewForced:
		return "forced"
	}
	return ""
}

// renewMark is a worker's renewal drain: when it began, and the worker's CreatedAt, so a mark never applies to a later
// worker of the same id.
type renewMark struct{ since, created time.Time }

// renewStep decides the renewal of worker in, which carries the key pairs of identity (kind, ns, name): idle after
// half the period it is renewed now; at its renewal age it gets no further call, and it is renewed once idle, or
// DrainGrace after that at the latest, so a call in flight finishes on the old pair, still valid for a tenth of the
// period.
func (r *Reconciler) renewStep(in runtime.Instance, kind v1.Kind, ns, name string, idle bool) renewal {
	now := r.clock.Now()
	age := now.Sub(in.CreatedAt)
	r.stopMu.Lock()
	mark, draining := r.renewing[in.ID]
	draining = draining && mark.created.Equal(in.CreatedAt)
	started := false
	if !draining && age >= r.s3Gateway.RenewalAge(kind, ns, name) {
		mark, draining, started = renewMark{since: now, created: in.CreatedAt}, true, true
		r.renewing[in.ID] = mark
	}
	r.stopMu.Unlock()
	if started && !idle {
		r.logger.Info("a worker drains for a fresh S3 key pair", "namespace", ns, "name", name, "instance", in.ID, "age", age)
	}
	switch {
	case draining && idle:
		return renewDrained
	case draining && now.Sub(mark.since) >= r.drainGrace:
		return renewForced
	case draining:
		return renewDrain
	case idle && age >= r.s3Gateway.Period/2:
		return renewIdle
	}
	return renewNone
}

// renewDue reports whether worker in is due for renewal, without starting its drain.
func (r *Reconciler) renewDue(in runtime.Instance, kind v1.Kind, ns, name string, idle bool) bool {
	age := r.clock.Now().Sub(in.CreatedAt)
	return r.isRenewing(in) || age >= r.s3Gateway.RenewalAge(kind, ns, name) || idle && age >= r.s3Gateway.Period/2
}

// isRenewing reports whether worker in drains for a key renewal.
func (r *Reconciler) isRenewing(in runtime.Instance) bool {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()
	mark, ok := r.renewing[in.ID]
	return ok && mark.created.Equal(in.CreatedAt)
}

// endRenewal forgets worker id's renewal drain and renewal boot, as it is removed.
func (r *Reconciler) endRenewal(id runtime.InstanceID) {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()
	delete(r.renewing, id)
	delete(r.renewBoot, id)
}

// markRenewBoot records that worker id was renewed now, so its next worker boots for a key renewal (renewalBooting).
func (r *Reconciler) markRenewBoot(id runtime.InstanceID) {
	r.stopMu.Lock()
	defer r.stopMu.Unlock()
	r.renewBoot[id] = r.clock.Now()
}

// renewalBooting reports whether a worker among insts boots after a key renewal: created after its id was renewed, not
// ended and not listening yet. A renewal boot that listens or ends is forgotten; forgetting an ended one is defense in
// depth, as a crash's reason already comes before Renewing. A worker created before the mark is the renewed one itself,
// which may still be listed while it stops.
func (r *Reconciler) renewalBooting(insts []runtime.Instance) bool {
	booting := false
	for _, in := range insts {
		r.stopMu.Lock()
		at, ok := r.renewBoot[in.ID]
		r.stopMu.Unlock()
		switch {
		case !ok || in.CreatedAt.Before(at):
		case in.State.Terminal() || r.listening(in):
			r.stopMu.Lock()
			delete(r.renewBoot, in.ID)
			r.stopMu.Unlock()
		default:
			booting = true
		}
	}
	return booting
}

// renewKeys renews at most one running worker of fn's revision rev per pass, oldest first, finishing a drain under way
// first, so renewals roll one replica at a time:
//   - an idle worker past half the period is renewed early only when rev runs a lone replica, or all of its desired
//     replicas run and listen;
//   - a worker at its renewal age waits while another worker of rev boots after a renewal, but only until halfway to the
//     end of its pair's validity (at least a twentieth of the period), so a replacement that never listens holds it no
//     longer; a broken replica never holds it, since its pair must not expire beside one.
//
// The same pass creates a retired worker again with a fresh pair. It returns when the pass must come back (zero if it
// need not).
func (r *Reconciler) renewKeys(ctx context.Context, fn *v1.Function, rev v1.ObjectName, desired int) (time.Time, error) {
	if !r.keysRotate() || len(fn.Spec.Bindings.Blob) == 0 {
		return time.Time{}, nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return time.Time{}, err
	}
	insts = slices.DeleteFunc(insts, func(in runtime.Instance) bool { return in.Revision != rev || in.State != runtime.StateRunning })
	slices.SortFunc(insts, func(a, b runtime.Instance) int { return a.CreatedAt.Compare(b.CreatedAt) })
	ns, name := string(fn.Namespace), string(fn.Name)
	step := func(in runtime.Instance) renewal { return r.renewStep(in, v1.KindFunction, ns, name, r.idle(fn, in)) }
	if i := slices.IndexFunc(insts, r.isRenewing); i >= 0 {
		return r.renew(ctx, insts[i], ns, name, step(insts[i]))
	}
	early := desired <= 1 || len(insts) >= desired && !slices.ContainsFunc(insts, func(in runtime.Instance) bool { return !r.listening(in) })
	booting := r.renewalBooting(insts)
	renewAge := r.s3Gateway.RenewalAge(v1.KindFunction, ns, name)
	waiting := false
	for _, in := range insts {
		switch age := r.clock.Now().Sub(in.CreatedAt); {
		case age < renewAge && !early:
			continue
		case age >= renewAge && booting && age < renewAge+(r.s3Gateway.Period-renewAge)/2:
			waiting = true
			continue
		}
		if w := step(in); w != renewNone {
			return r.renew(ctx, in, ns, name, w)
		}
	}
	if waiting {
		return r.clock.Now().Add(r.drainPoll), nil
	}
	return time.Time{}, nil
}

// renew acts on step w of worker in's renewal: a drain comes back after DrainPollInterval; a renewal now retires the
// worker and marks its next one as a renewal boot.
func (r *Reconciler) renew(ctx context.Context, in runtime.Instance, ns, name string, w renewal) (time.Time, error) {
	if !w.now() {
		return r.clock.Now().Add(r.drainPoll), nil
	}
	r.logger.Info("renewing a worker for a fresh S3 key pair", "namespace", ns, "name", name, "instance", in.ID,
		"age", r.clock.Now().Sub(in.CreatedAt), "how", w.how())
	if err := r.retire(ctx, in); err != nil {
		return time.Time{}, err
	}
	r.markRenewBoot(in.ID)
	return time.Time{}, nil
}

// poolRenewal decides the renewal of the running worker of a pool whose manifest holds a member with issued env (its
// key pair); the pool's identity for the renewal age is its worker's name.
func (r *Reconciler) poolRenewal(cur []runtime.Instance, ns v1.NamespaceName, pool v1.ObjectName, manifest []poolManifestEntry) renewal {
	if !r.keysRotate() || len(cur) == 0 || cur[0].State != runtime.StateRunning ||
		!slices.ContainsFunc(manifest, func(m poolManifestEntry) bool { return m.issue != nil }) {
		return renewNone
	}
	idle := r.calls == nil || r.calls.Idle(instanceURL(ns, pool, cur[0]), r.handOutSettle)
	return r.renewStep(cur[0], v1.KindFunction, string(ns), string(pool), idle)
}
