package function

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/embedimg"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
)

// shipped is a runtime that an Addon ships (ADR-0344 Decision 6): the Addon and its entry. The zero value is a runtime
// that is one of funcd's own, or that no stored Addon ships.
type shipped struct {
	addon v1.ObjectName
	entry v1.ShippedRuntime
}

func (s shipped) found() bool { return s.addon != "" }

// imageFor is the image that a new Revision of w records (ADR-0344 Decision 8): the entry's when it serves w's
// workload, else "".
func (s shipped) imageFor(w workload) string {
	if s.found() && s.entry.Serves(v1.WorkloadOf(w.kind())) {
		return s.entry.Image
	}
	return ""
}

// ownRuntime reports whether rt is one of funcd's own runtimes, which no Addon's shadows (ADR-0344 Decisions 4 and 6):
// a runtime whose image funcd embeds, or a name of the node or python family, which funcd's shims run.
func ownRuntime(rt v1.RuntimeName) bool {
	return rt.IsNode() || isPythonFamily(rt) || slices.Contains(embedimg.Runtimes(), string(rt))
}

// shippedRuntime resolves rt among the stored Addons, read at each call (ADR-0344 Decision 6): zero for one of funcd's
// own runtimes and for one that no Addon ships, which ADR-0149 Decision 2 resolves.
func (r *Reconciler) shippedRuntime(ctx context.Context, rt v1.RuntimeName) (shipped, error) {
	if rt == "" || ownRuntime(rt) {
		return shipped{}, nil
	}
	list, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return shipped{}, fault.Wrapf(err, fault.KindOf(err), "function.shippedRuntime", "list Addons")
	}
	for _, o := range list.Items {
		if a, ok := o.(*v1.Addon); ok {
			if e, ok := a.Spec.Shipped(rt); ok {
				return shipped{addon: a.Name, entry: e}, nil
			}
		}
	}
	return shipped{}, nil
}

// runtimeGate is the runtime gate of w (ADR-0149 Decision 2, ADR-0344 Decisions 4 and 6), whose current Revision
// recorded the runtime image recorded; false when w's runtime runs on this server. An Addon's runtime fails when its
// entry does not serve w's workload or no worker driver of this server runs an image; an http Function never names
// one, since its runtime is of the node family (Function.Validate). A Revision that recorded an image whose Addon is
// gone fails. Any other runtime takes ADR-0149's rule: in process mode one that no shim runs fails unless w pools on a host
// of its own (issue #371), in containerd mode the Lake engine's image fails, and an absent image fails at Create
// instead. The legacy mode with no Materializer is never gated.
func (r *Reconciler) runtimeGate(w workload, sh shipped, recorded string) (gateFailure, bool) {
	rt := w.spec.Runtime
	runs, why := r.runsImages()
	var cause string
	switch {
	case r.materializer == nil:
		return gateFailure{}, false
	case sh.found() && !sh.entry.Serves(v1.WorkloadOf(w.kind())):
		cause = fmt.Sprintf("Addon %q ships it for %s, not for %ss", sh.addon, workloadNames(sh.entry.Workloads), w.kind())
	case sh.found() && !runs:
		cause = "it is an Addon's image, which no worker driver of this server runs"
		if why != "" {
			cause += ": " + why // the engine search's, or the rootless engine's, under funcdctl dev (ADR-0346 Decision 1)
		}
	case sh.found():
		return gateFailure{}, false
	case recorded != "":
		cause = "the Addon that shipped it is gone"
	case r.endpointMode == EndpointLoopback:
		if r.shimFor(rt) != nil || r.pooled(w.fn()) {
			return gateFailure{}, false
		}
		cause = "no shim is registered for it"
	case r.endpointMode == EndpointNetnsFixedPort && rt == lakesvc.DuckDBRuntime:
		cause = "it is the Lake engine image, not a function runtime"
	default:
		return gateFailure{}, false
	}
	msg := unavailablePrefix(rt) + ": " + cause
	return gateFailure{reason: reasonRuntimeUnavailable, message: msg, readyMessage: msg, phase: v1.PhaseFailed}, true
}

