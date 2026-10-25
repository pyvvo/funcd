package function

import (
	"cmp"
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/volume"
)

// volumeStore is the quarantine of the Functions' local volumes (ADR-0288): the volume manager's entries, named by
// their Function and volume. It is a quarantine.Returner.
type volumeStore struct{ r *Reconciler }

// Quarantine is the quarantine of the Functions' local volumes, which also returns them (quarantine.Returner); nil
// while volumes are off.
func (r *Reconciler) Quarantine() quarantine.Store {
	if r.volumes == nil {
		return nil
	}
	return volumeStore{r}
}

func volumeRef(id volume.ID) quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: id.Namespace, Name: id.Function}, Volume: id.Name}
}

func refVolume(op string, ref quarantine.Ref) (volume.ID, error) {
	if ref.Kind != v1.KindFunction || ref.Volume == "" {
		return volume.ID{}, fault.Invalidf(op, "%s: want a Function and its volume", ref)
	}
	return volume.ID{Namespace: ref.Namespace, Function: ref.Name, Name: ref.Volume}, nil
}

func (q volumeStore) Move(ctx context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	id, err := refVolume("function.Quarantine", ref)
	if err != nil {
		return quarantine.Entry{}, err
	}
	since, err := q.r.volumes.Quarantine().Move(ctx, id, now)
	if err != nil {
		return quarantine.Entry{}, err
	}
	return quarantine.Entry{Ref: ref, Since: since, State: quarantine.Kept}, nil
}

// List returns the entries; one with neither data nor state is none.
func (q volumeStore) List(ctx context.Context) ([]quarantine.Entry, error) {
	qs, err := q.r.volumes.Quarantine().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range qs {
		if e.State != "" {
			out = append(out, quarantine.Entry{Ref: volumeRef(e.ID), Since: e.Since, State: e.State})
		}
	}
	return out, nil
}

// Expire removes each entry Expired at cutoff, a RemoveAll whose leaf frees its quota project once no file is charged
// to it, and each entry with neither data nor state; it returns the first.
func (q volumeStore) Expire(ctx context.Context, cutoff time.Time) ([]quarantine.Entry, error) {
	vq := q.r.volumes.Quarantine()
	qs, err := vq.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range qs {
		entry := quarantine.Entry{Ref: volumeRef(e.ID), Since: e.Since, State: e.State}
		expired := entry.Expired(cutoff)
		if !expired && e.State != "" {
			continue
		}
		if err := vq.Remove(ctx, e.ID, e.Since); err != nil {
			return out, err
		}
		if expired {
			out = append(out, entry)
		}
	}
	return out, nil
}

// Return brings ref's entry dated since, the newest when zero, back into the stored Function's volume of that name
// (ADR-0288 Decision 6), with the load a Restore uses (ADR-0262 Decisions 11 and 12): the Function's workers stopped,
// the volume checked empty, the entry written returning, its data untarred into the volume, which charges the files
// to the volume's own quota project, the workers released, then the entry deleted. A returning entry resumes: its
// emptiness check skipped, the partial copy in the volume removed first. NotFound without an entry; Conflict without
// the Function or its local volume of that name, or when that volume is not empty.
func (q volumeStore) Return(ctx context.Context, ref quarantine.Ref, since time.Time) error {
	const op = "function.ReturnVolume"
	r := q.r
	id, err := refVolume(op, ref)
	if err != nil {
		return err
	}
	entries, err := q.List(ctx)
	if err != nil {
		return err
	}
	e, err := quarantine.Pick(entries, ref, since)
	if err != nil {
		return err
	}
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ref.Namespace, ref.Name)
	if fault.KindOf(err) == fault.NotFound {
		return fault.Conflictf(op, "Function %s/%s is not stored: apply it first", ref.Namespace, ref.Name)
	}
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "get function %s/%s", ref.Namespace, ref.Name)
	}
	fn := obj.(*v1.Function)
	var spec *v1.VolumeEntry
	for i, v := range fn.Spec.Volumes {
		if v.Type == v1.VolumeLocal && v.VolumeName() == ref.Volume {
			spec = &fn.Spec.Volumes[i]
		}
	}
	if spec == nil {
		return fault.Conflictf(op, "Function %s/%s declares no local volume %q", ref.Namespace, ref.Name, ref.Volume)
	}
	if _, err := r.volumes.Reserve(ctx, id, fn.UID, spec.Bytes(), cmp.Or(spec.Deletion, v1.DeletionRetain)); err != nil {
		return err
	}
	release, err := r.StopWorkers(ctx, ref.Namespace, ref.Name)
	if err != nil {
		return err
	}
	defer release()
	vq := r.volumes.Quarantine()
	if e.State == quarantine.Returning {
		if err := r.volumes.Clear(ctx, id); err != nil {
			return err
		}
	} else {
		empty, err := r.volumes.Empty(ctx, id)
		if err != nil {
			return err
		}
		if !empty {
			return fault.Conflictf(op, "volume %s/%s/%s is not empty: a return goes into an empty volume", ref.Namespace,
				ref.Name, ref.Volume)
		}
		if err := vq.Mark(ctx, id, e.Since, quarantine.Returning); err != nil {
			return err
		}
	}
	rc, err := vq.Tar(ctx, id, e.Since)
	if err != nil {
		return err
	}
	err = r.volumes.Untar(ctx, id, rc)
	_ = rc.Close()
	if err != nil {
		return err
	}
	release()
	return vq.Remove(ctx, id, e.Since)
}

// reclaimOrphanVolume follows, at boot, the deletion policy of a local volume no stored Function holds: retain keeps
// it unowned; delete moves it into the quarantine instead of releasing it (ADR-0288 Decision 1).
func (r *Reconciler) reclaimOrphanVolume(ctx context.Context, in volume.Info) error {
	if in.Deletion != v1.DeletionDelete {
		return r.volumes.Adopt(ctx, in.ID, "")
	}
	e, err := r.Quarantine().Move(ctx, volumeRef(in.ID), r.clock.Now())
	if fault.KindOf(err) == fault.NotFound {
		// the meta a cut move left with no data behind it: nothing to keep, and the manager dropped it
		return nil
	}
	if err != nil {
		return err
	}
	r.logger.WarnContext(ctx, "moved the data of a Function with no object into the quarantine", "namespace", in.ID.Namespace,
		"name", in.ID.Function, "volume", in.ID.Name, "until", e.Until(r.quarantinePeriod))
	return nil
}
