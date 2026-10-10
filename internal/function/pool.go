package function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/scheduler"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// poolManifestEntry is one row of the pool worker's FUNCD_POOL_MANIFEST (ADR-0044): the
// admitted member's invocable name, its materialized artifact path, and its handler export.
// The pure pooling policy never builds this — the reconciler serializes it from admitted
// members (ADR-0046 Contracts).
type poolManifestEntry struct {
	Name     string `json:"name"`
	Artifact string `json:"artifact"`
	Handler  string `json:"handler"`
	// Contract is the delivered ADR-0059 contract-blob path (ADR-0123) the pool worker compiles
	// its I/O validator from at member init — the pool analog of the solo worker's
	// FUNCD_CONTRACT_PATH. Empty ⇒ no schema delivered (dev/legacy); the worker falls back to a
	// module-baked validator. A contracted member with a set-but-broken path fails the host closed.
	Contract string `json:"contract,omitempty"`
	// Env is set in this member's env only: its own credentials and bundle dir (memberEnv).
	Env map[string]string `json:"env,omitempty"`
	// code is the code the row runs the member at; unexported, so it is neither in the manifest nor its signature.
	code memberCode
}

// memberCode is the code a pool row runs a member at, as a Revision snapshots it (ADR-0020).
type memberCode struct{ image, digest, handler string }

// poolHold is the manifest a key's pool workers were last built to hold: its signature and each member's code.
type poolHold struct {
	sig   string
	codes map[v1.ObjectName]memberCode
}

// poolHostFor selects the pool-host launch prefix for a runtime (ADR-0050): the longest registered
// pool-family prefix that matches rt (e.g. "python" → pool.py). Failing that, the default
// poolShimCommand (the Node pool.mjs) applies — but ONLY to a node-family runtime that no registered
// *runtime* shim family (shimByFamily) matches (ADR-0149 Decision 1). Any other runtime stays solo
// rather than running its artifact in a Node host (issue #371). nil ⇒ no host ⇒ solo.
func (r *Reconciler) poolHostFor(rt v1.RuntimeName) []string {
	best, cmd := "", []string(nil)
	for family, c := range r.poolShimsByFamily {
		if len(family) > len(best) && strings.HasPrefix(string(rt), family) {
			best, cmd = family, c
		}
	}
	if cmd != nil {
		return cmd
	}
	if !isNodeFamily(rt) {
		return nil
	}
	for family := range r.shimByFamily {
		if strings.HasPrefix(string(rt), family) {
			return nil
		}
	}
	return r.poolShimCommand
}

// assign resolves a function's pooling placement (ADR-0046/0050). A function is solo unless it declares a worker id
// AND a pool host exists for its runtime family (pooled). Otherwise it lists every function of the same key from the
// store and runs the pure Assigner over them, so admission is a stable function of declared membership.
func (r *Reconciler) assign(ctx context.Context, fn *v1.Function, idx accessIndex) (pooling.Assignment, error) {
	const op = "function.assign"
	key, pooled := r.poolKeyFor(fn, idx)
	if !pooled {
		return pooling.Assignment{Pooled: false}, nil // no worker id or no host for this runtime → solo
	}
	sameKey, err := r.sameKeyFunctions(ctx, key, idx)
	if err != nil {
		return pooling.Assignment{}, fault.Wrapf(err, fault.KindOf(err), op, "list same-key functions")
	}
	return r.assigner.Assign(fn, key, sameKey, r.poolLimit)
}

// sameKeyFunctions returns every function in the store of key (ns, runtime, worker-id, access),
// Ready or not — the membership the cap + manifest are computed over (ADR-0046 Decision 3/4) —
// each as pinnedMember returns it.
func (r *Reconciler) sameKeyFunctions(ctx context.Context, key pooling.PoolKey, idx accessIndex) ([]*v1.Function, error) {
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: key.Namespace})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.sameKeyFunctions", "list functions")
	}
	out := make([]*v1.Function, 0, len(list.Items))
	for _, obj := range list.Items {
		fn, ok := obj.(*v1.Function)
		if !ok {
			continue
		}
		if k, isPooled := r.poolKeyFor(fn, idx); isPooled && k == key {
			pinned, perr := r.pinnedMember(ctx, fn)
			switch {
			case perr == nil:
				fn = pinned
			case errors.Is(perr, errRevisionMissing) || fault.KindOf(perr) == fault.Conflict:
				// no stamped member enters the manifest without its digest: it keeps the revision it serves, or is
				// left out, and its own reconcile reports it (ADR-0172 Decision 7)
				s := r.servingMember(ctx, fn)
				if s == nil {
					r.logger.Warn("pool member left out: its revision is missing or another's", "function", fn.Name, "err", perr)
					continue
				}
				fn = s
			default:
				return nil, perr
			}
			// A member whose artifact no node can run is neither ranked, counted nor materialized (ADR-0145): its
			// own reconcile reports NoMatchingPlatform, and the pool serves its peers. One whose platforms cannot be
			// listed now (a registry outage) keeps the revision it serves, or is left out: its own reconcile retries,
			// and its peers keep their pool.
			if perr := r.placeable(ctx, fn, fn.Spec.Image, fn.Spec.ImageDigest); perr != nil {
				if errors.Is(perr, scheduler.ErrNoMatchingPlatform) {
					continue
				}
				s := r.servingMember(ctx, fn)
				if s == nil || r.placeable(ctx, s, s.Spec.Image, s.Spec.ImageDigest) != nil {
					r.logger.Warn("pool member left out: its platforms cannot be listed", "function", fn.Name, "err", perr)
					continue
				}
				fn = s
			}
			out = append(out, fn)
		}
	}
	return out, nil
}

// pinnedMember is m at its current generation's Revision digest (ADR-0035), the digest the pool
// gates and materializes it at, as the solo path does: a Function deployed from a tag alone has no
// spec.imageDigest. A member whose Revision is not stamped yet (none stored, or a deleted namesake's) is returned as
// stored; a stamped member whose Revision is gone is errRevisionMissing, and another Function's Revision a Conflict
// (ADR-0172 Decision 7).
func (r *Reconciler) pinnedMember(ctx context.Context, m *v1.Function) (*v1.Function, error) {
	name := revisionName(m)
	tmpl, digest, err := r.revisionTemplate(ctx, m, v1.ObjectName(name))
	switch {
	case err == nil:
	case fault.KindOf(err) != fault.NotFound:
		return nil, err
	case m.Status.CurrentRevision == name:
		return nil, fmt.Errorf("%w: %s", errRevisionMissing, name)
	default:
		return m, nil
	}
	tmpl.Spec.ImageDigest = digest
	return tmpl, nil
}

