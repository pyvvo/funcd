package function

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/volume"
)

// containerVolumeDir is where a worker sees its volumes (ADR-0262 Decision 2), beside containerArtifactDir.
const containerVolumeDir = "/var/funcd/volumes"

// Volume reasons and the DataMissing condition (ADR-0262 Decisions 5, 7, 10 and 12).
const (
	reasonVolumesOff      = "VolumesOff"
	reasonVolumeCapacity  = "VolumeCapacity"
	reasonVolumeBudget    = "VolumeBudget"
	reasonVolumeInvalid   = "VolumeInvalid"
	reasonVolumeNotFound  = "VolumeNotFound"
	reasonVolumeHeld      = "VolumeHeld"
	condDataMissing       = v1.ConditionType("DataMissing")
	reasonNoVolume        = "NoVolume"
	reasonVolumesRestored = "Restored"
)

func localID(fn *v1.Function, name v1.ObjectName) volume.ID {
	return volume.ID{Namespace: fn.Namespace, Function: fn.Name, Name: name}
}

// ensureVolumes reserves fn's local volumes before any worker boots (ADR-0262 Decisions 5 and 7), adopting a ref'd
// retained volume first, and records a DataMissing loss (Decision 12). It returns the gate of the first volume that
// stops fn.
func (r *Reconciler) ensureVolumes(ctx context.Context, fn *v1.Function) (*gateFailure, error) {
	if len(fn.Spec.Volumes) == 0 {
		r.markVolumesSeen(fn) // a volume a later generation adds is new, not lost (#1200)
		return nil, nil
	}
	if r.volumes == nil {
		msg := "spec.volumes: volumes are off on this daemon"
		return &gateFailure{reason: reasonVolumesOff, message: msg, readyMessage: msg, phase: v1.PhaseFailed}, nil
	}
	if err := r.dataMissing(ctx, fn); err != nil {
		return nil, err
	}
	if fn.Status.ObservedGeneration < fn.Generation {
		if err := r.dropRemovedVolumes(ctx, fn); err != nil {
			return nil, err
		}
	}
	for i, v := range fn.Spec.Volumes {
		if v.Type != v1.VolumeLocal {
			continue
		}
		field := fmt.Sprintf("spec.volumes[%d]", i)
		id := localID(fn, v.VolumeName())
		deletion := cmp.Or(v.Deletion, v1.DeletionRetain)
		if v.Ref != "" {
			if err := r.volumes.Adopt(ctx, id, fn.UID); err != nil {
				return volumeGate(field, err)
			}
		}
		if _, err := r.volumes.Reserve(ctx, id, fn.UID, v.Bytes(), deletion); err != nil {
			return volumeGate(field, err)
		}
	}
	return nil, nil
}

// volumeGate maps a volume error to its gate; an error that names no gate fails the pass.
func volumeGate(field string, err error) (*gateFailure, error) {
	g := func(reason string, phase v1.Phase, requeue bool) (*gateFailure, error) {
		msg := field + ": " + withoutOp(err)
		f := &gateFailure{reason: reason, message: msg, readyMessage: msg, phase: phase}
		if requeue {
			f.requeue = controller.SupervisionPeriod
		}
		return f, nil
	}
	switch {
	case errors.Is(err, volume.ErrNotOwned):
		return g(reasonStoreNotOwned, v1.PhasePending, true)
	case errors.Is(err, volume.ErrCapacity):
		return g(reasonVolumeCapacity, v1.PhasePending, true)
	case errors.Is(err, volume.ErrBudget):
		return g(reasonVolumeBudget, v1.PhasePending, true)
	case fault.KindOf(err) == fault.NotFound:
		return g(reasonVolumeNotFound, v1.PhasePending, true)
	case fault.KindOf(err) == fault.Conflict:
		return g(reasonVolumeHeld, v1.PhasePending, true)
	case fault.KindOf(err) == fault.Invalid:
		return g(reasonVolumeInvalid, v1.PhaseFailed, false)
	}
	return nil, err
}

