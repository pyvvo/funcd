package function

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
)

// workerEnv is what the workload hooks of the providers a Function binds give its workers (ADR-0306). shared is the
// same for every Function that binds alike, so the members of a pool share it; own is this Function's alone and stable
// across passes; issue makes a part anew for each worker as it is created, so neither the shared env nor a pool row's
// signature holds it (ADR-0270 Decision 5).
type workerEnv struct {
	shared, own map[string]string
	issue       func() map[string]string
	// endpoints are the keys of shared and own that hold funcd's loopback address, which a worker in a container
	// reaches by another host (ADR-0346 Decision 5).
	endpoints []string
}

// into writes w into the env of a solo worker: shared, then own, then a freshly issued part, so a worker's own and
// issued keys win over a bound config key of the same name, as a pool member's row does (ADR-0306 Decision 3).
func (w workerEnv) into(env map[string]string) {
	maps.Copy(env, w.shared)
	maps.Copy(env, w.own)
	if w.issue != nil {
		maps.Copy(env, w.issue())
	}
}

// boundEnv is the env fn's current bindings give every revision it boots, or the first gate that holds fn.
type boundEnv struct {
	worker workerEnv
	gate   *gateFailure
}

// workloadHook is one binding provider's part in starting a workload's workers, a Function's or a Job's (ADR-0306
// Decision 1, ADR-0347 Decision 5). Its check runs before any worker exists: it holds w (a gate), fails the pass (an
// error), or returns the env w's workers share with the members of a pool that bind alike, and the keys of its env,
// shared or own, that hold funcd's loopback address (ADR-0346 Decision 5).
type workloadHook interface {
	check(ctx context.Context, w workload) (shared map[string]string, endpoints []string, gate *gateFailure, err error)
}

// ownWorkloadHook is a workload hook that also gives w's workers env of w's own: own is stable across passes, issue
// makes a part anew for each worker, nil when there is none. An own part that cannot be made holds w with its hook's
// reason.
type ownWorkloadHook interface {
	workloadHook
	own(w workload) (own map[string]string, issue func() map[string]string, gate *gateFailure)
}

var (
	_ ownWorkloadHook = s3Hook{}
	_ ownWorkloadHook = lakeHook{}
	_ ownWorkloadHook = addonHook{}
)

// workloadHooks are the workload hooks in the order their gates hold a Function (ADR-0306 Decision 2).
func (r *Reconciler) workloadHooks() []workloadHook {
	return []workloadHook{rootEntriesHook{r}, volumesHook{r}, configHook{r}, dataReferencesHook{r}, s3Hook{r}, lakeHook{r},
		addonHook{r}}
}

// AddonProxy is where a worker reaches the add-on proxy, and the token it derives per Function (ADR-0338 Decision 3).
// Address binds the proxy at its first use and returns its host and port; nil when funcd runs no proxy.
type AddonProxy struct {
	Address func() (string, error)
	Token   func(ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) string
}

// addonHook gives a Function that binds add-on objects, for each alias, the proxy's base URL for it, shared, and the
// Function's own token there (ADR-0338 Decision 3). A missing object is the data references' gate.
type addonHook struct{ r *Reconciler }

func (h addonHook) check(_ context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	p := h.r.addonProxy
	if len(w.spec.Bindings.Addon) == 0 || p.Address == nil {
		return nil, nil, nil, nil
	}
	addr, err := p.Address()
	if err != nil {
		return nil, nil, nil, err
	}
	env := make(map[string]string, len(w.spec.Bindings.Addon))
	var endpoints []string
	for _, b := range w.spec.Bindings.Addon {
		k := "FUNCD_ADDON_" + strings.ToUpper(b.Alias) + "_URL"
		env[k] = "http://" + addr + "/addon/" + b.Alias
		endpoints = append(endpoints, k)
	}
	return env, endpoints, nil, nil
}

func (h addonHook) own(w workload) (map[string]string, func() map[string]string, *gateFailure) {
	p := h.r.addonProxy
	if len(w.spec.Bindings.Addon) == 0 || p.Address == nil || p.Token == nil {
		return nil, nil, nil
	}
	token := p.Token(w.meta().Namespace, w.kind(), w.meta().Name)
	env := make(map[string]string, len(w.spec.Bindings.Addon))
	for _, b := range w.spec.Bindings.Addon {
		env["FUNCD_ADDON_"+strings.ToUpper(b.Alias)+"_TOKEN"] = token
	}
	return env, nil, nil
}