// servingMember is m at the revision it serves, for a member whose current revision cannot be gated or materialized:
// while m serves (Ready or Degraded) a revision other than its current generation's, its pool entry stays at that
// revision, as a solo Function's serving revision keeps its calls while the current one fails (ADR-0143). nil when m
// serves no other revision or that Revision cannot be read.
func (r *Reconciler) servingMember(ctx context.Context, m *v1.Function) *v1.Function {
	s := m.Status.ServingRevision
	if !servingPhase(m.Status.Phase) || s == "" || s == revisionName(m) {
		return nil
	}
	tmpl, digest, err := r.revisionTemplate(ctx, m, v1.ObjectName(s))
	if err != nil {
		return nil
	}
	tmpl.Spec.ImageDigest = digest
	return tmpl
}

// convergePooled provisions a pooled member's shared pool worker, driven to the max desired over the key's admitted
// members (ADR-0046 Decision 6), and judges the member by its own entry in the pool host's /health/members, as a solo
// replica is judged by its readiness: ready → ready, and the serving revision follows the current one (ADR-0143
// Decision 8); loading, restarting, no entry or no answer → not ready; failed with "load timed out" → a boot crash
// loop, read again at its backoff deadline; any other failure before the current revision serves → a shape failure,
// retried after the supervision period. The pool host holds the member at its current revision only, so its failure is
// a crash under repair only while that revision is the serving one (ADR-0143 Decision 5). A member whose current
// revision serves is judged on the pool worker the resolver hands out, and one whose current revision does not serve yet
// on the worker of the current manifest, which alone holds it (ADR-0190 Decision 8). While the resolver still hands out
// an old pool worker, which serves a carried member at its serving revision until the key switches to the current
// manifest's worker (ADR-0224 Decision 6), the phase and Ready follow that worker's entry and the current revision is
// reported as booting beside it, as a solo switch reports its serving revision (ADR-0143 Decision 5, #863); those cases
// are judged even while the current worker does not run. The current worker's boot crash is reported in the solo
// vocabulary (ADR-0225 Decision 3): on Ready when the member is judged on that worker, on RevisionReady when the
// member's current revision differs from the one it serves; a Start error takes its place. The pool worker is judged on
// its own liveness (ensurePool), never on a member's state. It also returns how soon an old pool worker's drain needs
// the pass back.
func (r *Reconciler) convergePooled(ctx context.Context, fn *v1.Function, a pooling.Assignment, secretEnv, catalogEnv map[string]string, idx accessIndex) (verdict, time.Duration, error) {
	pass, err := r.ensurePool(ctx, a.Key, fn, secretEnv, catalogEnv, idx)
	if err != nil {
		return verdict{}, 0, err
	}
	// the pool serves the siblings; an asleep member counts none of it, so only a call wakes it (ADR-0193)
	if r.asleep(fn) {
		return verdict{pooled: true}, pass.drainAfter, nil
	}
	v := verdict{running: pass.running, serving: servingPhase(fn.Status.Phase), retryAt: pass.retryAt, pollAt: pass.pollAt, startErr: pass.startErr, pooled: true}
	if r.materializer == nil {
		v.ready = pass.running // legacy mode: no shim to ask (ADR-0020)
	} else {
		servesCurrent := v.serving && servingRevision(fn) == v1.ObjectName(fn.Status.CurrentRevision)
		w, handedOut := r.servingPool(a.Key, fn.Name, pass.all)
		oldOut := handedOut && !slices.ContainsFunc(pass.current, func(in runtime.Instance) bool { return in.ID == w.ID })
		crash := pass.crashLoop
		if pass.startErr != nil {
			crash = ""
		}
		judge := pass.current
		switch {
		case oldOut && servesCurrent:
			judge, v.running = []runtime.Instance{w}, 1
		case handedOut && servesCurrent:
			judge = []runtime.Instance{w}
		case oldOut && v.serving:
			_, m, ok, _ := r.memberIn(ctx, a.Key, []runtime.Instance{w}, fn.Name)
			if ok && m.State == memberReady {
				v.ready = 1
			}
			v.running, v.switching, v.booting, v.currentCrashLoop = 1, true, pass.running > 0, crash
			return v, pass.drainAfter, nil
		default:
			v.crashLoop = crash
			if v.serving && !servesCurrent {
				v.currentCrashLoop = crash
			}
		}
		if v.running == 0 {
			return v, pass.drainAfter, nil
		}
		in, m, ok, _ := r.memberIn(ctx, a.Key, judge, fn.Name)
		switch {
		case !ok:
		case m.State == memberReady && m.Dependency != nil:
			// judged by its own entry alone: its siblings and the pool host are unaffected (ADR-0215 Decision 5)
			v.report = m.Dependency
			if r.clock.Now().Sub(lastStart(in)) >= r.bootTimeout {
				v.settled = 1
			}
		case m.State == memberReady:
			v.ready = 1
		case m.State == memberFailed && m.Error == loadTimedOut:
			c := r.boot.count(runtime.NewInstanceID(fn.Namespace, fn.Name, v1.ObjectName(fn.Status.CurrentRevision), 0), in.CreatedAt,
				"the handler did not load within "+r.bootTimeout.String())
			v.crashLoop, v.retryAt = c.message, r.boot.reread(c, r.clock.Now())
		case m.State == memberFailed && !servesCurrent:
			v.shapeFailed, v.loadErr = true, m.Error
		}
	}
	if v.ready >= 1 {
		fn.Status.ServingRevision = fn.Status.CurrentRevision
	}
	return v, pass.drainAfter, nil
}