// dropRemovedVolumes applies its deletion policy to each local volume fn holds that its spec no longer declares: a
// delete volume goes, a retain one is kept with no owner.
func (r *Reconciler) dropRemovedVolumes(ctx context.Context, fn *v1.Function) error {
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
	}
	for _, in := range infos {
		if in.ID.Namespace != fn.Namespace || in.ID.Function != fn.Name || in.ID.Worker != "" || in.Owner != fn.UID {
			continue
		}
		if slices.ContainsFunc(fn.Spec.Volumes, func(v v1.VolumeEntry) bool { return v.Type == v1.VolumeLocal && v.VolumeName() == in.ID.Name }) {
			continue
		}
		if err := r.dropVolume(ctx, in); err != nil {
			return err
		}
	}
	return nil
}

// dropVolume removes a delete volume or retains a retain one (ADR-0262 Decision 7).
func (r *Reconciler) dropVolume(ctx context.Context, in volume.Info) error {
	if in.ID.Worker != "" || in.Deletion == v1.DeletionDelete {
		return r.volumes.Release(ctx, in.ID)
	}
	return r.volumes.Adopt(ctx, in.ID, "")
}

// workerVolumes reserves a new emptyDir of each emptyDir volume for the worker replica of fn is about to create, each
// taking over the space of the one the worker it replaces held (#1207), and returns every volume's mount and env value
// (ADR-0262 Decisions 2 and 3). A local volume is reserved again, idempotently, for its path.
func (r *Reconciler) workerVolumes(ctx context.Context, fn *v1.Function, id runtime.InstanceID) ([]runtime.Mount, map[string]string, error) {
	if len(fn.Spec.Volumes) == 0 {
		return nil, nil, nil
	}
	if r.volumes == nil {
		return nil, nil, fault.Invalidf("function.workerVolumes", "spec.volumes: volumes are off on this daemon")
	}
	r.volMu.Lock()
	old := r.emptyDirs[id]
	delete(r.emptyDirs, id)
	r.volMu.Unlock()
	token, err := workerToken()
	if err != nil {
		r.dropEmptyDirs(ctx, old)
		return nil, nil, err
	}
	var mounts []runtime.Mount
	env := make(map[string]string, len(fn.Spec.Volumes))
	var empty []volume.ID
	for _, v := range fn.Spec.Volumes {
		vid := localID(fn, v.VolumeName())
		var host string
		if v.Type == v1.VolumeEmptyDir {
			vid.Worker = token
			if i := slices.IndexFunc(old, func(o volume.ID) bool { return o.Name == vid.Name }); i >= 0 {
				host, err = r.volumes.Renew(ctx, old[i], vid, v.Bytes())
				old = slices.Delete(old, i, i+1)
			} else {
				host, err = r.volumes.Reserve(ctx, vid, "", v.Bytes(), "")
			}
			if err == nil {
				empty = append(empty, vid)
			}
		} else {
			host, err = r.volumes.Reserve(ctx, vid, fn.UID, v.Bytes(), cmp.Or(v.Deletion, v1.DeletionRetain))
		}
		if err != nil {
			r.dropEmptyDirs(ctx, append(empty, old...))
			return nil, nil, err
		}
		if r.endpointMode == EndpointNetnsFixedPort {
			target := path.Join(containerVolumeDir, string(v.VolumeName()))
			mounts = append(mounts, runtime.Mount{Source: host, Target: target})
			env[v1.VolumeEnv(v.VolumeName())] = target
		} else {
			env[v1.VolumeEnv(v.VolumeName())] = host
		}
	}
	r.dropEmptyDirs(ctx, old) // the emptyDirs of volumes the spec no longer declares
	if len(empty) > 0 {
		r.volMu.Lock()
		r.emptyDirs[id] = empty
		r.volMu.Unlock()
	}
	return mounts, env, nil
}

func workerToken() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fault.Wrapf(err, fault.Internal, "function.workerToken", "draw a worker token")
	}
	return hex.EncodeToString(b), nil
}

