package function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
}

// poolHostFor selects the pool-host launch prefix for a runtime (ADR-0050): the longest registered
// pool-family prefix that matches rt (e.g. "python" → pool.py). Failing that, the default
// poolShimCommand (the Node pool.mjs) applies — but ONLY to a node-family runtime, i.e. one not
// served by a registered non-default *runtime* shim (shimByFamily). That guard keeps a python*
// runtime (which has a python runtime shim) out of the Node pool host when it has no pool host of
// its own — it stays solo rather than running a Python artifact in a Node host. nil ⇒ no host ⇒ solo.
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
	for family := range r.shimByFamily {
		if strings.HasPrefix(string(rt), family) {
			return nil // non-node runtime with no pool host of its own → solo
		}
	}
	return r.poolShimCommand // the node default applies to node-family runtimes only
}

// poolKeyFor returns fn's pool key IFF it is eligible to co-pool: it opted in (spec.pooling.worker)
// AND a pool host is configured for its runtime family (ADR-0050). Each pool host runs one language
// (Node pool.mjs for node*, the subinterpreter pool.py for python*), so a function pools exactly
// when a host for its runtime exists — node via the default poolShimCommand, python via a
// WithPoolShimFor registration. With no python host configured, poolHostFor(python*) is nil → it
// runs solo (ADR-0049 Decision 8 preserved); with no host at all, every function is solo.
func (r *Reconciler) poolKeyFor(fn *v1.Function) (pooling.PoolKey, bool) {
	key, ok := pooling.KeyOf(fn)
	if !ok || r.poolHostFor(fn.Spec.Runtime) == nil {
		return pooling.PoolKey{}, false
	}
	return key, true
}

// assign resolves a function's pooling placement (ADR-0046/0050). A function is solo unless it
// declares a worker id AND a pool host exists for its runtime family (poolKeyFor). Otherwise it
// lists every function declaring the same (namespace, runtime, worker-id) from the store and runs
// the pure Assigner over them, so admission is a stable function of declared membership.
func (r *Reconciler) assign(ctx context.Context, fn *v1.Function) (pooling.Assignment, error) {
	const op = "function.assign"
	key, pooled := r.poolKeyFor(fn)
	if !pooled {
		return pooling.Assignment{Pooled: false}, nil // no worker id or no host for this runtime → solo
	}
	sameKey, err := r.sameKeyFunctions(ctx, key)
	if err != nil {
		return pooling.Assignment{}, fault.Wrapf(err, fault.KindOf(err), op, "list same-key functions")
	}
	return r.assigner.Assign(fn, sameKey, r.poolLimit)
}