// servingPool is the pool worker among insts, key's, that the resolver hands out to member (ADR-0190 Decision 8,
// ADR-0224 Decision 4): while key's switch record waits and member is carried, the worker the record hands out if it
// listens, else the current manifest's if it listens, else none; otherwise the newest listening one. It reads the
// record and never probes. ok is false when it hands out none.
func (r *Reconciler) servingPool(key pooling.PoolKey, member v1.ObjectName, insts []runtime.Instance) (runtime.Instance, bool) {
	r.poolMu.Lock()
	d, ok := r.poolDrains[key]
	waits := ok && d.switched.IsZero() && slices.Contains(d.carried, member)
	r.poolMu.Unlock()
	if !waits {
		return newestPool(insts, r.listening)
	}
	for _, id := range []runtime.InstanceID{d.from, d.next} {
		if in, ok := newestPool(insts, func(in runtime.Instance) bool { return in.ID == id && r.listening(in) }); ok {
			return in, true
		}
	}
	return runtime.Instance{}, false
}

// readyForSwitch reports whether member entry m lets its key switch to the worker that reported it (ADR-0224 Decision
// 3a): ready, with no dependency report (ADR-0215 Decision 4).
func readyForSwitch(m memberHealth) bool {
	return m.State == memberReady && m.Dependency == nil
}

// The member states a pool host reports on /health/members.
const (
	memberReady  = "ready"
	memberFailed = "failed"
	// loadTimedOut is the error of a member whose first load ran past FUNCD_POOL_LOAD_TIMEOUT_MS.
	loadTimedOut = "load timed out"
)

// memberHealth is one entry of a pool host's GET /health/members; Dependency is the member's dependency report, absent
// when its check passes (ADR-0215 Decision 4).
type memberHealth struct {
	Name       string                  `json:"name"`
	State      string                  `json:"state"`
	Error      string                  `json:"error,omitempty"`
	Dependency *local.DependencyReport `json:"dependency,omitempty"`
}

// memberIn reads member's entry from the newest running pool worker among insts, key's pool workers; ok is false when
// there is none, it does not answer or it has no entry for member, and err is set only when it does not answer. The
// returned Instance is zero exactly when no running pool worker has a port to ask. An answer counts as that pool
// worker's liveness.
func (r *Reconciler) memberIn(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance, member v1.ObjectName) (runtime.Instance, memberHealth, bool, error) {
	if in, ok := newestPool(insts, func(in runtime.Instance) bool { return in.State == runtime.StateRunning && in.Port > 0 }); ok {
		members, ok := r.probeMembers(ctx, in.IP, in.Port)
		if !ok {
			return in, memberHealth{}, false, fault.Unavailablef("function.memberIn", "pool worker %s did not answer %s", in.ID, membersPath)
		}
		r.markLive(in.ID, r.clock.Now())
		for _, m := range members {
			if m.Name == string(member) {
				return in, m, true, nil
			}
		}
		return in, memberHealth{}, false, nil
	}
	return runtime.Instance{}, memberHealth{}, false, nil
}

// poolListened reports whether pool worker in has listened. A listened worker the runtime lists without an address is
// hung, which poolSilent judges, not a boot crash (#422); a solo replica keeps r.listening.
func poolListened(in runtime.Instance) bool { return in.Listened }

// poolSilent reports whether the running pool worker in insts that has listened is hung, which ensurePool restarts at
// once: silent on /health/liveness for livenessTimeout (ADR-0215 Decision 1). One that has not listened is
// stopUnlistenedIn's (ADR-0225 Decision 1).
func (r *Reconciler) poolSilent(ctx context.Context, insts []runtime.Instance) bool {
	if r.materializer == nil {
		return false
	}
	for _, in := range insts {
		if in.State == runtime.StateRunning && in.Listened {
			return r.probeLiveness(ctx, in)
		}
	}
	return false
}

// poolPass is what ensurePool left of a key's pool workers: the revisionPass of the worker holding the current manifest,
// that worker (current) and every worker of the key it keeps (all), how soon the pass must come back for a worker that
// drains (0 if none drains), the current worker's boot-crash message while its count is above 0 (crashLoop) and, while
// it boots again with a count, its boot deadline (pollAt, #354).
type poolPass struct {
	revisionPass
	current, all []runtime.Instance
	drainAfter   time.Duration
	crashLoop    string
	pollAt       time.Time
}