// releaseEmptyDirs releases the emptyDirs the worker id held: it was replaced, stopped or removed.
func (r *Reconciler) releaseEmptyDirs(ctx context.Context, id runtime.InstanceID) error {
	r.volMu.Lock()
	ids := r.emptyDirs[id]
	delete(r.emptyDirs, id)
	r.volMu.Unlock()
	for _, vid := range ids {
		if err := r.volumes.Release(ctx, vid); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) dropEmptyDirs(ctx context.Context, ids []volume.ID) {
	for _, vid := range ids {
		if err := r.volumes.Release(ctx, vid); err != nil {
			r.logger.Warn("could not release an emptyDir", "volume", vid.String(), "err", err)
		}
	}
}

// keepSlots records the replica slots indexes of revision rev as ones fn's pass creates again or waits to: the pass's
// sweep leaves their emptyDirs to the workers that replace them (#1207).
func (r *Reconciler) keepSlots(fn *v1.Function, rev v1.ObjectName, indexes []int) {
	key := fnKey{fn.Namespace, fn.Name}
	r.volMu.Lock()
	defer r.volMu.Unlock()
	if r.passSlots[key] == nil {
		r.passSlots[key] = map[runtime.InstanceID]bool{}
	}
	for _, i := range indexes {
		r.passSlots[key][runtime.NewInstanceID(fn.Namespace, fn.Name, rev, i)] = true
	}
}

// sweepEmptyDirs releases, at the end of a pass that converged fn's revisions, the emptyDirs of fn's workers that ended
// or are gone: an ended worker is replaced, never started again, so its emptyDir is never read again (ADR-0262 Decision
// 1). It sweeps once every revision's step kept its slots, so a step never releases the emptyDir of a slot another
// revision's step creates again in the same pass; that worker takes the space over (#1207).
func (r *Reconciler) sweepEmptyDirs(ctx context.Context, fn *v1.Function) error {
	if r.volumes == nil {
		return nil
	}
	key, prefix := fnKey{fn.Namespace, fn.Name}, string(fn.Namespace)+"/"+string(fn.Name)+"/"
	r.volMu.Lock()
	kept, converged := r.passSlots[key]
	delete(r.passSlots, key)
	var ended []runtime.InstanceID
	for id := range r.emptyDirs {
		if strings.HasPrefix(string(id), prefix) && !kept[id] {
			ended = append(ended, id)
		}
	}
	r.volMu.Unlock()
	if !converged || len(ended) == 0 {
		return nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return err
	}
	live := make(map[runtime.InstanceID]bool, len(insts))
	for _, in := range insts {
		if !in.State.Terminal() {
			live[in.ID] = true
		}
	}
	var gone []runtime.InstanceID
	for _, id := range ended {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		if err := r.releaseEmptyDirs(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// volumesGone applies the delete path to a deleted Function's volumes unless the platform is held (ADR-0262
// Decision 8): its emptyDirs go, each local volume per its deletion policy.
func (r *Reconciler) volumesGone(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	if r.volumes == nil || r.platformHeld() {
		return nil
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
	}
	for _, in := range infos {
		if in.ID.Namespace == ns && in.ID.Function == name {
			if err := r.dropVolume(ctx, in); err != nil {
				return err
			}
		}
	}
	r.volMu.Lock()
	for id := range r.emptyDirs {
		if strings.HasPrefix(string(id), string(ns)+"/"+string(name)+"/") {
			delete(r.emptyDirs, id)
		}
	}
	delete(r.passSlots, fnKey{ns, name})
	r.volMu.Unlock()
	return nil
}

func (r *Reconciler) platformHeld() bool { return r.hold != nil && r.hold.Held() }

// ReclaimVolumes runs at boot before the controller (ADR-0262 Decision 8): no worker survives a restart, so every
// emptyDir left goes, and the local volumes of Functions no longer stored, or stored under another UID, follow their
// deletion policy. While the platform is held it reclaims nothing: hold status lists the local volumes
// (VolumeOrphans), and the emptyDirs go once the hold lifts (releaseHeldLeftovers, #1204).
func (r *Reconciler) ReclaimVolumes(ctx context.Context) error {
	if r.volumes == nil {
		return nil
	}
	if err := r.bookStoredVolumes(ctx); err != nil {
		return err
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
	}
	if r.platformHeld() {
		r.volMu.Lock()
		for _, in := range infos {
			if in.ID.Worker != "" {
				r.heldLeftovers = append(r.heldLeftovers, in.ID)
			}
		}
		r.volMu.Unlock()
		return nil
	}
	for _, in := range infos {
		if in.ID.Worker != "" {
			if err := r.volumes.Release(ctx, in.ID); err != nil {
				return err
			}
			continue
		}
		if in.Owner == "" {
			continue
		}
		live, err := r.holds(ctx, in)
		if err != nil {
			return err
		}
		if !live {
			if err := r.dropVolume(ctx, in); err != nil {
				return err
			}
		}
	}
	return nil
}

// releaseHeldLeftovers releases, once the hold lifts, the emptyDirs a held boot kept: their workers ended with the last
// run, and an emptyDir is never restore evidence, so they hold no capacity until the next start (#1204).
func (r *Reconciler) releaseHeldLeftovers(ctx context.Context) {
	if r.platformHeld() {
		return
	}
	r.volMu.Lock()
	ids := r.heldLeftovers
	r.heldLeftovers = nil
	r.volMu.Unlock()
	for _, id := range ids {
		if err := r.volumes.Release(ctx, id); err != nil {
			r.logger.WarnContext(ctx, "could not release an emptyDir the held boot kept", "volume", id.String(), "err", err)
		}
	}
}

// holds reports whether the stored Function a local volume's path names holds it.
func (r *Reconciler) holds(ctx context.Context, in volume.Info) (bool, error) {
	uid, ok, err := r.FunctionUID(ctx, in.ID.Namespace, in.ID.Function)
	return ok && uid == in.Owner, err
}

// FunctionUID returns the UID of the stored Function, false when none is stored; the volume manager's Holder.
func (r *Reconciler) FunctionUID(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.UID, bool, error) {
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, fault.Wrapf(err, fault.KindOf(err), "function.FunctionUID", "get function %s/%s", ns, name)
	}
	return obj.GetObjectMeta().UID, true, nil
}

// VolumeOrphans returns the local volumes no stored Function holds, as "<function>/<volume>" refs of kind Function, for
// hold status: restore evidence while held (ADR-0262 Decision 8).
func (r *Reconciler) VolumeOrphans(ctx context.Context) ([]v1.ObjectRef, error) {
	if r.volumes == nil {
		return nil, nil
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []v1.ObjectRef
	for _, in := range infos {
		if in.ID.Worker != "" {
			continue
		}
		live, err := r.holds(ctx, in)
		if err != nil {
			return nil, err
		}
		if !live {
			out = append(out, v1.ObjectRef{Kind: v1.KindFunction, Namespace: in.ID.Namespace, Name: in.ID.Function + "/" + in.ID.Name})
		}
	}
	return out, nil
}

// StopWorkers stops every worker of Function ns/name and keeps its passes from creating or starting one until release
// is called: a Restore loads the Function's volume while none runs, so no worker writes there during the load or
// keeps a file the load replaced (ADR-0262 Decisions 11 and 12, #1206). A pass already creating workers ends first,
// and those are stopped too.
func (r *Reconciler) StopWorkers(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error) {
	key := fnKey{ns, name}
	r.volMu.Lock()
	r.restoring[key]++
	for r.launching[key] > 0 {
		r.launched.Wait()
	}
	r.volMu.Unlock()
	release = sync.OnceFunc(func() {
		r.volMu.Lock()
		if r.restoring[key]--; r.restoring[key] == 0 {
			delete(r.restoring, key)
		}
		r.volMu.Unlock()
	})
	if err := r.retireWorkers(ctx, ns, name); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// beginLaunch reports whether fn's pass may create or start workers, which StopWorkers forbids; a true answer is paired
// with endLaunch.
func (r *Reconciler) beginLaunch(fn *v1.Function) bool {
	key := fnKey{fn.Namespace, fn.Name}
	r.volMu.Lock()
	defer r.volMu.Unlock()
	if r.restoring[key] > 0 {
		return false
	}
	r.launching[key]++
	return true
}

func (r *Reconciler) endLaunch(fn *v1.Function) {
	key := fnKey{fn.Namespace, fn.Name}
	r.volMu.Lock()
	if r.launching[key]--; r.launching[key] == 0 {
		delete(r.launching, key)
	}
	r.volMu.Unlock()
	r.launched.Broadcast()
}

// retireWorkers retires every worker of Function ns/name that has not ended.
func (r *Reconciler) retireWorkers(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	insts, err := r.namedInstances(ctx, ns, name)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if in.State.Terminal() {
			continue
		}
		if err := r.retire(ctx, in); err != nil {
			return err
		}
	}
	return nil
}

// VolumeLost reports whether the stored Function's DataMissing names its local volume name as lost: what the volume
// holds dates from after the loss, so a Restore into it replaces that (ADR-0262 Decision 12, #1206).
func (r *Reconciler) VolumeLost(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error) {
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ns, fn)
	if fault.KindOf(err) == fault.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "function.VolumeLost", "get function %s/%s", ns, fn)
	}
	f, _ := obj.(*v1.Function)
	if f == nil {
		return false, nil
	}
	c, ok := f.Status.Conditions.Get(condDataMissing)
	return ok && c.Status == v1.ConditionTrue && slices.Contains(lostVolumes(c.Message), string(name)), nil
}

// markVolumesSeen records this process's first pass over fn, the only pass that may find a lost volume.
func (r *Reconciler) markVolumesSeen(fn *v1.Function) {
	r.volMu.Lock()
	r.volumesSeen[fnKey{fn.Namespace, fn.Name}] = true
	r.volMu.Unlock()
}

// dataMissing sets DataMissing on fn (ADR-0262 Decision 12, as ADR-0234 Decision 6): on this process's first pass,
// with durable volumes, a Function whose stored Ready is True for its current generation and whose local volume has no
// leaf lost its data; a Succeeded Restore into that volume since then clears it.
func (r *Reconciler) dataMissing(ctx context.Context, fn *v1.Function) error {
	key := fnKey{fn.Namespace, fn.Name}
	r.volMu.Lock()
	first, lost := !r.volumesSeen[key], r.volumesMissing[key]
	pending := len(lost) > 0
	r.volMu.Unlock()
	cur, has := fn.Status.Conditions.Get(condDataMissing)
	if pending && has && cur.Status == v1.ConditionTrue {
		r.volMu.Lock()
		delete(r.volumesMissing, key)
		r.volMu.Unlock()
		pending = false
	}
	if pending {
		r.setDataMissing(fn, lost)
		return nil
	}
	if has && cur.Status == v1.ConditionTrue {
		restored, err := r.volumesRestoredSince(ctx, fn, lostVolumes(cur.Message), time.Time(cur.LastTransitionTime))
		if err != nil || !restored {
			return err
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condDataMissing, Status: v1.ConditionFalse, Reason: reasonVolumesRestored, ObservedGeneration: fn.Generation})
		return nil
	}
	if !first {
		return nil
	}
	r.markVolumesSeen(fn)
	ready, ok := fn.Status.Conditions.Get(condReady)
	// The stored Ready vouches only for the volumes its pass reserved: those of the current generation when no pass
	// was left to run on an update (#1200).
	if !r.volumesDurable || !ok || ready.Status != v1.ConditionTrue || fn.Status.ObservedGeneration != fn.Generation || !fn.Spec.HasLocalVolume() {
		return nil
	}
	var gone []string
	for _, v := range fn.Spec.Volumes {
		if v.Type != v1.VolumeLocal {
			continue
		}
		// the leaf holds the data, not the meta beside it: a meta a crash damaged is no loss (#1203)
		_, err := r.volumes.Empty(ctx, localID(fn, v.VolumeName()))
		if fault.KindOf(err) != fault.NotFound {
			if err != nil {
				return err
			}
			continue
		}
		gone = append(gone, string(v.VolumeName()))
		r.logger.WarnContext(ctx, "function volume lost its data", "namespace", fn.Namespace, "function", fn.Name, "volume", v.VolumeName())
	}
	if len(gone) > 0 {
		r.volMu.Lock()
		r.volumesMissing[key] = gone
		r.volMu.Unlock()
		r.setDataMissing(fn, gone)
	}
	return nil
}

