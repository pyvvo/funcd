package function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
)

// accessIndex is what a namespace grants its Functions outside their specs, built once per pass: per Function, the
// KV tables ("kv/<store>/<table>") and Bucket prefixes ("blob/<bucket>/<prefix>") it owns, and the grants it holds
// through RolesAssignments, EgressPolicies and Policies, each named by what it grants.
type accessIndex struct{ owned, grants map[v1.ObjectName][]string }

// pooled reports, with no I/O, whether fn runs in a pool worker: it declares a worker id and a pool host serves its
// runtime family (ADR-0050).
func (r *Reconciler) pooled(fn *v1.Function) bool {
	return fn.Spec.Pooling.Worker != "" && r.poolHostFor(fn.Spec.Runtime) != nil
}

// accessIn lists the namespace's KVStores, Buckets, RolesAssignments, EgressPolicies and Policies once each into an
// accessIndex. An absent referent adds nothing; a List error fails the pass.
func (r *Reconciler) accessIn(ctx context.Context, ns v1.NamespaceName) (accessIndex, error) {
	const op = "function.accessIn"
	idx := accessIndex{owned: map[v1.ObjectName][]string{}, grants: map[v1.ObjectName][]string{}}
	list := func(k v1.Kind) ([]v1.Object, error) {
		res, err := r.store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %s", k)
		}
		return res.Items, nil
	}
	kvs, err := list(v1.KindKVStore)
	if err != nil {
		return accessIndex{}, err
	}
	for _, o := range kvs {
		if st, ok := o.(*v1.KVStore); ok {
			for _, t := range st.Spec.Tables {
				if t.Owner != "" {
					idx.owned[t.Owner] = append(idx.owned[t.Owner], "kv/"+string(st.Name)+"/"+t.Name)
				}
			}
		}
	}
	buckets, err := list(v1.KindBucket)
	if err != nil {
		return accessIndex{}, err
	}
	for _, o := range buckets {
		if b, ok := o.(*v1.Bucket); ok {
			for _, p := range b.Spec.Prefixes {
				if p.Owner != "" {
					idx.owned[p.Owner] = append(idx.owned[p.Owner], "blob/"+string(b.Name)+"/"+p.Name)
				}
			}
		}
	}
	ras, err := list(v1.KindRolesAssignment)
	if err != nil {
		return accessIndex{}, err
	}
	for _, o := range ras {
		if ra, ok := o.(*v1.RolesAssignment); ok {
			idx.addRolesAssignment(ra)
		}
	}
	eps, err := list(v1.KindEgressPolicy)
	if err != nil {
		return accessIndex{}, err
	}
	for _, o := range eps {
		if ep, ok := o.(*v1.EgressPolicy); ok {
			rules, merr := json.Marshal(ep.Spec.Rules)
			if merr != nil {
				return accessIndex{}, fault.Wrapf(merr, fault.Internal, op, "encode EgressPolicy %s", ep.Name)
			}
			grant := "egresspolicy/" + string(ep.Name) + "@" + sha256Hex(rules)
			for _, fn := range ep.Spec.AppliesTo {
				idx.grants[fn] = append(idx.grants[fn], grant)
			}
		}
	}
	// this namespace's Policies only: a Policy grants nothing outside its namespace, so another's must not split a pool
	pols, err := list(v1.KindPolicy)
	if err != nil {
		return accessIndex{}, err
	}
	for _, o := range pols {
		if p, ok := o.(*v1.Policy); ok {
			byFn, perr := cedarauth.FunctionGrants(p.Namespace, p.Name, p.Spec.Cedar)
			if perr != nil {
				return accessIndex{}, fault.Wrapf(perr, fault.KindOf(perr), op, "read Policy %s/%s", p.Namespace, p.Name)
			}
			for fn, gs := range byFn {
				idx.grants[fn] = append(idx.grants[fn], gs...)
			}
		}
	}
	return idx, nil
}

