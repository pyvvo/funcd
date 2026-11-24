package addon

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
)

// Reconcile converges one add-on object (ADR-0332 Decision 3). It reads the Addon of the object's kind on each pass and
// holds the object Pending, with no engine and no edge entry, while that Addon is gone, its config breaks the Addon's
// schema, the provider runtime runs no image (ADR-0344 Decision 9), its env does not evaluate, or a bound Secret is
// missing or forbidden. Otherwise it converges one engine with the kind entry's image, port, probe and storage and the
// object's resources, fenced with no endpoint and admitting only funcd, and programs the object's edge entry once it is
// Ready.
// While an in-place Restore's mark stands, it starts the engine with restore.env, removes the copy once that engine is
// Ready, then starts it without restore.env and clears the mark once that one is Ready (ADR-0333 Decision 4). A
// deleted object loses its entry, its engine and then its storage.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.addon.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		r.forgetTarget(req.Namespace, req.Name)
		if err := r.deleted(ctx, req.Namespace, req.Name); err != nil {
			return controller.Result{}, err
		}
		return controller.Result{}, r.partGone(ctx, req.Namespace, req.Name)
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get add-on object")
	}
	o, ok := obj.(*v1.AddonObject)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not an AddonObject", req.Namespace, req.Name)
	}
	if sa, st, err := r.settingsKindOf(ctx, o.Spec.Kind); err != nil {
		return controller.Result{}, err
	} else if st != nil {
		return r.settingsObject(ctx, sa, st, o) // a settings object runs no engine (ADR-0340 Decision 3)
	}
	a, err := r.addonOf(ctx, o.Spec.Kind)
	if err != nil {
		return controller.Result{}, err
	}
	if (a != nil && a.Spec.Engineless()) || (a == nil && o.Status.Workload == v1.WorkloadEngineless) {
		return r.enginelessObject(ctx, a, o) // an engineless object runs no engine (ADR-0345)
	}
	if err := r.remember(ctx, o); err != nil {
		return controller.Result{}, err
	}
	if a == nil {
		return r.hold(ctx, o, reasonAddonNotFound, fmt.Sprintf("no Addon registers the kind %q", o.Spec.Kind), 0)
	}
	// The engine runs the revision the object pins, never the Addon's spec of the pass (ADR-0339 Decision 2), and so do
	// its settings kinds, while the Addon declares them (ADR-0358 Decision 5).
	current := a
	a, rev, ph, err := r.pinned(ctx, a, o)
	if err != nil {
		return controller.Result{}, err
	}
	if ph != nil {
		return r.hold(ctx, o, ph.reason, ph.message, 0)
	}
	if !r.runsImages {
		return r.hold(ctx, o, reasonRuntimeUnavailable, unavailable(a), 0)
	}
	if msg, err := configBreaks(a, o); err != nil {
		return controller.Result{}, err
	} else if msg != "" {
		return r.hold(ctx, o, reasonConfigInvalid, msg, 0)
	}
	env, err := RenderEnv(a.Spec.KindAttributes().Env, map[string]json.RawMessage{configDoc: configOf(o)}, schemas(a.Spec.KindAttributes().Config))
	if err != nil {
		return r.hold(ctx, o, reasonEnvInvalid, err.Error(), 0)
	}
	step, err := r.restoreStep(ctx, a, o)
	if err != nil {
		return controller.Result{}, err
	}
	if step == restoreLoading {
		renv, err := restoreEnv(a, o)
		if err != nil {
			return r.hold(ctx, o, reasonEnvInvalid, err.Error(), 0)
		}
		maps.Copy(env, renv)
	}
	bound, err := r.secretEnv(ctx, a, o)
	switch fault.KindOf(err) {
	case "":
	case fault.NotFound:
		return r.hold(ctx, o, reasonSecretNotFound, err.Error(), r.referentPoll)
	case fault.Forbidden, fault.Invalid:
		// No Policy event reconciles an object, so a lifted forbid is found by the requeue (ADR-0321 Decision 5).
		return r.hold(ctx, o, reasonBindingResolveFailed, err.Error(), r.referentPoll)
	default:
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "resolve the Secrets of %s/%s", o.Namespace, o.Name)
	}
	secrets.MergeEnvGuarded(env, bound, r.logger)

	st, cerr := r.prov.Converge(ctx, r.engineSpec(a, o, env))
	if cerr != nil && st.Reason == reasonRuntimeUnavailable {
		// A runtime that can run no engine, funcdctl dev's with no container engine, says why (ADR-0334 Decision 1).
		return r.hold(ctx, o, reasonRuntimeUnavailable, cerr.Error(), 0)
	}
	if cerr != nil {
		st = provider.ProviderStatus{Reason: reasonEngineConvergeFailed}
	}
	if !st.Ready {
		r.seeTarget(o.Namespace, o.Name, false)
	}
	o.Status.Address, o.Status.Workload = st.Address, v1.WorkloadEngine // the mark outlives the Addon (ADR-0345 Decision 1)
	o.Status.Phase, o.Status.ObservedGeneration = v1.PhasePending, o.Generation
	if st.Ready {
		o.Status.Phase = v1.PhaseReady
		o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, ObservedGeneration: o.Generation})
	} else {
		reason, msg := cmp.Or(st.Reason, reasonEngineNotReady), "the engine does not answer its readiness path yet"
		if cerr != nil {
			msg = cerr.Error()
		}
		o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg,
			ObservedGeneration: o.Generation})
	}
	moved, err := r.restoreMoves(ctx, a, o, step, cerr == nil && st.Ready)
	if err != nil {
		return controller.Result{}, err
	}
	if err := r.syncEntry(ctx, a, o, st); err != nil {
		return controller.Result{}, err
	}
	if _, uerr := r.store.Update(ctx, o); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	switch {
	case cerr != nil:
		return controller.Result{}, fault.Wrapf(cerr, fault.KindOf(cerr), op, "converge the engine of %s/%s", o.Namespace, o.Name)
	case moved:
		return controller.Result{Requeue: true}, nil
	case !st.Ready:
		return controller.Result{RequeueAfter: r.enginePoll}, nil
	}
	next, err := r.syncSettings(ctx, current, rev, o)
	if err != nil {
		return controller.Result{}, err
	}
	if next > 0 && next < r.period {
		return controller.Result{RequeueAfter: next}, nil
	}
	return controller.Result{RequeueAfter: r.period}, nil
}

