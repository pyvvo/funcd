package function

import (
	"context"
	"errors"
	"maps"
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

// workloadHook is one binding provider's part in starting a Function's workers (ADR-0306 Decision 1). Its check runs
// before any worker exists: it holds fn (a gate), fails the pass (an error), or returns the env fn's workers share with
// the members of a pool that bind alike.
type workloadHook interface {
	check(ctx context.Context, fn *v1.Function) (shared map[string]string, gate *gateFailure, err error)
}

// ownWorkloadHook is a workload hook that also gives fn's workers env of fn's own: own is stable across passes, issue
// makes a part anew for each worker, nil when there is none. An own part that cannot be made holds fn with its hook's
// reason.
type ownWorkloadHook interface {
	workloadHook
	own(fn *v1.Function) (own map[string]string, issue func() map[string]string, gate *gateFailure)
}

var (
	_ ownWorkloadHook = s3Hook{}
	_ ownWorkloadHook = lakeHook{}
)

// workloadHooks are the workload hooks in the order their gates hold a Function (ADR-0306 Decision 2).
func (r *Reconciler) workloadHooks() []workloadHook {
	return []workloadHook{rootEntriesHook{r}, volumesHook{r}, configHook{r}, dataReferencesHook{r}, s3Hook{r}, lakeHook{r}}
}

// bindings resolves fn's current bindings before any worker boots, every hook's check and then their own parts: the env
// they give fn's workers, or the first gate that holds fn.
func (r *Reconciler) bindings(ctx context.Context, fn *v1.Function) (boundEnv, error) {
	shared, g, err := checkAll(ctx, fn, r.workloadHooks())
	if err != nil || g != nil {
		return boundEnv{gate: g}, err
	}
	own, issue, g := r.ownWorkerEnv(fn)
	if g != nil {
		return boundEnv{gate: g}, nil
	}
	return boundEnv{worker: workerEnv{shared: shared, own: own, issue: issue}}, nil
}

// checkAll runs each hook's check in order until one holds fn or fails, and merges the env they share, an earlier
// hook's key winning, so a bound config key overrides a provider's shared default (ADR-0306 Decision 3).
func checkAll(ctx context.Context, fn *v1.Function, hooks []workloadHook) (map[string]string, *gateFailure, error) {
	shared := map[string]string{}
	for _, h := range hooks {
		env, g, err := h.check(ctx, fn)
		if err != nil || g != nil {
			return nil, g, err
		}
		for k, v := range env {
			if _, ok := shared[k]; !ok {
				shared[k] = v
			}
		}
	}
	return shared, nil, nil
}

// ownWorkerEnv is the env fn's workload hooks give fn alone: their own parts, and a func that issues their parts for
// each of its workers, nil when none issues one. The first own part that cannot be made holds fn with its gate.
func (r *Reconciler) ownWorkerEnv(fn *v1.Function) (map[string]string, func() map[string]string, *gateFailure) {
	own := map[string]string{}
	var issues []func() map[string]string
	for _, h := range r.workloadHooks() {
		oh, ok := h.(ownWorkloadHook)
		if !ok {
			continue
		}
		env, issue, g := oh.own(fn)
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

func (h rootEntriesHook) check(ctx context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	g, err := h.r.ensureRootEntries(ctx, fn)
	return nil, g, err
}

// volumesHook reserves fn's local volumes, as fn's, before any worker mounts them (ADR-0262). A worker's mounts and
// their env come with its spec (workerVolumes).
type volumesHook struct{ r *Reconciler }

func (h volumesHook) check(ctx context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	g, err := h.r.ensureVolumes(ctx, fn)
	return nil, g, err
}

// configHook resolves fn's bound ConfigMaps and Secrets (ADR-0057, F15 last mile) and gives its workers the config keys
// (ADR-0245); no secret value reaches them (ADR-0249). A PDP deny, a missing Secret or an unconfigured resolver fails
// fn closed (SecretResolveFailed), a missing ConfigMap too (ConfigResolveFailed, ADR-0093 §4): one envresolve.Resolve
// call, two reasons. No ConfigMap or Secret event reconciles a Function, so a binding applied later is found only by a
// requeue. A running worker of the serving revision keeps its start-time env (ADR-0221 Decision 3).
type configHook struct{ r *Reconciler }

func (h configHook) check(ctx context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	res, err := h.r.resolveBindingEnv(ctx, fn)
	if err != nil {
		reason := "SecretResolveFailed"
		if errors.Is(err, envresolve.ErrConfig) {
			reason = "ConfigResolveFailed"
		}
		var requeue time.Duration
		if fault.KindOf(err) == fault.NotFound {
			requeue = h.r.referentPoll
		}
		return nil, &gateFailure{reason: reason, message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed, requeue: requeue}, nil
	}
	env := map[string]string{}
	h.r.addBindingEnv(env, res)
	return env, nil, nil
}

// dataReferencesHook holds fn not-Ready while a spec.bindings.blob or spec.bindings.kv binding names a Bucket prefix or
// a KVStore table not applied yet, and requeues (ADR-0121): the PDP denies the bound access until the referent exists.
type dataReferencesHook struct{ r *Reconciler }

func (h dataReferencesHook) check(ctx context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	requeue, reason, msg, err := h.r.resolveDataReferences(ctx, fn)
	if err != nil || !requeue {
		return nil, nil, err
	}
	return nil, &gateFailure{reason: reason, message: msg, readyMessage: msg, phase: v1.PhasePending, requeue: h.r.referentPoll}, nil
}

// s3Hook gives a Function that binds a Bucket the S3 gateway's region and endpoint, shared, and its key pair, issued
// with each worker (ADR-0085, ADR-0270 Decision 5). A disabled gateway, or a Function with no spec.bindings.blob, gets
// nothing. The function never chooses its secret, cannot derive a peer's, and the secret is never logged.
type s3Hook struct{ r *Reconciler }

func (h s3Hook) binds(fn *v1.Function) bool {
	g := h.r.s3Gateway
	return g.Enabled && g.Derive != nil && len(fn.Spec.Bindings.Blob) > 0
}

func (h s3Hook) check(_ context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	if !h.binds(fn) {
		return nil, nil, nil
	}
	// The sandbox-facing endpoint: Endpoint when set (the node address a netns'd worker can reach, the CNI bridge
	// gateway IP under containerd), else derived from the bind ListenAddr (loopback dev).
	endpoint := h.r.s3Gateway.Endpoint
	if endpoint == "" {
		endpoint = "http://" + h.r.s3Gateway.ListenAddr
	}
	return map[string]string{"AWS_REGION": "us-east-1", "AWS_ENDPOINT_URL_S3": endpoint}, nil, nil
}

func (h s3Hook) own(fn *v1.Function) (map[string]string, func() map[string]string, *gateFailure) {
	if !h.binds(fn) {
		return nil, nil, nil
	}
	ns, name := string(fn.Namespace), string(fn.Name)
	return nil, func() map[string]string {
		access, secret := h.r.s3Gateway.Derive(v1.KindFunction, ns, name)
		return map[string]string{"AWS_ACCESS_KEY_ID": access, "AWS_SECRET_ACCESS_KEY": secret}
	}, nil
}

// lakeHook gives a Function that binds a Lake each Lake's PEP proxy URL and, in dev, the DuckDB extension dir, shared,
// and its lake token, its own (ADR-0091 as reworked by ADR-0137). A bound Lake that is not Ready yet holds fn Pending
// and requeues fail-closed (LakeNotReady, F61).
type lakeHook struct{ r *Reconciler }

func (h lakeHook) check(ctx context.Context, fn *v1.Function) (map[string]string, *gateFailure, error) {
	if len(fn.Spec.Bindings.Lake) == 0 {
		return nil, nil, nil
	}
	urls, requeue, err := h.r.resolveLakeEnv(ctx, fn)
	if err != nil {
		return nil, lakeResolveFailed(err), nil
	}
	if requeue {
		const waiting = "a bound Lake is not Ready yet (no endpoint, token or live proxy); waiting"
		return nil, &gateFailure{reason: "LakeNotReady", message: waiting, readyMessage: waiting, phase: v1.PhasePending, requeue: h.r.referentPoll}, nil
	}
	h.r.addLakeExtensionDir(urls, fn)
	return urls, nil, nil
}

// own derives fn's lake token, the one place it is derived (ADR-0306 Decision 7). It is keyed by (ns, fn), so it is the
// same for every Lake fn binds: the proxy of each resolves it to this Function principal, then PEPs lake::query on that
// Lake (ADR-0137).
func (h lakeHook) own(fn *v1.Function) (map[string]string, func() map[string]string, *gateFailure) {
	if len(fn.Spec.Bindings.Lake) == 0 {
		return nil, nil, nil
	}
	token, err := lakegw.DeriveLakeToken(h.r.lakeMaster, fn.Namespace, fn.Name)
	if err != nil {
		return nil, nil, lakeResolveFailed(fault.Wrapf(err, fault.KindOf(err), "function.lakeHook.own", "derive lake token for %s/%s", fn.Namespace, fn.Name))
	}
	env := make(map[string]string, len(fn.Spec.Bindings.Lake))
	for _, c := range fn.Spec.Bindings.Lake {
		env["FUNCD_LAKE_"+strings.ToUpper(c.Alias)+"_TOKEN"] = token
	}
	return env, nil, nil
}

// lakeResolveFailed fails fn closed on a Lake binding that cannot be resolved.
func lakeResolveFailed(err error) *gateFailure {
	return &gateFailure{reason: "LakeResolveFailed", message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed}
}