// ensurePool drives key's pool workers to their desired state (ADR-0046 Decisions 4 & 6, ADR-0190 Decision 8): it builds
// the manifest from the key's admitted members and computes the pool's desired replica as the max over those members'
// effective desired. A pool worker carries its manifest's signature in its revision slot, so a rebuild is due when no
// worker of the key holds the current signature, as the runtime lists it: the rebuild starts a second worker beside the
// old one, which serves the carried members until the key switches to the new one (beginPoolSwitch, settlePoolSwitch,
// ADR-0224) and then drains (drainPool). The worker of the current signature follows the boot rule of every Function
// (ADR-0225): one that has not listened bootTimeout after its last start is stopped, counted as a boot crash and
// created again once its growing wait has passed, in the same pass when it has; an end before listening and a failed
// Start go on the same count, and an end after listening is restarted once a period old (#603). One that listened and
// is silent on /health/liveness for livenessTimeout is restarted at once (#422). Every worker is reclaimed when desired
// is 0. The pass carries the current worker's running count, its backoff deadline, its Start error and its boot crash,
// which the pass writes to the member's status, as for a solo worker (issue #73). self is the member being reconciled:
// its resolved secretEnv and catalogEnv give the pool's shared env.
func (r *Reconciler) ensurePool(ctx context.Context, key pooling.PoolKey, self *v1.Function, secretEnv, catalogEnv map[string]string, idx accessIndex) (poolPass, error) {
	members, err := r.admittedMembers(ctx, key, idx)
	if err != nil {
		return poolPass{}, err
	}
	shared := r.poolSharedEnv(self, secretEnv, catalogEnv)
	manifest, desired, err := r.poolManifest(ctx, members, self, shared)
	if err != nil {
		return poolPass{}, err
	}
	sig := manifestSignature(manifest)
	r.holdPool(key, sig, manifest)

	poolName := poolInstanceName(key)
	insts, err := r.namedInstances(ctx, key.Namespace, poolName)
	if err != nil {
		return poolPass{}, err
	}
	cur, old := splitPool(insts, sig)
	keep := r.drainingMembers(key, old)
	if desired == 0 {
		// all members idle → reclaim the pool workers (RSS→0); next request wakes the current one.
		return poolPass{}, r.reclaimPool(ctx, key, cur, old)
	}
	now, legacy := r.clock.Now(), r.materializer == nil
	ul, err := r.stopUnlistenedIn(ctx, cur, poolListened)
	if err != nil {
		return poolPass{}, err
	}

	var pass poolPass
	started := false
	switch {
	case len(ul.stopped) > 0:
		// the current worker did not listen within bootTimeout: a boot crash, created again once its wait has passed
		if ul.retryAt.After(now) {
			pass.retryAt = ul.retryAt
			break
		}
		if pass.startErr, err = r.restartPool(ctx, key, cur, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
		started = true
	case len(cur) == 0:
		// first bring-up, or membership/artifact changed → a worker of the current manifest beside the old ones (the host
		// reads its manifest at boot only, ADR-0046 workaround)
		if pass.startErr, err = r.createPool(ctx, key, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
		if runningCount(old) > 0 {
			r.beginPoolSwitch(key, runtime.NewInstanceID(key.Namespace, poolName, v1.ObjectName(sig), 0), old, keep)
		}
		started = true
	case runningCount(cur) == 0:
		// the current worker is stopped, exited or never started, and a member now wants it up with the same manifest: a
		// failed Start or an end before listening waits out its growing wait, an end after listening a period (ADR-0225
		// Decision 3, ADR-0142, #603)
		if at, herr := r.boot.held(cur[0].ID, now); herr != nil {
			pass.retryAt, pass.startErr = at, herr
			break
		}
		_, _, _, pass.retryAt = planReplicas(map[int]runtime.Instance{0: cur[0]}, []int{0}, convergeOpts{serving: true}, now, r.supervisionPeriod, r.boot, legacy)
		if !pass.retryAt.IsZero() {
			break
		}
		if pass.startErr, err = r.restartPool(ctx, key, cur, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
		started = true
	case r.poolSilent(ctx, cur):
		// a running host that listened and stopped answering its liveness is restarted (issue #422); a member's state
		// never is
		r.logger.Warn("restarting a pool worker silent on its liveness", "namespace", key.Namespace, "pool", poolName)
		if pass.startErr, err = r.restartPool(ctx, key, cur, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
		started = true
	default:
		// up, manifest unchanged → no-op (the idempotent path; no restart).
	}

	insts, err = r.namedInstances(ctx, key.Namespace, poolName)
	if err != nil {
		return poolPass{}, err
	}
	cur, old = splitPool(insts, sig)
	pass.running = runningCount(cur)
	if started && len(cur) > 0 {
		pass.retryAt = earlier(pass.retryAt, r.boot.startResult(cur[0].ID, now, pass.startErr))
	}
	if pass.running == 0 && pass.startErr == nil && pass.retryAt.IsZero() && len(cur) > 0 {
		// a pool worker (re)started in this pass that exited at once waits out its backoff as one found exited does, so
		// a woken member is not left Idle
		_, _, _, pass.retryAt = planReplicas(map[int]runtime.Instance{0: cur[0]}, []int{0}, convergeOpts{serving: true}, now, r.supervisionPeriod, r.boot, legacy)
	}
	if len(cur) > 0 {
		pass.crashLoop, pass.pollAt = r.poolBoot(cur[0])
	}
	names := manifestNames(manifest)
	sw := r.settlePoolSwitch(ctx, key, cur, old, names)
	old, pass.drainAfter, err = r.drainPool(ctx, key, cur, old, names, sw)
	if err != nil {
		return poolPass{}, err
	}
	pass.current, pass.all = cur, append(slices.Clone(cur), old...)
	return pass, nil
}

// poolBoot reads the boot count of in, the current pool worker: once it has listened the count is reset (ADR-0225
// Decision 1); while it is above 0, its message and, while in runs and has not listened, its boot deadline.
func (r *Reconciler) poolBoot(in runtime.Instance) (crashLoop string, pollAt time.Time) {
	if in.Listened {
		r.boot.reset(in.ID)
		return "", time.Time{}
	}
	c, ok := r.boot.crash(in.ID)
	if !ok || c.count == 0 {
		return "", time.Time{}
	}
	if in.State == runtime.StateRunning {
		pollAt = lastStart(in).Add(r.bootTimeout)
	}
	return c.message, pollAt
}

// splitPool splits key's pool workers insts into those holding manifest signature sig and the others.
func splitPool(insts []runtime.Instance, sig string) (cur, old []runtime.Instance) {
	for _, in := range insts {
		if string(in.Revision) == sig {
			cur = append(cur, in)
		} else {
			old = append(old, in)
		}
	}
	return cur, old
}

// newestPool is the newest worker among insts, by CreatedAt, for which keep holds.
func newestPool(insts []runtime.Instance, keep func(runtime.Instance) bool) (runtime.Instance, bool) {
	best, ok := runtime.Instance{}, false
	for _, in := range insts {
		if keep(in) && (!ok || in.CreatedAt.After(best.CreatedAt)) {
			best, ok = in, true
		}
	}
	return best, ok
}

// drainingMembers is the member set the old workers of key may still serve calls for: the one last recorded, while one of
// them runs. A worker created beside them gets the union on the pool's local API (ADR-0190 Decision 8).
func (r *Reconciler) drainingMembers(key pooling.PoolKey, old []runtime.Instance) []v1.ObjectName {
	if runningCount(old) == 0 {
		return nil
	}
	members, _, _ := r.PoolMembers(key.Namespace, poolInstanceName(key))
	return members
}

// drainPool retires the old workers of key, those not holding the current manifest (ADR-0190 Decision 8, ADR-0224
// Decision 5), by sw, settlePoolSwitch's record: at once one that does not run, since no call can complete on it, and
// one other than sw.from that never listened, since none was handed out; the others are kept while the newest current
// worker does not listen, and sw.from also until the switch. Then one is retired once no call to it is in flight nor
// handed out within HandOutSettle, or DrainGrace after its clock started: the switch for sw.from, the first listen of
// the current worker for the others. A retired worker's boot count goes with it (retire, ADR-0225). While one drains,
// the pool's local API keeps the members of both; once none is left, its member set is the current manifest's, names.
// It returns the old workers left and how soon the pass must come back for them (0 if none is left): while the key
// waits on a listening current worker, by the end of its load clock.
func (r *Reconciler) drainPool(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance, names []v1.ObjectName, sw poolDrain) ([]runtime.Instance, time.Duration, error) {
	const op = "function.drainPool"
	if len(old) == 0 {
		return nil, 0, nil
	}
	poolName := poolInstanceName(key)
	_, listens := newestPool(cur, r.listening)
	now, waiting := r.clock.Now(), sw.next != "" && sw.switched.IsZero()
	wait := r.drainPoll
	if waiting && listens {
		wait = min(wait, max(r.bootTimeout-now.Sub(sw.since), time.Millisecond))
	}
	var left []runtime.Instance
	for _, in := range old {
		keep := false
		switch {
		case in.State != runtime.StateRunning:
		case in.ID != sw.from && r.materializer != nil && !in.Listened:
		case !listens || (in.ID == sw.from && waiting):
			keep = true
		default:
			start := sw.since
			if in.ID == sw.from {
				start = sw.switched
			}
			elapsed := now.Sub(start)
			keep = elapsed < r.drainGrace && r.calls != nil && !r.calls.Idle(instanceURL(key.Namespace, poolName, in), r.handOutSettle)
			if keep {
				wait = min(wait, max(r.drainGrace-elapsed, time.Millisecond))
			}
		}
		if keep {
			left = append(left, in)
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return nil, 0, err
		}
	}
	if len(left) > 0 {
		return left, wait, nil
	}
	r.endPoolDrain(key)
	r.setPoolMembers(key.Namespace, poolName, names)
	if r.invokeSockets != nil {
		if _, err := r.invokeSockets.PoolSocketFor(key.Namespace, poolName, names); err != nil {
			return nil, 0, fault.Wrapf(err, fault.KindOf(err), op, "narrow the pool's local API socket")
		}
	}
	return nil, 0, nil
}

// manifestNames is the member names of manifest.
func manifestNames(manifest []poolManifestEntry) []v1.ObjectName {
	names := make([]v1.ObjectName, 0, len(manifest))
	for _, m := range manifest {
		names = append(names, v1.ObjectName(m.Name))
	}
	return names
}

// reclaimPool stops key's current pool workers cur, forgetting their boot counts as a solo scale-down does (ADR-0225
// Decision 3), retires its old ones and ends their drain: ensurePool's desired == 0 branch, shared with releasePool.
func (r *Reconciler) reclaimPool(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance) error {
	for _, in := range cur {
		r.boot.reset(in.ID)
	}
	if runningCount(cur) > 0 {
		if err := r.stopPool(ctx, cur); err != nil {
			return err
		}
	}
	for _, in := range old {
		if err := r.retire(ctx, in); err != nil {
			return err
		}
	}
	r.endPoolDrain(key)
	return nil
}

// releasePool stops fn's pool workers when no admitted member of its key wants one: the max of desiredReplicas over
// admittedMembers is 0 (ADR-0046 Decision 6, ADR-0193). A solo Function stops nothing. The members are the stored
// records, so the gated member counts as its read phase. The current workers are those of the manifest last built for
// the key; before any was built, every worker counts as current, so none is removed.
func (r *Reconciler) releasePool(ctx context.Context, fn *v1.Function, idx accessIndex) error {
	key, ok := r.poolKeyFor(fn, idx)
	if !ok {
		return nil
	}
	members, err := r.admittedMembers(ctx, key, idx)
	if err != nil {
		return err
	}
	for _, m := range members {
		if r.desiredReplicas(m) > 0 {
			return nil
		}
	}
	insts, err := r.namedInstances(ctx, key.Namespace, poolInstanceName(key))
	if err != nil {
		return err
	}
	r.poolMu.Lock()
	hold, held := r.poolHolds[key]
	r.poolMu.Unlock()
	cur, old := insts, []runtime.Instance(nil)
	if held {
		cur, old = splitPool(insts, hold.sig)
	}
	return r.reclaimPool(ctx, key, cur, old)
}

// admittedMembers returns the key's admitted members (the first PoolLimit by name), the set
// the manifest and the pool's desired replica are computed over (rejected members excluded).
func (r *Reconciler) admittedMembers(ctx context.Context, key pooling.PoolKey, idx accessIndex) ([]*v1.Function, error) {
	all, err := r.rankedMembers(ctx, key, idx)
	if err != nil {
		return nil, err
	}
	return all[:min(len(all), r.poolLimit)], nil
}

// rankedMembers returns every function of key in admission order (by name, ADR-0046 Decision 3).
func (r *Reconciler) rankedMembers(ctx context.Context, key pooling.PoolKey, idx accessIndex) ([]*v1.Function, error) {
	all, err := r.sameKeyFunctions(ctx, key, idx)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

// poolManifest materializes each admitted member's artifact and builds the pool manifest plus
// the pool's desired replica = max over members' effective desired (ADR-0046 Decision 6), so a
// warm/woken member keeps the pool up for idle siblings and reclaim fires only when all are idle.
// A member whose artifact cannot be materialized fails alone (ADR-0046 bounded blast radius): it
// keeps the revision it serves (servingMember) or is left out of the manifest, and only self's own
// failure is returned. Each row carries the env only that member gets (memberEnv); it resolves nothing.
func (r *Reconciler) poolManifest(ctx context.Context, members []*v1.Function, self *v1.Function, shared map[string]string) ([]poolManifestEntry, int, error) {
	const op = "function.poolManifest"
	manifest := make([]poolManifestEntry, 0, len(members))
	desired := 0
	for _, m := range members {
		path := ""
		contractPath := ""
		if r.materializer != nil {
			p, err := r.materializer.Materialize(ctx, m)
			if err != nil && m.Name != self.Name {
				if s := r.servingMember(ctx, m); s != nil {
					if sp, serr := r.materializer.Materialize(ctx, s); serr == nil {
						m, p, err = s, sp, nil
					}
				}
			}
			if err != nil {
				err = fault.Wrapf(err, fault.KindOf(err), op, "materialize %s/%s", m.Namespace, m.Name)
				if m.Name == self.Name {
					return nil, 0, err
				}
				r.logger.Warn("pool member left out: its artifact cannot be materialized", "function", m.Name, "err", err)
				continue
			}
			path = p
			// ADR-0123: the pool host runs in process mode (it reads the host artifact paths
			// directly), so the delivered contract path is the host path in the bundle root.
			if name, ok := contractFileIn(filepath.Dir(p)); ok {
				contractPath = filepath.Join(filepath.Dir(p), name)
			}
		}
		env, err := r.memberEnv(m, path, shared)
		if err != nil {
			if m.Name == self.Name {
				return nil, 0, fault.Wrapf(err, fault.KindOf(err), op, "build the env of %s/%s", m.Namespace, m.Name)
			}
			r.logger.Warn("pool member left out: its env cannot be built", "function", m.Name, "err", err)
			continue
		}
		if d := r.desiredReplicas(m); d > desired {
			desired = d
		}
		manifest = append(manifest, poolManifestEntry{
			Name: string(m.Name), Artifact: path, Handler: m.Spec.Handler, Contract: contractPath, Env: env,
			code: memberCode{image: m.Spec.Image, digest: m.Spec.ImageDigest, handler: m.Spec.Handler},
		})
	}
	return manifest, desired, nil
}

// memberEnv is the env only member m gets, set in its manifest row: its S3 keypair when it binds spec.blob, its
// catalog token per alias, and its bundle dir. Both credentials derive from the name, so a row is stable across
// passes. A key the shared env sets is omitted, so the shared env wins as a solo worker's later env does.
func (r *Reconciler) memberEnv(m *v1.Function, artifact string, shared map[string]string) (map[string]string, error) {
	env := map[string]string{}
	if artifact != "" {
		env["FUNCD_BUNDLE_DIR"] = filepath.Dir(artifact)
	}
	if r.s3Gateway.Enabled && r.s3Gateway.Derive != nil && len(m.Spec.Blob) > 0 {
		env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"] = r.s3Gateway.Derive(v1.KindFunction, string(m.Namespace), string(m.Name))
	}
	if len(m.Spec.Catalogs) > 0 {
		token, err := cataloggw.DeriveCatalogToken(r.catalogMaster, m.Namespace, m.Name)
		if err != nil {
			return nil, err
		}
		for _, c := range m.Spec.Catalogs {
			env["FUNCD_CATALOG_"+strings.ToUpper(c.Alias)+"_TOKEN"] = token
		}
	}
	for k := range shared {
		delete(env, k)
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

// poolSharedEnv is the env every member of a pool shares, taken from self, the member being reconciled — members of
// one key share their access, so their bindings resolve alike: its config and secrets, its catalog URLs, the S3
// region and endpoint, and the DuckDB extension dir. Never from a manifest member, which may serve an older revision.
func (r *Reconciler) poolSharedEnv(self *v1.Function, secretEnv, catalogEnv map[string]string) map[string]string {
	env := map[string]string{}
	s3 := map[string]string{}
	r.addS3Env(s3, self)
	for _, k := range []string{"AWS_REGION", "AWS_ENDPOINT_URL_S3"} {
		if v, ok := s3[k]; ok {
			env[k] = v
		}
	}
	for k, v := range catalogEnv {
		if strings.HasSuffix(k, "_URL") {
			env[k] = v
		}
	}
	r.addCatalogExtensionDir(env, self)
	r.addRecordBound(env)
	r.mergeSecretEnv(env, secretEnv)
	return env
}

// createPool creates+starts the pool worker for key holding manifest, of signature sig, which names it in its revision
// slot (ADR-0190 Decision 8). The pool host reads its manifest from the FILE named by FUNCD_POOL_MANIFEST
// (writePoolManifest), dials the pool's local API socket (FUNCD_INVOKE_SOCKET), on which each call names its member, and
// bounds each member's load by FUNCD_POOL_LOAD_TIMEOUT_MS. The member set, with keep, the members of the old workers
// that still drain, is recorded before Create, so the capture hook keeps the first start's records. The process driver
// injects FUNCD_PORTFILE and the pool host writes its bound port back (ADR-0030 handshake). It returns the Start error
// apart (startPoolInstance).
func (r *Reconciler) createPool(ctx context.Context, key pooling.PoolKey, sig string, manifest []poolManifestEntry, shared map[string]string, keep []v1.ObjectName) (startErr, err error) {
	const op = "function.createPool"
	manifestPath, err := writePoolManifest(r.poolManifestDir, key, manifest)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "write pool manifest")
	}
	poolName := poolInstanceName(key)
	names := manifestNames(manifest)
	for _, k := range keep {
		if !slices.Contains(names, k) {
			names = append(names, k)
		}
	}
	r.setPoolMembers(key.Namespace, poolName, names)
	env := maps.Clone(shared)
	env["FUNCD_POOL_MANIFEST"] = manifestPath
	env["FUNCD_POOL_LOAD_TIMEOUT_MS"] = strconv.FormatInt(max(r.bootTimeout.Milliseconds(), 1), 10)
	if r.invokeSockets != nil {
		sock, serr := r.invokeSockets.PoolSocketFor(key.Namespace, poolName, names)
		if serr != nil {
			return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "provision the pool's local API socket")
		}
		env["FUNCD_INVOKE_SOCKET"] = sock
	}
	if _, perr := r.scheduler.Schedule(ctx, scheduler.Request{Namespace: key.Namespace, Name: poolName, Replica: 0}); perr != nil {
		return nil, fault.Wrapf(perr, fault.KindOf(perr), op, "schedule pool worker")
	}
	spec := runtime.WorkerSpec{
		Namespace: key.Namespace,
		Name:      poolName,
		OwnerKind: v1.KindFunction,
		Revision:  v1.ObjectName(sig),
		Replica:   0,
		Members:   len(manifest),
		Image:     key.Runtime,
		Command:   r.poolHostFor(v1.RuntimeName(key.Runtime)), // node pool.mjs, or python pool.py (ADR-0050)
		Env:       env,
	}
	inst, cerr := r.runtime.Create(ctx, spec)
	if cerr != nil {
		return nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "create pool worker")
	}
	return r.startPoolInstance(ctx, []runtime.Instance{inst}), nil
}

// restartPool stops the pool worker insts of the current manifest and creates it again under the same instance id from
// the rewritten manifest, so the pool host re-reads the manifest at boot and its CreatedAt is its boot time (ADR-0030
// §4b). The restart is observable as a new PID. It returns the Start error apart (startPoolInstance).
func (r *Reconciler) restartPool(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance, sig string, manifest []poolManifestEntry, shared map[string]string, keep []v1.ObjectName) (startErr, err error) {
	if err := r.stopPool(ctx, insts); err != nil {
		return nil, err
	}
	return r.createPool(ctx, key, sig, manifest, shared, keep)
}

// startPoolInstance starts the pool worker instance(s) createPool just created, the second half of every bring-up,
// rebuild and restart. Its error is a Start error, which the pass counts (ADR-0225) and writes to the member's status
// instead of failing before it (issue #359).
func (r *Reconciler) startPoolInstance(ctx context.Context, insts []runtime.Instance) error {
	const op = "function.startPoolInstance"
	for _, in := range insts {
		if serr := r.runtime.Start(ctx, in.ID); serr != nil {
			r.logger.Warn("could not start pool worker", "instance", in.ID, "err", serr)
			return fault.Wrapf(serr, fault.KindOf(serr), op, "start pool worker")
		}
	}
	return nil
}

// writePoolManifest writes the manifest to the pool's file in dir and returns its path. The rows carry member
// credentials, so the file is 0600, written through an O_CREATE|O_EXCL temp file renamed into place, in a 0700 dir
// funcd owns: a dir that is a symlink, another user's, or open to others is refused. One file per pool (namespace
// and key), so leftovers never accumulate; reclaimOrphanPools deletes it.
func writePoolManifest(dir string, key pooling.PoolKey, manifest []poolManifestEntry) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	path := poolManifestPath(dir, key.Namespace, poolInstanceName(key))
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", werr
	}
	return path, nil
}

// poolManifestPath is the manifest file of pool worker `pool` of ns in dir.
func poolManifestPath(dir string, ns v1.NamespaceName, pool v1.ObjectName) string {
	return filepath.Join(dir, string(ns)+"."+string(pool)+".json")
}

// ensurePrivateDir creates dir 0700 when absent, and otherwise refuses one that is a symlink, not a directory, not
// this process's user's, or open to group or others.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fault.Invalidf("function.ensurePrivateDir", "pool manifest dir %q is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o700 || !ownedByMe(fi) {
		return fault.Invalidf("function.ensurePrivateDir", "pool manifest dir %q must be mode 0700 and owned by funcd", dir)
	}
	return nil
}

