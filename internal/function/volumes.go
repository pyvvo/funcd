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
		if _, err := r.volumes.Reserve(ctx, id, fn.UID, v.SizeBytes, deletion); err != nil {
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

// workerVolumes reserves a new emptyDir of each emptyDir volume for the worker replica of fn is about to create,
// releasing the ones the worker it replaces held, and returns every volume's mount and env value (ADR-0262 Decisions 2
// and 3). A local volume is reserved again, idempotently, for its path.
func (r *Reconciler) workerVolumes(ctx context.Context, fn *v1.Function, id runtime.InstanceID) ([]runtime.Mount, map[string]string, error) {
	if len(fn.Spec.Volumes) == 0 {
		return nil, nil, nil
	}
	if r.volumes == nil {
		return nil, nil, fault.Invalidf("function.workerVolumes", "spec.volumes: volumes are off on this daemon")
	}
	if err := r.releaseEmptyDirs(ctx, id); err != nil {
		return nil, nil, err
	}
	token, err := workerToken()
	if err != nil {
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
			host, err = r.volumes.Reserve(ctx, vid, "", v.SizeBytes, "")
			if err == nil {
				empty = append(empty, vid)
			}
		} else {
			host, err = r.volumes.Reserve(ctx, vid, fn.UID, v.SizeBytes, cmp.Or(v.Deletion, v1.DeletionRetain))
		}
		if err != nil {
			r.dropEmptyDirs(ctx, empty)
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

// sweepEmptyDirs releases the emptyDirs of fn's workers that ended or are gone: an ended worker is replaced, never
// started again, so its emptyDir is never read again (ADR-0262 Decision 1).
func (r *Reconciler) sweepEmptyDirs(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, insts []runtime.Instance) error {
	if r.volumes == nil {
		return nil
	}
	live := make(map[runtime.InstanceID]bool, len(insts))
	for _, in := range insts {
		if !in.State.Terminal() {
			live[in.ID] = true
		}
	}
	r.volMu.Lock()
	var gone []runtime.InstanceID
	for id := range r.emptyDirs {
		if strings.HasPrefix(string(id), string(ns)+"/"+string(name)+"/") && !live[id] {
			gone = append(gone, id)
		}
	}
	r.volMu.Unlock()
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
	r.volMu.Unlock()
	return nil
}

func (r *Reconciler) platformHeld() bool { return r.hold != nil && r.hold.Held() }

// ReclaimVolumes runs at boot before the controller (ADR-0262 Decision 8): no worker survives a restart, so every
// emptyDir left goes, and the local volumes of Functions no longer stored, or stored under another UID, follow their
// deletion policy. A no-op while the platform is held, whose hold status lists them (VolumeOrphans).
func (r *Reconciler) ReclaimVolumes(ctx context.Context) error {
	if r.volumes == nil || r.platformHeld() {
		return nil
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
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

// dataMissing sets DataMissing on fn (ADR-0262 Decision 12, as ADR-0234 Decision 6): on this process's first pass,
// with durable volumes, a Function whose stored Ready is True and whose local volume has no leaf lost its data; a
// Succeeded Restore into that volume since then clears it.
func (r *Reconciler) dataMissing(ctx context.Context, fn *v1.Function) error {
	key := fnKey{fn.Namespace, fn.Name}
	r.volMu.Lock()
	first, pending := !r.volumesSeen[key], r.volumesMissing[key]
	r.volMu.Unlock()
	cur, has := fn.Status.Conditions.Get(condDataMissing)
	if pending && has && cur.Status == v1.ConditionTrue {
		r.volMu.Lock()
		delete(r.volumesMissing, key)
		r.volMu.Unlock()
		pending = false
	}
	if pending {
		r.setDataMissing(fn, pending)
		return nil
	}
	if has && cur.Status == v1.ConditionTrue {
		restored, err := r.volumesRestoredSince(ctx, fn, time.Time(cur.LastTransitionTime))
		if err != nil || !restored {
			return err
		}
		fn.Status.Conditions.Set(v1.Condition{Type: condDataMissing, Status: v1.ConditionFalse, Reason: reasonVolumesRestored, ObservedGeneration: fn.Generation})
		return nil
	}
	if !first {
		return nil
	}
	r.volMu.Lock()
	r.volumesSeen[key] = true
	r.volMu.Unlock()
	ready, ok := fn.Status.Conditions.Get(condReady)
	if !r.volumesDurable || !ok || ready.Status != v1.ConditionTrue || !fn.Spec.HasLocalVolume() {
		return nil
	}
	infos, err := r.volumes.List(ctx)
	if err != nil {
		return err
	}
	for _, v := range fn.Spec.Volumes {
		if v.Type != v1.VolumeLocal {
			continue
		}
		id := localID(fn, v.VolumeName())
		if !slices.ContainsFunc(infos, func(in volume.Info) bool { return in.ID == id }) {
			r.volMu.Lock()
			r.volumesMissing[key] = true
			r.volMu.Unlock()
			r.logger.WarnContext(ctx, "function volume lost its data", "namespace", fn.Namespace, "function", fn.Name, "volume", v.VolumeName())
			r.setDataMissing(fn, true)
			return nil
		}
	}
	return nil
}

func (r *Reconciler) setDataMissing(fn *v1.Function, missing bool) {
	if !missing {
		return
	}
	fn.Status.Conditions.Set(v1.Condition{Type: condDataMissing, Status: v1.ConditionTrue, Reason: reasonNoVolume, ObservedGeneration: fn.Generation,
		Message: "the Function was Ready before and a local volume is gone, so it starts empty: run a workload Restore into it"})
}

// volumesRestoredSince reports a Succeeded Restore created at or after since whose member is one of fn's local
// volumes, restored into fn.
func (r *Reconciler) volumesRestoredSince(ctx context.Context, fn *v1.Function, since time.Time) (bool, error) {
	l, err := r.store.List(ctx, v1.KindRestore.GVK(), store.ListOptions{Namespace: fn.Namespace})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "function.dataMissing", "list restores in %q", fn.Namespace)
	}
	for _, o := range l.Items {
		rs, ok := o.(*v1.Restore)
		if !ok || rs.Status.Phase != v1.RunSucceeded || time.Time(rs.CreationTime).Before(since) {
			continue
		}
		for _, m := range rs.Spec.Members {
			if m.Kind == v1.KindFunction && m.Volume != "" && cmp.Or(m.As, m.Name) == fn.Name {
				return true, nil
			}
		}
	}
	return false, nil
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