// runsImages reports whether this server's worker runtime runs a worker from an image now, and when not, why: one
// that is a runtime.ImageRunner says so itself, as funcdctl dev's Pair does (ADR-0346 Decision 1); else the containerd
// driver does, and the process driver does not.
func (r *Reconciler) runsImages() (bool, string) {
	if ir, ok := r.runtime.(runtime.ImageRunner); ok {
		return ir.RunsImages()
	}
	return r.endpointMode == EndpointNetnsFixedPort, ""
}

// endpointHost is the host a container worker of a process-mode server reaches funcd's listeners by (ADR-0346
// Decision 5): its image runtime's, else funcd's loopback address.
func (r *Reconciler) endpointHost() string {
	if ir, ok := r.runtime.(runtime.ImageRunner); ok {
		return ir.EndpointHost()
	}
	return runtime.LoopbackHost
}

// addonImage is the image a process-mode server runs w's worker of revision rev from, when it runs one (ADR-0346
// Decision 2): the image of the Addon runtime that rev records (ADR-0344 Decision 8), on a worker runtime that runs
// images. ok is false for any other worker, which the process driver runs.
func (r *Reconciler) addonImage(ctx context.Context, w workload, rev v1.ObjectName) (string, bool, error) {
	if ownRuntime(w.spec.Runtime) {
		return "", false, nil
	}
	if ok, _ := r.runsImages(); !ok {
		return "", false, nil
	}
	snap, err := r.getRevision(ctx, w.meta().Namespace, rev)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return "", false, nil
	case err != nil:
		return "", false, fault.Wrapf(err, fault.KindOf(err), "function.addonImage", "get revision %q", rev)
	}
	return snap.Spec.RuntimeImage, snap.Spec.RuntimeImage != "", nil
}

// imageWorkerSpec is spec in the container shape for a worker that a process-mode server runs from img, an Addon
// runtime's under funcdctl dev (ADR-0346 Decisions 2, 5 and 6): no Command; the artifact's folder delivered read-only at
// containerArtifactDir, which the image driver copies into the worker's bundle volume; the local API on the worker's
// own listener with its token; funcd's endpoints by the host the worker reaches them by; and on an engine in a VM the
// fixed port that the engine publishes. No socket is bound in, and no volume.
func (r *Reconciler) imageWorkerSpec(spec runtime.WorkerSpec, w workload, rev v1.ObjectName, wo workerOf, artifactPath string, we workerEnv,
	img string) (runtime.WorkerSpec, error) {
	spec.Image = img
	spec.Mounts = []runtime.Mount{{Source: filepath.Dir(artifactPath), Target: containerArtifactDir, ReadOnly: true}}
	spec.Env = r.shimWorkerEnv(w, wo, filepath.Join(containerArtifactDir, filepath.Base(artifactPath)), containerArtifactDir, "", we, nil)
	host := r.endpointHost()
	if r.invokeSockets != nil {
		id := runtime.NewInstanceID(w.meta().Namespace, wo.name, rev, wo.replica)
		url, token, err := r.invokeSockets.ListenerFor(callerRef(w, wo), id)
		if err != nil {
			return runtime.WorkerSpec{}, fault.Wrapf(err, fault.KindOf(err), "function.workerSpec", "serve the local API of worker %s", id)
		}
		spec.Env["FUNCD_INVOKE_URL"], spec.Env["FUNCD_INVOKE_TOKEN"] = viaHost(url, host), token
	}
	for _, k := range we.endpoints {
		if v, ok := spec.Env[k]; ok {
			spec.Env[k] = viaHost(v, host)
		}
	}
	if host != runtime.LoopbackHost {
		spec.Env["FUNCD_PORT"] = strconv.Itoa(containerShimPort)
	}
	return spec, nil
}

// viaHost is v with funcd's loopback address written as host, the one a container worker reaches it by (ADR-0346
// Decision 5).
func viaHost(v, host string) string {
	if host == runtime.LoopbackHost {
		return v
	}
	return strings.ReplaceAll(v, runtime.LoopbackHost, host)
}

// workloadNames joins an entry's workloads for a RuntimeUnavailable message: "Job", "Function and Job".
func workloadNames(ws []v1.Workload) string {
	names := make([]string, len(ws))
	for i, w := range ws {
		names[i] = string(w)
	}
	return strings.Join(names, " and ")
}