// stopPool stops every running/created instance of the pool worker. Idempotent.
func (r *Reconciler) stopPool(ctx context.Context, insts []runtime.Instance) error {
	const op = "function.stopPool"
	for _, in := range insts {
		if serr := r.runtime.Stop(ctx, in.ID); serr != nil {
			return fault.Wrapf(serr, fault.KindOf(serr), op, "stop pool worker")
		}
	}
	return nil
}

// reclaimOrphanPools stops and removes each pool worker in ns whose key no Function declares any more: its last member
// was deleted or moved to another key, by a spec change or by an access change. Only a member's own reconcile drives
// its pool (ADR-0046 Decision 6), so nothing else would reclaim it; its local API socket and manifest go with it. The
// workers are listed before the Functions, so a pool created in between has its member listed. A List error reclaims
// nothing.
func (r *Reconciler) reclaimOrphanPools(ctx context.Context, ns v1.NamespaceName) error {
	const op = "function.reclaimOrphanPools"
	insts, err := r.runtime.List(ctx, ns)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list workers")
	}
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list functions")
	}
	idx, err := r.accessIn(ctx, ns)
	if err != nil {
		return err
	}
	declared := map[v1.ObjectName]bool{}
	for _, obj := range list.Items {
		if fn, ok := obj.(*v1.Function); ok {
			if key, pooled := r.poolKeyFor(fn, idx); pooled {
				declared[poolInstanceName(key)] = true
			}
		}
	}
	for _, in := range insts {
		if in.OwnerKind != v1.KindFunction || declared[in.Name] || !strings.HasPrefix(string(in.Name), poolInstancePrefix) {
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return err
		}
		r.forgetPool(ns, in.Name)
		if r.invokeSockets != nil {
			r.invokeSockets.Remove(ns, in.Name)
		}
		_ = os.Remove(poolManifestPath(r.poolManifestDir, ns, in.Name))
	}
	return nil
}