// restoreMoves moves an in-place Restore's mark on once the engine of this pass is Ready: after an engine started with
// restore.env, the copy goes, then the mark turns Loaded, and the next pass starts the engine without restore.env; after
// that one, the mark goes. It reports a mark turned Loaded.
func (r *Reconciler) restoreMoves(ctx context.Context, a *v1.Addon, o *v1.AddonObject, step restoreStep, ready bool) (bool, error) {
	switch {
	case !ready || step == restoreNone:
		return false, nil
	case step == restoreLoaded:
		o.Status.Restore = nil
		return false, nil
	}
	if err := r.clearCopy(ctx, a, o); err != nil {
		return false, err
	}
	o.Status.Restore.State = v1.AddonRestoreLoaded
	return true, nil
}

// addonOf is the Addon that registers kind, whatever its name, matched by plural as the API matches it; nil when none
// does.
func (r *Reconciler) addonOf(ctx context.Context, kind string) (*v1.Addon, error) {
	list, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "services.addon.addonOf", "list Addons")
	}
	for _, item := range list.Items {
		if a, ok := item.(*v1.Addon); ok && v1.SameAddonKind(a.Spec.Kind(), kind) {
			return a, nil
		}
	}
	return nil, nil
}

// unavailable is why a's engines do not run on a process server (ADR-0344 Decision 9, ADR-0149's reason): its
// provider runtime is the process driver's, which runs no image.
func unavailable(a *v1.Addon) string {
	return fmt.Sprintf("the engine of Addon %s is an image, and this server runs its workers on the process driver, which "+
		"runs no image: an engine needs runtime.mode containerd", a.Name)
}

// configBreaks names how the object's config breaks its Addon's current schema, "" when it fits: an Addon change applies
// to the next writes, so a stored object may no longer fit (ADR-0332 Decision 1).
func configBreaks(a *v1.Addon, o *v1.AddonObject) (string, error) {
	const op = "services.addon.configBreaks"
	v, err := contract.Compile(a.Spec.KindAttributes().Config)
	if err != nil {
		return "spec.config of Addon " + string(a.Name) + " is no longer a schema: " + err.Error(), nil
	}
	parts, err := Breaks(v, o.Spec.Config)
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "check the config of %s/%s", o.Namespace, o.Name)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return fmt.Sprintf("the config does not fit the %s schema of Addon %s: %s", a.Spec.Kind(), a.Name, strings.Join(parts, "; ")), nil
}