// sameKeyFunctions returns every function in the store declaring key (ns, runtime, worker-id),
// Ready or not — the membership the cap + manifest are computed over (ADR-0046 Decision 3/4) —
// each as pinnedMember returns it.
func (r *Reconciler) sameKeyFunctions(ctx context.Context, key pooling.PoolKey) ([]*v1.Function, error) {
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.sameKeyFunctions", "list functions")
	}
	out := make([]*v1.Function, 0, len(list.Items))
	for _, obj := range list.Items {
		fn, ok := obj.(*v1.Function)
		if !ok {
			continue
		}
		if k, isPooled := r.poolKeyFor(fn); isPooled && k == key {
			if fn, err = r.pinnedMember(ctx, fn); err != nil {
				return nil, err
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
// spec.imageDigest. A member whose Revision is not stamped yet is returned as stored.
func (r *Reconciler) pinnedMember(ctx context.Context, m *v1.Function) (*v1.Function, error) {
	tmpl, digest, err := r.revisionTemplate(ctx, m, v1.ObjectName(revisionName(m)))
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return m, nil
		}
		return nil, err
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
// members (ADR-0046 Decision 6), and judges it: the member is ready iff the pool worker serves (Decision 5). Pooled
// members can't declare secrets (gated in Reconcile), and a pooled function's shared worker can't isolate a
// per-function catalog token, so neither env reaches the pool (a catalog-consuming function runs solo, ADR-0086). The
// serving revision follows the current one once the pool worker is ready (ADR-0143 Decision 8).
func (r *Reconciler) convergePooled(ctx context.Context, fn *v1.Function, a pooling.Assignment) (verdict, error) {
	running, err := r.ensurePool(ctx, a.Key, fn)
	if err != nil {
		return verdict{}, err
	}
	// A pool host serves only once every member's handler has loaded, then fails its readiness while any one handler's
	// thread respawns (ADR-0044 Decision 4). No endpoint judges one member, so a member is ready while its pool is live
	// (ADR-0046 Decision 5).
	ready, shapeFailed := r.readyReplicas(ctx, a.Key.Namespace, poolInstanceName(a.Key), "", running, 1, livenessPath)
	serving := servingPhase(fn.Status.Phase)
	if serving {
		shapeFailed = false // ADR-0142: in a pass that started serving, a Failed replica is a crash under repair
	}
	if ready >= 1 {
		fn.Status.ServingRevision = fn.Status.CurrentRevision
	}
	return verdict{running: running, ready: ready, shapeFailed: shapeFailed, serving: serving}, nil
}

// ensurePool drives the single pool worker for key to its desired state (ADR-0046 Decisions
// 4 & 6): it builds the manifest from the key's admitted members, computes the pool's desired
// replica as the max over those members' effective desired, and ensures exactly one pool
// worker — created/restarted only when the desired manifest differs from the running one
// (idempotent), reclaimed when desired is 0. It returns the pool worker's running count. self is
// the member being reconciled.
func (r *Reconciler) ensurePool(ctx context.Context, key pooling.PoolKey, self *v1.Function) (int, error) {
	members, err := r.admittedMembers(ctx, key)
	if err != nil {
		return 0, err
	}
	manifest, desired, err := r.poolManifest(ctx, members, self)
	if err != nil {
		return 0, err
	}
	sig := manifestSignature(manifest)

	poolName := poolInstanceName(key)
	insts, err := r.namedInstances(ctx, key.Namespace, poolName)
	if err != nil {
		return 0, err
	}
	exists := len(insts) > 0
	running := runningCount(insts) > 0

	switch {
	case desired == 0:
		// all members idle → reclaim the pool worker (RSS→0); next request wakes it.
		if running {
			if err := r.stopPool(ctx, insts); err != nil {
				return 0, err
			}
		}
		r.forgetPoolSig(key)
		return 0, nil
	case !exists:
		// first bring-up → create + start the pool worker from the current manifest.
		if err := r.createPool(ctx, key, manifest); err != nil {
			return 0, err
		}
		r.setPoolSig(key, sig)
	case sig != r.poolSig(key):
		// membership/artifact changed → rebuild: rewrite the manifest file then restart the
		// existing pool worker (pool.mjs reads its manifest at boot only, ADR-0046 workaround).
		// Restart reuses the instance id (a new PID), so a member never gets a second worker.
		if err := r.restartPool(ctx, key, insts, manifest); err != nil {
			return 0, err
		}
		r.setPoolSig(key, sig)
	case !running:
		// the pool worker exists but is stopped (a prior reclaim) and a member now wants it up
		// with the same manifest → just start it back (no rebuild).
		if err := r.startPoolInstance(ctx, insts); err != nil {
			return 0, err
		}
	default:
		// up, manifest unchanged → no-op (the idempotent path; no restart).
	}

	insts, err = r.namedInstances(ctx, key.Namespace, poolName)
	if err != nil {
		return 0, err
	}
	return runningCount(insts), nil
}

// admittedMembers returns the key's admitted members (the first PoolLimit by name), the set
// the manifest and the pool's desired replica are computed over (rejected members excluded).
func (r *Reconciler) admittedMembers(ctx context.Context, key pooling.PoolKey) ([]*v1.Function, error) {
	all, err := r.sameKeyFunctions(ctx, key)
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
// failure is returned. A member the pooled config/secret gate fails closed is left out too: no
// worker runs its code (ADR-0057).
func (r *Reconciler) poolManifest(ctx context.Context, members []*v1.Function, self *v1.Function) ([]poolManifestEntry, int, error) {
	const op = "function.poolManifest"
	manifest := make([]poolManifestEntry, 0, len(members))
	desired := 0
	for _, m := range members {
		if _, gerr := r.resolveBindingEnv(ctx, m, true); gerr != nil {
			continue
		}
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
		if d := r.desiredReplicas(m); d > desired {
			desired = d
		}
		manifest = append(manifest, poolManifestEntry{
			Name: string(m.Name), Artifact: path, Handler: m.Spec.Handler, Contract: contractPath,
		})
	}
	return manifest, desired, nil
}

// createPool creates+starts the pool worker for key. The pool host (pool.mjs, ADR-0044) reads
// its manifest from the FILE named by FUNCD_POOL_MANIFEST (not inline JSON), so the reconciler
// writes the admitted members' manifest to a temp file and passes its path. The process driver
// injects FUNCD_PORTFILE and the pool host writes its bound port back (ADR-0030 handshake), so
// the pool worker's address resolves exactly like a solo shim.
func (r *Reconciler) createPool(ctx context.Context, key pooling.PoolKey, manifest []poolManifestEntry) error {
	const op = "function.createPool"
	manifestPath, err := writePoolManifest(key, manifest)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "write pool manifest")
	}
	poolName := poolInstanceName(key)
	if _, perr := r.scheduler.Schedule(ctx, scheduler.Request{Namespace: key.Namespace, Name: poolName, Replica: 0}); perr != nil {
		return fault.Wrapf(perr, fault.KindOf(perr), op, "schedule pool worker")
	}
	spec := runtime.WorkerSpec{
		Namespace: key.Namespace,
		Name:      poolName,
		Replica:   0,
		Image:     key.Runtime,
		Command:   r.poolHostFor(v1.RuntimeName(key.Runtime)), // node pool.mjs, or python pool.py (ADR-0050)
		Env:       map[string]string{"FUNCD_POOL_MANIFEST": manifestPath},
	}
	inst, cerr := r.runtime.Create(ctx, spec)
	if cerr != nil {
		return fault.Wrapf(cerr, fault.KindOf(cerr), op, "create pool worker")
	}
	if serr := r.runtime.Start(ctx, inst.ID); serr != nil {
		return fault.Wrapf(serr, fault.KindOf(serr), op, "start pool worker")
	}
	return nil
}

// restartPool rewrites the pool worker's manifest file (same stable path the existing instance
// launched with) and restarts the existing instance so the pool host re-reads the new manifest
// at boot. The instance id is reused; the restart is observable as a new PID (ADR-0046 V1
// rebuild). The members' artifacts are already materialized by poolManifest.
func (r *Reconciler) restartPool(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance, manifest []poolManifestEntry) error {
	const op = "function.restartPool"
	if _, err := writePoolManifest(key, manifest); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "rewrite pool manifest")
	}
	if err := r.stopPool(ctx, insts); err != nil {
		return err
	}
	return r.startPoolInstance(ctx, insts)
}