// addRolesAssignment adds a grant per entry whose principal is a Function: the role and scope it grants, both
// defaults applied, never the principal, so Functions granted one role on one scope stay together.
func (idx accessIndex) addRolesAssignment(ra *v1.RolesAssignment) {
	for _, e := range ra.Spec.Assignments {
		p := e.Principal
		if p == nil {
			p = ra.Spec.Principal
		}
		if p == nil || p.Kind != v1.PrincipalKindFunction {
			continue
		}
		scope := e.Scope
		if scope == nil {
			scope = ra.Spec.Scope
		}
		granted := struct {
			RoleRef v1.RoleRef   `json:"roleRef"`
			Scope   *v1.ScopeRef `json:"scope"`
		}{e.RoleRef, scope}
		data, err := json.Marshal(granted)
		if err != nil {
			continue // a struct of strings cannot fail to encode
		}
		idx.grants[p.Name] = append(idx.grants[p.Name], "rolesassignment/"+string(ra.Name)+"@"+sha256Hex(data))
	}
}

// sha256Hex is the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// poolKeyFor returns fn's pool key IFF it is eligible to co-pool (pooled): its namespace, runtime, worker id and
// access — the KV tables it both owns and binds, every Bucket prefix it owns, its resource group and its grants
// from idx.
func (r *Reconciler) poolKeyFor(fn *v1.Function, idx accessIndex) (pooling.PoolKey, bool) {
	if !r.pooled(fn) {
		return pooling.PoolKey{}, false
	}
	var owned []string
	for _, o := range idx.owned[fn.Name] {
		if strings.HasPrefix(o, "blob/") || slices.ContainsFunc(fn.Spec.KV, func(b v1.FunctionKV) bool {
			return o == "kv/"+string(b.Store)+"/"+b.Table
		}) {
			owned = append(owned, o)
		}
	}
	grants := slices.Clone(idx.grants[fn.Name])
	if rg := fn.ResourceGroup; rg != "" {
		grants = append(grants, "group/"+string(rg))
	}
	slices.Sort(grants)
	return pooling.KeyOf(fn, owned, slices.Compact(grants))
}

// MapAccess maps a KVStore, Bucket, RolesAssignment, EgressPolicy or Policy change to every Function of its namespace
// that declares a pool worker, Idle included: the change can move each to another pool.
func (r *Reconciler) MapAccess(ctx context.Context, obj v1.Object) []controller.Request {
	ns := obj.GetNamespace()
	res, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		r.logger.Warn("could not list the Functions an access change affects", "namespace", ns, "err", err)
		return nil
	}
	var out []controller.Request
	for _, o := range res.Items {
		if fn, ok := o.(*v1.Function); ok && fn.Spec.Pooling.Worker != "" {
			out = append(out, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: fn.Namespace, Name: fn.Name})
		}
	}
	return out
}

// poolSetKey names a pool worker's member set.
func poolSetKey(ns v1.NamespaceName, worker v1.ObjectName) string {
	return string(ns) + "/" + string(worker)
}

// setPoolMembers records the member set of a pool worker before it is created, so its first records are kept.
func (r *Reconciler) setPoolMembers(ns v1.NamespaceName, worker v1.ObjectName, members []v1.ObjectName) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	r.poolSets[poolSetKey(ns, worker)] = slices.Clone(members)
}

// PoolMembers is a snapshot of the member set last recorded for pool worker `worker` in ns: its names and a lookup
// over them. ok is false when none is recorded.
func (r *Reconciler) PoolMembers(ns v1.NamespaceName, worker v1.ObjectName) (members []v1.ObjectName, isMember func(name string) bool, ok bool) {
	r.poolMu.Lock()
	set, ok := r.poolSets[poolSetKey(ns, worker)]
	r.poolMu.Unlock()
	if !ok {
		return nil, func(string) bool { return false }, false
	}
	members = slices.Clone(set)
	byName := make(map[string]bool, len(members))
	for _, m := range members {
		byName[string(m)] = true
	}
	return members, func(name string) bool { return byName[name] }, true
}

// poolLiveness is when a key's pool worker last answered its liveness.
type poolLiveness struct {
	worker runtime.InstanceID
	at     time.Time
}

// poolDrain is the drain of a key's old pool workers: since when worker next of the current manifest listens.
type poolDrain struct {
	next  runtime.InstanceID
	since time.Time
}

// poolLastLive is when pool worker id of key last answered /health/liveness; zero when it has not.
func (r *Reconciler) poolLastLive(key pooling.PoolKey, id runtime.InstanceID) time.Time {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	if l := r.poolLive[key]; l.worker == id {
		return l.at
	}
	return time.Time{}
}

// markPoolLive records that pool worker id of key answered at `at`.
func (r *Reconciler) markPoolLive(key pooling.PoolKey, id runtime.InstanceID, at time.Time) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	r.poolLive[key] = poolLiveness{worker: id, at: at}
}