// Breaks names each way config, an empty object when unset, breaks the schema v: "spec.config.<path>: <message>", in
// the validator's order; none when it fits.
func Breaks(v *contract.Validator, config json.RawMessage) ([]string, error) {
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	details, err := v.Validate(config)
	if err != nil {
		return nil, err
	}
	parts := make([]string, len(details))
	for i, d := range details {
		parts[i] = "spec.config" + strings.ReplaceAll(d.Path, "/", ".") + ": " + d.Message
	}
	return parts, nil
}

// secretEnv reads the object's bound Secrets as the object, each read asked of Cedar (ADR-0332 Decision 4), and keeps
// only the keys its Addon's secretKeys lists.
func (r *Reconciler) secretEnv(ctx context.Context, a *v1.Addon, o *v1.AddonObject) (map[string]string, error) {
	if len(o.Spec.Bindings.Secret) == 0 {
		return nil, nil
	}
	resolved, err := provider.ResolveEnv(ctx, provider.EnvDeps{
		Secrets: r.secrets,
		Store:   r.store,
		Identity: func(ns v1.NamespaceName) auth.Identity {
			return secrets.WorkloadIdentity(v1.KindAddonObject, ns, o.Name)
		},
		Logger: r.logger,
	}, o.Namespace, nil, o.Spec.Bindings.Secret)
	if err != nil {
		return nil, err
	}
	maps.DeleteFunc(resolved, func(k, _ string) bool { return !slices.Contains(a.Spec.KindAttributes().SecretKeys, k) })
	return resolved, nil
}

// engineSpec is the object's engine: its Addon's kind entry's image, port, probe and storage (ADR-0344 Decision 3), the
// object's resources, one replica, and reached only from funcd (ADR-0332 Decision 3). With egress on it has no output
// fence, so its connections pass the egress gateway under the EgressPolicies that name its object; off, it is fenced
// with no endpoint (ADR-0352 Decision 2).
func (r *Reconciler) engineSpec(a *v1.Addon, o *v1.AddonObject, env map[string]string) provider.ProviderSpec {
	var fence []netip.AddrPort
	if !r.egress {
		fence, _ = provider.EgressTo("")
	}
	entry, _ := a.Spec.KindRuntime()
	at := a.Spec.KindAttributes()
	spec := provider.ProviderSpec{
		Ref:        provider.ProviderRef{Namespace: o.Namespace, Name: o.Name},
		ServedKind: v1.Kind(at.Kind),
		Image:      entry.Image,
		Port:       at.Port,
		Env:        env,
		Readiness:  provider.ReadinessProbe{Path: at.Readiness.Path, ExpectStatus: expectStatus},
		Replicas:   1,
		Resources:  provider.ResourceSpec{CPU: o.Spec.Resources.CPU, Memory: o.Spec.Resources.Memory},
		EgressOnly: fence,
		HostOnly:   true,
	}
	if s := at.Storage; s != nil {
		spec.Storage = &provider.StorageSpec{Path: s.Path, UID: s.UID, GID: s.GID}
	}
	return spec
}

// syncEntry programs the object's edge entry while it is Ready and declares spec.ingress, and clears it otherwise. The
// entry is an upstream one (ADR-0138) at the Addon's basePath, kept, with its private paths, and its stance is the
// object's own ingress.auth, open when unset: an upstream entry takes no namespace default (ADR-0332 Decision 5). An
// oidc stance whose provider the namespace lacks keeps the entry's claims, refused, so its paths answer 503 (ADR-0335
// Decision 10).
func (r *Reconciler) syncEntry(ctx context.Context, a *v1.Addon, o *v1.AddonObject, st provider.ProviderStatus) error {
	const op = "services.addon.syncEntry"
	if r.routes == nil {
		return nil
	}
	src := routeSource(o.Namespace, o.Name)
	in := o.Spec.Ingress
	if in == nil || !st.Ready {
		return r.clearEntry(ctx, o)
	}
	missing, err := r.missingProvider(ctx, o)
	if err != nil {
		return err
	}
	enabled := in.Enabled == nil || *in.Enabled
	entry := router.Entry{
		Namespace: o.Namespace,
		Host:      in.Host,
		Auth:      stance(in),
		OIDC:      router.OIDCOf(in.Stance()),
		Enabled:   &enabled,
		Rules: []router.CompiledRule{{
			Path: a.Spec.AddonBasePath(), Upstream: "http://" + st.Address, KeepPath: true, PrivatePaths: a.Spec.KindAttributes().PrivatePaths,
		}},
		Owner: router.Owner{Kind: v1.KindAddonObject, Namespace: o.Namespace, Name: o.Name},
	}
	if missing != "" {
		entry.Refused = router.ReasonProviderNotFound
	}
	verdicts, err := r.routes.Set(ctx, src, []router.Entry{entry})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "program the edge entry %s", src)
	}
	if len(verdicts) != 1 {
		return fault.Internalf(op, "the edge aggregator returned %d verdicts for the 1 entry of %s", len(verdicts), src)
	}
	switch v := verdicts[0]; {
	case missing != "":
		o.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: reasonProviderNotFound,
			Message: fmt.Sprintf("spec.ingress.auth.provider %q names no OIDCProvider of namespace %s", missing, o.Namespace)})
	case v.Reason != "":
		o.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: v.Reason, Message: v.Message})
	default:
		o.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionTrue, Reason: reasonProgrammed})
	}
	return nil
}