// bindings resolves w's current bindings before any worker boots, every hook's check and then their own parts: the env
// they give w's workers, or the first gate that holds w.
func (r *Reconciler) bindings(ctx context.Context, w workload) (boundEnv, error) {
	shared, endpoints, g, err := checkAll(ctx, w, r.workloadHooks())
	if err != nil || g != nil {
		return boundEnv{gate: g}, err
	}
	own, issue, g := r.ownWorkerEnv(w)
	if g != nil {
		return boundEnv{gate: g}, nil
	}
	return boundEnv{worker: workerEnv{shared: shared, own: own, issue: issue, endpoints: endpoints}}, nil
}

// checkAll runs each hook's check in order until one holds w or fails, and merges the env they share, an earlier
// hook's key winning, so a bound config key overrides a provider's shared default (ADR-0306 Decision 3).
func checkAll(ctx context.Context, w workload, hooks []workloadHook) (map[string]string, []string, *gateFailure, error) {
	shared := map[string]string{}
	var endpoints []string
	for _, h := range hooks {
		env, eps, g, err := h.check(ctx, w)
		if err != nil || g != nil {
			return nil, nil, g, err
		}
		for k, v := range env {
			if _, ok := shared[k]; !ok {
				shared[k] = v
			}
		}
		endpoints = append(endpoints, eps...)
	}
	return shared, endpoints, nil, nil
}