// fillRuntimeImage records, once, the image of the Addon runtime that serves rev, a Revision of w created before that
// runtime served w (ADR-0344 Decision 8): when rev records none, the entry serves w's workload and no worker of rev
// runs. It returns the image rev records after the call. A worker started before the fill keeps the image it runs.
func (r *Reconciler) fillRuntimeImage(ctx context.Context, w workload, rev *v1.Revision, sh shipped) (string, error) {
	img := sh.imageFor(w)
	if rev.Spec.RuntimeImage != "" || img == "" {
		return rev.Spec.RuntimeImage, nil
	}
	runs, err := r.revisionRuns(ctx, rev)
	if err != nil || runs {
		return "", err
	}
	rev.Spec.RuntimeImage = img
	if _, err := r.store.Update(ctx, rev); err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "function.fillRuntimeImage", "record the runtime image of revision %q", rev.Name)
	}
	return img, nil
}

// revisionRuns reports whether a worker of rev, of any owner in its namespace, has not ended.
func (r *Reconciler) revisionRuns(ctx context.Context, rev *v1.Revision) (bool, error) {
	all, err := r.runtime.List(ctx, rev.Namespace)
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "function.revisionRuns", "list workers")
	}
	return slices.ContainsFunc(all, func(in runtime.Instance) bool { return in.Revision == rev.Name && !in.State.Terminal() }), nil
}

// workerImage is the image that a container worker of revision rev of w runs (ADR-0344 Decision 8): the image of the
// Addon runtime that rev records, else imageFor's, the curated or the operator's (ADR-0149).
func (r *Reconciler) workerImage(ctx context.Context, w workload, rev v1.ObjectName) (string, error) {
	if !ownRuntime(w.spec.Runtime) {
		snap, err := r.getRevision(ctx, w.meta().Namespace, rev)
		switch {
		case err == nil && snap.Spec.RuntimeImage != "":
			return snap.Spec.RuntimeImage, nil
		case err != nil && fault.KindOf(err) != fault.NotFound:
			return "", fault.Wrapf(err, fault.KindOf(err), "function.workerImage", "get revision %q", rev)
		}
	}
	return r.imageFor(string(w.spec.Runtime)), nil
}

// runtimeGateFails reports whether fn's runtime gate fails at its revision rev, or cannot be read: a steady pass skips
// the gate, so it asks this of a Function on a runtime that is none of funcd's own (ADR-0344 Decision 6).
func (r *Reconciler) runtimeGateFails(ctx context.Context, fn *v1.Function, rev v1.ObjectName) bool {
	sh, err := r.shippedRuntime(ctx, fn.Spec.Runtime)
	if err != nil {
		return true
	}
	snap, err := r.getRevision(ctx, fn.Namespace, rev)
	if err != nil {
		return true
	}
	_, failed := r.runtimeGate(functionWorkload(fn), sh, snap.Spec.RuntimeImage)
	return failed
}

// MapAddon maps an Addon's change to every Function whose spec.runtime is none of funcd's own (ADR-0344 Decision 7): a
// watch event carries no prior state, so the runtimes the Addon shipped before the change are unknown.
func (r *Reconciler) MapAddon(ctx context.Context, _ v1.Object) []controller.Request {
	return r.onShippedRuntimes(ctx, v1.KindFunction)
}

// MapAddon maps an Addon's change to every Job whose spec.runtime is none of funcd's own, as for a Function (ADR-0344
// Decisions 7 and 11).
func (j *JobReconciler) MapAddon(ctx context.Context, _ v1.Object) []controller.Request {
	return j.r.onShippedRuntimes(ctx, v1.KindJob)
}

// onShippedRuntimes are the requests of every stored object of kind k, a Function or a Job, whose spec.runtime an Addon
// may ship.
func (r *Reconciler) onShippedRuntimes(ctx context.Context, k v1.Kind) []controller.Request {
	list, err := r.store.List(ctx, k.GVK(), store.ListOptions{})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the workloads an Addon's change affects", "kind", k, "error", err)
		return nil
	}
	var reqs []controller.Request
	for _, o := range list.Items {
		var rt v1.RuntimeName
		switch x := o.(type) {
		case *v1.Function:
			rt = x.Spec.Runtime
		case *v1.Job:
			rt = x.Spec.Runtime
		}
		if rt != "" && !ownRuntime(rt) {
			m := o.GetObjectMeta()
			reqs = append(reqs, controller.Request{GVK: k.GVK(), Namespace: m.Namespace, Name: m.Name})
		}
	}
	return reqs
}