// missingProvider is the OIDCProvider the object's own oidc stance names when its namespace does not hold it, else "".
func (r *Reconciler) missingProvider(ctx context.Context, o *v1.AddonObject) (v1.ObjectName, error) {
	provider, _, ok := o.Spec.Ingress.Stance().OIDC()
	if !ok {
		return "", nil
	}
	_, err := r.store.Get(ctx, v1.KindOIDCProvider.GVK(), o.Namespace, provider)
	if fault.KindOf(err) == fault.NotFound {
		return provider, nil
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "services.addon.syncEntry", "get OIDCProvider %s/%s", o.Namespace, provider)
	}
	return "", nil
}

// clearEntry retracts the object's edge entry: IngressReady goes with no spec.ingress, and is False while the object is
// not Ready.
func (r *Reconciler) clearEntry(ctx context.Context, o *v1.AddonObject) error {
	src := routeSource(o.Namespace, o.Name)
	if _, err := r.routes.Set(ctx, src, nil); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.clearEntry", "clear the edge entry %s", src)
	}
	if o.Spec.Ingress == nil {
		o.Status.Conditions = slices.DeleteFunc(o.Status.Conditions, func(c v1.Condition) bool { return c.Type == condIngressReady })
	} else {
		o.Status.Conditions.Set(v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: reasonEngineNotReady,
			Message: "the object is not Ready"})
	}
	return nil
}

// stance is an entry's edge stance: the object's own ingress.auth.mode, open when unset (ADR-0332 Decision 5).
func stance(in *v1.Ingress) v1.AuthMode {
	return cmp.Or(in.AuthMode(), v1.AuthOpen)
}

// hold fails an object closed (ADR-0121 Decision 2): its edge entry goes, it is Pending with reason and no address, and
// its engine stops. requeue is the poll of a wait no watch ends; 0 waits for one.
func (r *Reconciler) hold(ctx context.Context, o *v1.AddonObject, reason, message string, requeue time.Duration) (controller.Result, error) {
	const op = "services.addon.hold"
	if r.routes != nil {
		if err := r.clearEntry(ctx, o); err != nil {
			return controller.Result{}, err
		}
	}
	r.seeTarget(o.Namespace, o.Name, false)
	o.Status.Address = ""
	o.Status.Phase, o.Status.ObservedGeneration = v1.PhasePending, o.Generation
	o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message,
		ObservedGeneration: o.Generation})
	if _, err := r.store.Update(ctx, o); err != nil {
		return controller.Result{}, retryOnConflict(err, op)
	}
	if err := r.prov.Teardown(ctx, provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}); err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "stop the engine of %s/%s", o.Namespace, o.Name)
	}
	return controller.Result{RequeueAfter: requeue}, nil
}

// deleted retracts a deleted object's edge entry and stops its engine, then reclaims its storage as its deletion says,
// retain when this process never saw it (ADR-0333 Decision 5); each step is idempotent.
func (r *Reconciler) deleted(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	const op = "services.addon.deleted"
	if r.routes != nil {
		if _, err := r.routes.Set(ctx, routeSource(ns, name), nil); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "clear the edge entry of %s/%s", ns, name)
		}
	}
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	if err := r.prov.Teardown(ctx, ref); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "stop the engine of %s/%s", ns, name)
	}
	if err := r.reclaim(ctx, ref, r.deletionOf(ref)); err != nil {
		return err
	}
	r.forget(ref)
	return nil
}

// retryOnConflict swallows a status write's Conflict, which the next watch event reconciles again.
func retryOnConflict(err error, op string) error {
	if fault.KindOf(err) == fault.Conflict {
		return nil
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
}