// ownWorkerEnv is the env w's workload hooks give w alone: their own parts, and a func that issues their parts for
// each of its workers, nil when none issues one. The first own part that cannot be made holds w with its gate.
func (r *Reconciler) ownWorkerEnv(w workload) (map[string]string, func() map[string]string, *gateFailure) {
	own := map[string]string{}
	var issues []func() map[string]string
	for _, h := range r.workloadHooks() {
		oh, ok := h.(ownWorkloadHook)
		if !ok {
			continue
		}
		env, issue, g := oh.own(w)
		if g != nil {
			return nil, nil, g
		}
		maps.Copy(own, env)
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	if len(issues) == 0 {
		return own, nil, nil
	}
	return own, func() map[string]string {
		env := map[string]string{}
		for _, issue := range issues {
			maps.Copy(env, issue())
		}
		return env
	}, nil
}

// rootEntriesHook holds fn until what it defines at its root exists, as fn's, before a binding resolves to it
// (ADR-0248).
type rootEntriesHook struct{ r *Reconciler }

func (h rootEntriesHook) check(ctx context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	g, err := h.r.ensureRootEntries(ctx, w)
	return nil, nil, g, err
}

// volumesHook reserves fn's local volumes, as fn's, before any worker mounts them (ADR-0262). A worker's mounts and
// their env come with its spec (workerVolumes).
type volumesHook struct{ r *Reconciler }

func (h volumesHook) check(ctx context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	g, err := h.r.ensureVolumes(ctx, w)
	return nil, nil, g, err
}

// configHook resolves fn's bound ConfigMaps and Secrets (ADR-0057, F15 last mile) and gives its workers the config keys
// (ADR-0245); no secret value reaches them (ADR-0249). A PDP deny, a missing Secret or an unconfigured resolver fails
// fn closed (SecretResolveFailed), a missing ConfigMap too (ConfigResolveFailed, ADR-0093 §4): one envresolve.Resolve
// call, two reasons. No ConfigMap or Secret event reconciles a Function, so a binding applied later is found only by a
// requeue. A running worker of the serving revision keeps its start-time env (ADR-0221 Decision 3).
type configHook struct{ r *Reconciler }

func (h configHook) check(ctx context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	res, err := h.r.resolveBindingEnv(ctx, w.fn())
	if err != nil {
		reason := "SecretResolveFailed"
		if errors.Is(err, envresolve.ErrConfig) {
			reason = "ConfigResolveFailed"
		}
		var requeue time.Duration
		if fault.KindOf(err) == fault.NotFound {
			requeue = h.r.referentPoll
		}
		return nil, nil, &gateFailure{reason: reason, message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed, requeue: requeue}, nil
	}
	env := map[string]string{}
	h.r.addBindingEnv(env, res)
	return env, nil, nil, nil
}

// dataReferencesHook holds fn not-Ready while a spec.bindings.blob or spec.bindings.kv binding names a Bucket prefix or
// a KVStore table not applied yet, and requeues (ADR-0121): the PDP denies the bound access until the referent exists.
type dataReferencesHook struct{ r *Reconciler }

func (h dataReferencesHook) check(ctx context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	requeue, reason, msg, err := h.r.resolveDataReferences(ctx, w.fn())
	if err != nil || !requeue {
		return nil, nil, nil, err
	}
	return nil, nil, &gateFailure{reason: reason, message: msg, readyMessage: msg, phase: v1.PhasePending, requeue: h.r.referentPoll}, nil
}

// s3Hook gives a Function that binds a Bucket the S3 gateway's region and endpoint, shared, and its key pair, issued
// with each worker (ADR-0085, ADR-0270 Decision 5). A disabled gateway, or a Function with no spec.bindings.blob, gets
// nothing. The function never chooses its secret, cannot derive a peer's, and the secret is never logged.
type s3Hook struct{ r *Reconciler }

func (h s3Hook) binds(w workload) bool {
	g := h.r.s3Gateway
	return g.Enabled && g.Derive != nil && len(w.spec.Bindings.Blob) > 0
}

func (h s3Hook) check(_ context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	if !h.binds(w) {
		return nil, nil, nil, nil
	}
	// The sandbox-facing endpoint: Endpoint when set (the node address a netns'd worker can reach, the CNI bridge
	// gateway IP under containerd), else derived from the bind ListenAddr (loopback dev).
	endpoint := h.r.s3Gateway.Endpoint
	if endpoint == "" {
		endpoint = "http://" + h.r.s3Gateway.ListenAddr
	}
	return map[string]string{"AWS_REGION": "us-east-1", "AWS_ENDPOINT_URL_S3": endpoint}, []string{"AWS_ENDPOINT_URL_S3"}, nil, nil
}

func (h s3Hook) own(w workload) (map[string]string, func() map[string]string, *gateFailure) {
	if !h.binds(w) {
		return nil, nil, nil
	}
	kind, ns, name := w.kind(), string(w.meta().Namespace), string(w.meta().Name)
	return nil, func() map[string]string {
		access, secret := h.r.s3Gateway.Derive(kind, ns, name)
		return map[string]string{"AWS_ACCESS_KEY_ID": access, "AWS_SECRET_ACCESS_KEY": secret}
	}, nil
}

// lakeHook gives a Function that binds a Lake each Lake's PEP proxy URL and, in dev, the DuckDB extension dir, shared,
// and its lake token, its own (ADR-0091 as reworked by ADR-0137). A bound Lake that is not Ready yet holds fn Pending
// and requeues fail-closed (LakeNotReady, F61).
type lakeHook struct{ r *Reconciler }

func (h lakeHook) check(ctx context.Context, w workload) (map[string]string, []string, *gateFailure, error) {
	if len(w.spec.Bindings.Lake) == 0 {
		return nil, nil, nil, nil
	}
	urls, requeue, err := h.r.resolveLakeEnv(ctx, w.fn())
	if err != nil {
		return nil, nil, lakeResolveFailed(err), nil
	}
	if requeue {
		const waiting = "a bound Lake is not Ready yet (no endpoint, token or live proxy); waiting"
		return nil, nil, &gateFailure{reason: "LakeNotReady", message: waiting, readyMessage: waiting, phase: v1.PhasePending, requeue: h.r.referentPoll}, nil
	}
	endpoints := slices.Sorted(maps.Keys(urls)) // each key resolveLakeEnv returns is a Lake's proxy URL
	h.r.addLakeExtensionDir(urls, w.fn())
	return urls, endpoints, nil, nil
}

// own derives w's lake token, the one place it is derived (ADR-0306 Decision 7). It is keyed by (ns, kind, name), so it
// is the same for every Lake w binds: the proxy of each resolves it to this Function or Job principal, then PEPs
// lake::query on that Lake (ADR-0137, ADR-0351 Decision 4).
func (h lakeHook) own(w workload) (map[string]string, func() map[string]string, *gateFailure) {
	if len(w.spec.Bindings.Lake) == 0 {
		return nil, nil, nil
	}
	ns, name := w.meta().Namespace, w.meta().Name
	token, err := lakegw.DeriveLakeToken(h.r.lakeMaster, ns, w.kind(), name)
	if err != nil {
		return nil, nil, lakeResolveFailed(fault.Wrapf(err, fault.KindOf(err), "function.lakeHook.own", "derive lake token for %s/%s", ns, name))
	}
	env := make(map[string]string, len(w.spec.Bindings.Lake))
	for _, c := range w.spec.Bindings.Lake {
		env["FUNCD_LAKE_"+strings.ToUpper(c.Alias)+"_TOKEN"] = token
	}
	return env, nil, nil
}

// lakeResolveFailed fails fn closed on a Lake binding that cannot be resolved.
func lakeResolveFailed(err error) *gateFailure {
	return &gateFailure{reason: "LakeResolveFailed", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed}
}
