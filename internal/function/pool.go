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
// on the worker of the current manifest, which alone holds it (ADR-0190 Decision 8). The pool worker is judged on its
// own liveness (ensurePool), never on a member's state. It also returns how soon an old pool worker's drain needs the
// pass back.
func (r *Reconciler) convergePooled(ctx context.Context, fn *v1.Function, a pooling.Assignment, secretEnv, catalogEnv map[string]string, idx accessIndex) (verdict, time.Duration, error) {
	pass, err := r.ensurePool(ctx, a.Key, fn, secretEnv, catalogEnv, idx)
	if err != nil {
		return verdict{}, 0, err
	}
	v := verdict{running: pass.running, serving: servingPhase(fn.Status.Phase), retryAt: pass.retryAt, startErr: pass.startErr, pooled: true}
	if pass.running == 0 {
		return v, pass.drainAfter, nil
	}
	if r.materializer == nil {
		v.ready = pass.running // legacy mode: no shim to ask (ADR-0020)
	} else {
		servesCurrent := v.serving && servingRevision(fn) == v1.ObjectName(fn.Status.CurrentRevision)
		judge := pass.current
		if w, ok := r.servingPool(pass.all); ok && servesCurrent {
			judge = []runtime.Instance{w}
		}
		in, m, ok := r.memberIn(ctx, a.Key, judge, fn.Name)
		switch {
		case !ok:
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

// servingPool is the pool worker among insts, a key's, that the resolver hands out (ADR-0190 Decision 8): the newest
// listening one, so an old one serves until a new one listens. ok is false when none listens.
func (r *Reconciler) servingPool(insts []runtime.Instance) (runtime.Instance, bool) {
	return newestPool(insts, r.listening)
}

// The member states a pool host reports on /health/members.
const (
	memberReady  = "ready"
	memberFailed = "failed"
	// loadTimedOut is the error of a member whose first load ran past FUNCD_POOL_LOAD_TIMEOUT_MS.
	loadTimedOut = "load timed out"
)

// memberHealth is one entry of a pool host's GET /health/members.
type memberHealth struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// memberIn reads member's entry from the newest running pool worker among insts, key's pool workers; ok is false when
// none answers or it has no entry for member. An answer counts as that pool worker's liveness.
func (r *Reconciler) memberIn(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance, member v1.ObjectName) (runtime.Instance, memberHealth, bool) {
	if in, ok := newestPool(insts, func(in runtime.Instance) bool { return in.State == runtime.StateRunning && in.Port > 0 }); ok {
		members, ok := r.probeMembers(ctx, in.IP, in.Port)
		if !ok {
			return in, memberHealth{}, false
		}
		r.markPoolLive(key, in.ID, r.clock.Now())
		for _, m := range members {
			if m.Name == string(member) {
				return in, m, true
			}
		}
		return in, memberHealth{}, false
	}
	return runtime.Instance{}, memberHealth{}, false
}

// poolSilent reports whether the running pool worker in insts has not answered /health/liveness for bootTimeout since
// its last answer, else since it was created: a hung host, which ensurePool restarts.
func (r *Reconciler) poolSilent(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance) bool {
	if r.materializer == nil {
		return false
	}
	for _, in := range insts {
		if in.State != runtime.StateRunning {
			continue
		}
		if in.Port > 0 && r.probeReady(ctx, in.IP, in.Port, livenessPath) {
			r.markPoolLive(key, in.ID, r.clock.Now())
			return false
		}
		last := r.poolLastLive(key, in.ID)
		if last.Before(in.CreatedAt) {
			last = in.CreatedAt
		}
		return r.clock.Now().Sub(last) >= r.bootTimeout
	}
	return false
}

// poolPass is what ensurePool left of a key's pool workers: the revisionPass of the worker holding the current manifest,
// that worker (current) and every worker of the key it keeps (all), and how soon the pass must come back for a worker
// that drains (0 if none drains).
type poolPass struct {
	revisionPass
	current, all []runtime.Instance
	drainAfter   time.Duration
}

// ensurePool drives key's pool workers to their desired state (ADR-0046 Decisions 4 & 6, ADR-0190 Decision 8): it builds
// the manifest from the key's admitted members and computes the pool's desired replica as the max over those members'
// effective desired. A pool worker carries its manifest's signature in its revision slot, so a rebuild is due when no
// worker of the key holds the current signature, as the runtime lists it: the rebuild starts a second worker beside the
// old one, which serves until the new one listens and then drains (drainPool). The worker of the current signature is
// restarted (stop and create) when it is stopped or exited, or silent on /health/liveness for bootTimeout, since no call
// can complete on it; every worker is reclaimed when desired is 0. The pass carries the current worker's running count,
// its backoff deadline and its Start error, which the pass writes to the member's status, as for a solo worker (issue
// #73). self is the member being reconciled: its resolved secretEnv and catalogEnv give the pool's shared env.
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

	var pass poolPass
	switch {
	case desired == 0:
		// all members idle → reclaim the pool workers (RSS→0); next request wakes the current one.
		if runningCount(cur) > 0 {
			if err := r.stopPool(ctx, cur); err != nil {
				return poolPass{}, err
			}
		}
		for _, in := range old {
			if err := r.retire(ctx, in); err != nil {
				return poolPass{}, err
			}
		}
		r.endPoolDrain(key)
		return poolPass{}, nil
	case len(cur) == 0:
		// first bring-up, or membership/artifact changed → a worker of the current manifest beside the old ones (the host
		// reads its manifest at boot only, ADR-0046 workaround)
		if pass.startErr, err = r.createPool(ctx, key, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
	case runningCount(cur) == 0:
		// the current worker is stopped or exited, and a member now wants it up with the same manifest → restart it by
		// ADR-0142's per-replica table as a crash under repair, so one that exited is created again only once it is a
		// period old: a pool host that cannot boot is retried once per period, as a solo replica is (issue #70, #603).
		_, _, _, pass.retryAt = planReplicas(map[int]runtime.Instance{0: cur[0]}, []int{0}, convergeOpts{serving: true}, r.clock.Now(), r.supervisionPeriod, nil, false)
		if !pass.retryAt.IsZero() {
			break
		}
		if pass.startErr, err = r.restartPool(ctx, key, cur, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
	case r.poolSilent(ctx, key, cur):
		// a running host that stopped answering its liveness is restarted (issue #422); a member's state never is
		r.logger.Warn("restarting a pool worker silent on its liveness", "namespace", key.Namespace, "pool", poolName)
		if pass.startErr, err = r.restartPool(ctx, key, cur, sig, manifest, shared, keep); err != nil {
			return poolPass{}, err
		}
	default:
		// up, manifest unchanged → no-op (the idempotent path; no restart).
	}

	insts, err = r.namedInstances(ctx, key.Namespace, poolName)
	if err != nil {
		return poolPass{}, err
	}
	cur, old = splitPool(insts, sig)
	pass.running = runningCount(cur)
	if pass.running == 0 && pass.startErr == nil && pass.retryAt.IsZero() && len(cur) > 0 {
		// a pool worker (re)started in this pass that exited at once waits out its backoff as one found exited does, so
		// a woken member is not left Idle
		_, _, _, pass.retryAt = planReplicas(map[int]runtime.Instance{0: cur[0]}, []int{0}, convergeOpts{serving: true}, r.clock.Now(), r.supervisionPeriod, nil, false)
	}
	old, pass.drainAfter, err = r.drainPool(ctx, key, cur, old, manifestNames(manifest))
	if err != nil {
		return poolPass{}, err
	}
	pass.current, pass.all = cur, append(slices.Clone(cur), old...)
	return pass, nil
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

// drainPool retires the old workers of key, those not holding the current manifest (ADR-0190 Decision 8): one that does
// not run at once, since no call can complete on it; a running one only once the newest current worker listens, as the
// resolver then hands that one out, and then once no call to it is in flight nor handed out within HandOutSettle, or
// DrainGrace after that worker was first seen listening. While one drains, the pool's local API keeps the members of
// both; once none is left, its member set is the current manifest's, names. It returns the old workers left and how
// soon the pass must come back for them (0 if none is left).
func (r *Reconciler) drainPool(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance, names []v1.ObjectName) ([]runtime.Instance, time.Duration, error) {
	const op = "function.drainPool"
	if len(old) == 0 {
		return nil, 0, nil
	}
	poolName := poolInstanceName(key)
	next, listens := newestPool(cur, r.listening)
	var elapsed time.Duration
	if listens {
		elapsed = r.clock.Now().Sub(r.poolDrainSince(key, next.ID))
	}
	var left []runtime.Instance
	for _, in := range old {
		if in.State == runtime.StateRunning && (!listens || (elapsed < r.drainGrace &&
			r.calls != nil && !r.calls.Idle(instanceURL(key.Namespace, poolName, in), r.handOutSettle))) {
			left = append(left, in)
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return nil, 0, err
		}
	}
	switch {
	case len(left) > 0 && !listens:
		return left, r.drainPoll, nil
	case len(left) > 0:
		return left, min(r.drainPoll, max(r.drainGrace-elapsed, time.Millisecond)), nil
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

// admittedMembers returns the key's admitted members (the first PoolLimit by name), the set
// the manifest and the pool's desired replica are computed over (rejected members excluded).
func (r *Reconciler) admittedMembers(ctx context.Context, key pooling.PoolKey, idx accessIndex) ([]*v1.Function, error) {
	all, err := r.sameKeyFunctions(ctx, key, idx)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if len(all) > r.poolLimit {
		all = all[:r.poolLimit]
	}
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

// startPoolInstance (re)starts the existing pool worker instance(s) — used both to wake a
// reclaimed pool back up and as the second half of a rebuild. The process driver re-execs the
// command, so the pool host re-reads its (possibly rewritten) manifest file. Its error is a Start error, which the pass
// writes to the member's status instead of failing before it (issue #359).
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

// poolDrainSince is when the drain of key's old pool workers started: when worker `next` of the current manifest was
// first seen listening, which a restart of funcd sets again (ADR-0190 Decision 8). Guarded by poolMu.
func (r *Reconciler) poolDrainSince(key pooling.PoolKey, next runtime.InstanceID) time.Time {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	d, ok := r.poolDrains[key]
	if !ok || d.next != next {
		d = poolDrain{next: next, since: r.clock.Now()}
		r.poolDrains[key] = d
	}
	return d.since
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

// forgetPool drops all r keeps for the reclaimed pool worker named name in ns: its drain, liveness, manifest and member
// set.
func (r *Reconciler) forgetPool(ns v1.NamespaceName, name v1.ObjectName) {
	ofPool := func(key pooling.PoolKey) bool { return key.Namespace == ns && poolInstanceName(key) == name }
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	for key := range r.poolDrains {
		if ofPool(key) {
			delete(r.poolDrains, key)
		}
	}
	for key := range r.poolLive {
		if ofPool(key) {
			delete(r.poolLive, key)
		}
	}
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