// poolInstancePrefix starts every pool worker's name (poolInstanceName).
const poolInstancePrefix = "__pool__"

// poolInstanceName is the synthetic worker name for a pool key. It encodes runtime, worker id
// and access so distinct keys never collide, and is prefixed so it can never equal a real function
// name (a DNS-1123 label cannot contain "__"), keeping a member's solo lookups separate.
func poolInstanceName(key pooling.PoolKey) v1.ObjectName {
	return v1.ObjectName(poolInstancePrefix + key.Runtime + "__" + key.Worker + "__" + key.AccessHash)
}

// manifestSignature is a stable digest of the pool manifest, which names the pool worker holding it (ADR-0190 Decision
// 8): the pool is rebuilt only when no worker holds it (a member joined/left or an artifact path changed). Its 128 bits
// keep a worker's runtime names short. Order-independent because the manifest is already name-sorted by the caller.
func manifestSignature(manifest []poolManifestEntry) string {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "" // marshal of a []struct of strings cannot fail; "" forces a rebuild if it ever did
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// runningCount counts instances in StateRunning.
func runningCount(insts []runtime.Instance) int {
	n := 0
	for _, in := range insts {
		if in.State == runtime.StateRunning {
			n++
		}
	}
	return n
}

// beginPoolSwitch makes key's switch record when ensurePool created next, the current manifest's worker, beside a
// running old one (ADR-0224 Decision 2): it hands out the worker the record being replaced hands out until it switched,
// that record's next once it switched, else the newest listening one of old; and it carries that record's members until
// it switched, else keep, the pool's member set before next was created.
func (r *Reconciler) beginPoolSwitch(key pooling.PoolKey, next runtime.InstanceID, old []runtime.Instance, keep []v1.ObjectName) {
	d := poolDrain{next: next, carried: slices.Clone(keep)}
	if f, ok := newestPool(old, r.listening); ok {
		d.from = f.ID
	}
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	switch prev, ok := r.poolDrains[key]; {
	case ok && prev.switched.IsZero():
		d.from, d.carried = prev.from, prev.carried
	case ok:
		d.from = prev.next
	}
	r.poolDrains[key] = d
}

// settlePoolSwitch is the switch decision of key's rebuild (ADR-0224 Decisions 2-3), over cur and old, key's workers of
// the current manifest and the others, and names, the current manifest's members. With no record for the current
// worker beside old ones, as after a restart of funcd, it makes one, switched when that worker listens. It starts the
// load clock when the current worker is first seen listening, and again when it was created again before the switch.
// While the record waits and the current worker listens, it switches once the worker the record hands out no longer
// listens, runtime.bootTimeout has passed on the load clock, or every carried member in names reads ready on the
// current worker (one /health/members read; no answer is not ready). It returns a copy of the record, zero if none.
func (r *Reconciler) settlePoolSwitch(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance, names []v1.ObjectName) poolDrain {
	next, ok := newestPool(cur, func(runtime.Instance) bool { return true })
	keep := r.drainingMembers(key, old)
	now, listens := r.clock.Now(), ok && r.listening(next)
	r.poolMu.Lock()
	d, held := r.poolDrains[key]
	switch {
	case !ok || (held && d.next == next.ID):
	case len(old) == 0:
		delete(r.poolDrains, key)
		r.poolMu.Unlock()
		return poolDrain{}
	default:
		d, held = poolDrain{next: next.ID, carried: keep}, true
		if f, fok := newestPool(old, r.listening); fok {
			d.from = f.ID
		}
		if listens {
			d.switched = now
		}
	}
	if !held {
		r.poolMu.Unlock()
		return poolDrain{}
	}
	if d.switched.IsZero() && next.CreatedAt.After(d.nextCreated) {
		d.since, d.nextCreated = time.Time{}, next.CreatedAt
	}
	if listens && d.since.IsZero() {
		d.since = now
	}
	r.poolDrains[key] = d
	d.carried = slices.Clone(d.carried)
	r.poolMu.Unlock()
	if !d.switched.IsZero() || !listens || !r.switchDue(ctx, d, next, old, names, now) {
		return d
	}
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	if c, ok := r.poolDrains[key]; ok && c.next == d.next && c.nextCreated.Equal(d.nextCreated) && c.switched.IsZero() {
		c.switched = now
		r.poolDrains[key] = c
	}
	d = r.poolDrains[key]
	d.carried = slices.Clone(d.carried)
	return d
}

// switchDue reports whether record d, waiting on next, which listens, switches at now (ADR-0224 Decision 3): (c) its
// from does not listen among old, (b) runtime.bootTimeout has passed since d.since, or (a) every member of d.carried in
// names reads readyForSwitch on next. With no shim to ask (legacy mode), (a) holds.
func (r *Reconciler) switchDue(ctx context.Context, d poolDrain, next runtime.Instance, old []runtime.Instance, names []v1.ObjectName, now time.Time) bool {
	if _, ok := newestPool(old, func(in runtime.Instance) bool { return in.ID == d.from && r.listening(in) }); !ok {
		return true
	}
	if now.Sub(d.since) >= r.bootTimeout {
		return true
	}
	var awaited []v1.ObjectName
	for _, m := range d.carried {
		if slices.Contains(names, m) {
			awaited = append(awaited, m)
		}
	}
	if len(awaited) == 0 || r.materializer == nil {
		return true
	}
	members, ok := r.probeMembers(ctx, next.IP, next.Port)
	if !ok {
		return false
	}
	for _, name := range awaited {
		i := slices.IndexFunc(members, func(m memberHealth) bool { return m.Name == string(name) })
		if i < 0 || !readyForSwitch(members[i]) {
			return false
		}
	}
	return true
}

// holdPool records manifest, of signature sig, as the one key's pool workers are built to hold.
func (r *Reconciler) holdPool(key pooling.PoolKey, sig string, manifest []poolManifestEntry) {
	codes := make(map[v1.ObjectName]memberCode, len(manifest))
	for _, m := range manifest {
		codes[v1.ObjectName(m.Name)] = m.code
	}
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	r.poolHolds[key] = poolHold{sig: sig, codes: codes}
}

// endPoolDrain forgets the drain of key's old pool workers.
func (r *Reconciler) endPoolDrain(key pooling.PoolKey) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	delete(r.poolDrains, key)
}