// dataMissingVolumes prefixes the lost volumes' names in DataMissing's message, which a later pass reads back.
const dataMissingVolumes = "lost volumes: "

// lostVolumes reads the volumes a DataMissing message names.
func lostVolumes(msg string) []string {
	rest, ok := strings.CutPrefix(msg, dataMissingVolumes)
	if !ok {
		return nil
	}
	names, _, _ := strings.Cut(rest, ";")
	return strings.Split(names, ",")
}

func (r *Reconciler) setDataMissing(fn *v1.Function, lost []string) {
	fn.Status.Conditions.Set(v1.Condition{Type: condDataMissing, Status: v1.ConditionTrue, Reason: reasonNoVolume, ObservedGeneration: fn.Generation,
		Message: dataMissingVolumes + strings.Join(lost, ",") + "; the Function was Ready before and these local volumes are gone, so they start empty: run a workload Restore into each"})
}

// volumesRestoredSince reports that Succeeded Restores created at or after since loaded every lost volume into fn.
func (r *Reconciler) volumesRestoredSince(ctx context.Context, fn *v1.Function, lost []string, since time.Time) (bool, error) {
	l, err := r.store.List(ctx, v1.KindRestore.GVK(), store.ListOptions{Namespace: fn.Namespace})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "function.dataMissing", "list restores in %q", fn.Namespace)
	}
	restored := map[string]bool{}
	for _, o := range l.Items {
		rs, ok := o.(*v1.Restore)
		if !ok || rs.Status.Phase != v1.RunSucceeded || time.Time(rs.CreationTime).Before(since) {
			continue
		}
		for _, m := range rs.Spec.Members {
			if m.Kind == v1.KindFunction && m.Volume != "" && cmp.Or(m.As, m.Name) == fn.Name {
				restored[string(m.Volume)] = true
			}
		}
	}
	for _, name := range lost {
		if !restored[name] {
			return false, nil
		}
	}
	return len(lost) > 0, nil
}