// startPoolInstance (re)starts the existing pool worker instance(s) — used both to wake a
// reclaimed pool back up and as the second half of a rebuild. The process driver re-execs the
// command, so the pool host re-reads its (possibly rewritten) manifest file.
func (r *Reconciler) startPoolInstance(ctx context.Context, insts []runtime.Instance) error {
	const op = "function.startPoolInstance"
	for _, in := range insts {
		if serr := r.runtime.Start(ctx, in.ID); serr != nil {
			return fault.Wrapf(serr, fault.KindOf(serr), op, "start pool worker")
		}
	}
	return nil
}

// writePoolManifest serializes the manifest to a stable per-key file and returns its path. The
// pool host reads this file at boot (ADR-0044); a rebuild overwrites it in place before the new
// worker starts. Per-key (not per-start) so a leftover file can never accumulate unbounded.
func writePoolManifest(key pooling.PoolKey, manifest []poolManifestEntry) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), "funcd-pool")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, string(poolInstanceName(key))+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
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
// was deleted or moved off the key. Only a member's own reconcile drives its pool (ensurePool), so nothing else would
// reclaim it (ADR-0046 Decision 6). The workers are listed before the Functions, so a pool created in between has its
// member listed.
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
	declared := map[v1.ObjectName]bool{}
	for _, obj := range list.Items {
		if fn, ok := obj.(*v1.Function); ok {
			if key, pooled := r.poolKeyFor(fn); pooled {
				declared[poolInstanceName(key)] = true
			}
		}
	}
	for _, in := range insts {
		if declared[in.Name] || !strings.HasPrefix(string(in.Name), poolInstancePrefix) {
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return err
		}
		r.forgetPoolSigOf(ns, in.Name)
	}
	return nil
}

// poolInstancePrefix starts every pool worker's name (poolInstanceName).
const poolInstancePrefix = "__pool__"

// poolInstanceName is the synthetic worker name for a pool key. It encodes runtime + worker
// id so distinct keys never collide, and is prefixed so it can never equal a real function
// name (a DNS-1123 label cannot contain "__"), keeping a member's solo lookups separate.
func poolInstanceName(key pooling.PoolKey) v1.ObjectName {
	return v1.ObjectName(poolInstancePrefix + key.Runtime + "__" + key.Worker)
}

// manifestSignature is a stable digest of the pool manifest used for idempotent restarts:
// the pool worker is rebuilt only when this changes (a member joined/left or an artifact
// path changed). Order-independent because the manifest is already name-sorted by the caller.
func manifestSignature(manifest []poolManifestEntry) string {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "" // marshal of a []struct of strings cannot fail; "" forces a rebuild if it ever did
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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

// poolSig / setPoolSig / forgetPoolSig guard the per-key last-applied manifest signature, the
// state that makes pool rebuilds idempotent (only restart on a manifest diff). The map is
// per-reconciler (a platform singleton) and mutex-guarded for concurrent reconciles.
func (r *Reconciler) poolSig(key pooling.PoolKey) string {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	return r.poolSigs[key]
}

func (r *Reconciler) setPoolSig(key pooling.PoolKey, sig string) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	if r.poolSigs == nil {
		r.poolSigs = map[pooling.PoolKey]string{}
	}
	r.poolSigs[key] = sig
}

func (r *Reconciler) forgetPoolSig(key pooling.PoolKey) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	delete(r.poolSigs, key)
}

// forgetPoolSigOf forgets the signature of the pool worker named name in ns.
func (r *Reconciler) forgetPoolSigOf(ns v1.NamespaceName, name v1.ObjectName) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	for key := range r.poolSigs {
		if key.Namespace == ns && poolInstanceName(key) == name {
			delete(r.poolSigs, key)
		}
	}
}