// forgetPool drops all r keeps for the reclaimed pool worker named name in ns: its drain, liveness, boot counts,
// manifest and member set.
func (r *Reconciler) forgetPool(ns v1.NamespaceName, name v1.ObjectName) {
	r.boot.forget(backoffPrefix(ns, name))
	ofPool := func(key pooling.PoolKey) bool { return key.Namespace == ns && poolInstanceName(key) == name }
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	for key := range r.poolDrains {
		if ofPool(key) {
			delete(r.poolDrains, key)
		}
	}
	r.forgetLive(ns, name)
	for key := range r.poolHolds {
		if ofPool(key) {
			delete(r.poolHolds, key)
		}
	}
	delete(r.poolSets, poolSetKey(ns, name))
}

// ownedByMe reports whether fi belongs to this process's user.
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// preparePoolManifestDir readies the dir pool manifests are written to: dir, checked and emptied of an earlier run's
// manifests, or, when dir is empty, a private temp dir this reconciler owns (own) and Close removes.
func preparePoolManifestDir(dir string) (path string, own bool, err error) {
	const op = "function.preparePoolManifestDir"
	if dir == "" {
		tmp, terr := os.MkdirTemp("", "funcd-pool")
		if terr != nil {
			return "", false, fault.Wrapf(terr, fault.Internal, op, "create the pool manifest dir")
		}
		return tmp, true, nil
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", false, fault.Wrapf(err, fault.KindOf(err), op, "prepare the pool manifest dir")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, fault.Wrapf(err, fault.Internal, op, "read the pool manifest dir")
	}
	for _, e := range entries {
		if rerr := os.RemoveAll(filepath.Join(dir, e.Name())); rerr != nil {
			return "", false, fault.Wrapf(rerr, fault.Internal, op, "empty the pool manifest dir")
		}
	}
	return dir, false, nil
}

// Close removes the pool manifest dir NewReconciler created; a configured one is left in place.
func (r *Reconciler) Close() error {
	if !r.ownManifestDir {
		return nil
	}
	return os.RemoveAll(r.poolManifestDir)
}