// MapRestore maps a Restore to the Functions whose volumes it loads, whose DataMissing it may clear.
func (r *Reconciler) MapRestore(_ context.Context, obj v1.Object) []controller.Request {
	rs, ok := obj.(*v1.Restore)
	if !ok {
		return nil
	}
	var out []controller.Request
	for _, m := range rs.Spec.Members {
		if m.Kind == v1.KindFunction && m.Volume != "" {
			out = append(out, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: rs.Namespace, Name: cmp.Or(m.As, m.Name)})
		}
	}
	return out
}

// DeleteVolume removes a retained local volume (ADR-0262 Decision 8): NotFound with none, Conflict while the stored
// Function its path names holds it.
func (r *Reconciler) DeleteVolume(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error {
	const op = "function.DeleteVolume"
	if r.volumes == nil {
		return fault.NotFoundf(op, "volumes are off")
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
	}
	id := volume.ID{Namespace: ns, Function: fn, Name: name}
	i := slices.IndexFunc(infos, func(in volume.Info) bool { return in.ID == id })
	if i < 0 {
		return fault.NotFoundf(op, "volume %s does not exist", id)
	}
	if in := infos[i]; in.Owner != "" {
		live, err := r.holds(ctx, in)
		if err != nil {
			return err
		}
		if live {
			return fault.Conflictf(op, "volume %s is held by Function %s/%s; delete the Function or drop the volume from its spec", id, ns, fn)
		}
	}
	return r.volumes.Release(ctx, id)
}

// startReason is a failed start's Ready reason: an emptyDir that does not fit names its volume reason (ADR-0262
// Decision 5), any other failure StartFailed.
func startReason(err error) string {
	switch {
	case errors.Is(err, volume.ErrCapacity):
		return reasonVolumeCapacity
	case errors.Is(err, volume.ErrBudget):
		return reasonVolumeBudget
	}
	return "StartFailed"
}

// bookStoredVolumes books every stored Function's local volumes at boot (ADR-0262 Decision 5), so an applied volume
// whose leaf no pass created yet keeps its space; one that no longer fits is logged, and its reconcile pass gates it.
func (r *Reconciler) bookStoredVolumes(ctx context.Context) error {
	l, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "function.ReclaimVolumes", "list functions")
	}
	for _, o := range l.Items {
		fn, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		for _, v := range fn.Spec.Volumes {
			if v.Type != v1.VolumeLocal {
				continue
			}
			if err := r.volumes.Book(ctx, localID(fn, v.VolumeName()), v.Bytes()); err != nil {
				r.logger.WarnContext(ctx, "a stored volume does not fit at boot", "namespace", fn.Namespace, "function", fn.Name, "volume", v.VolumeName(), "err", err)
			}
		}
	}
	return nil
}
