package workload

import (
	"cmp"
	"context"
	"io"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// VolumeLoader copies, loads, checks and clears a Function's local volume (ADR-0262 Decision 11): Source is its tar,
// read live, and Load refuses Conflict when the volume is not empty. Lost reports a volume its Function's DataMissing
// names (Decision 12), whose content a Restore replaces, and StopWorkers keeps the Function workerless until release,
// while its volume loads (#1206).
type VolumeLoader interface {
	Source(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (io.ReadCloser, error)
	Load(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName, archive io.Reader) error
	IsEmpty(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error)
	Clear(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error
	Lost(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) (bool, error)
	StopWorkers(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (release func(), err error)
}

// Volumes is the kind of a Function's local volume: volume/<function>/<volume>/ holds the volume's tar, which a Restore
// loads into that volume of the Function as, or of the Function itself, while its workers are stopped: only into an
// empty one, in place too, but for a lost one, which is emptied first (#1206). load nil is volumes off.
func Volumes(st store.Store, load VolumeLoader) MemberKind { return volumes{st: st, load: load} }

type volumes struct {
	st   store.Store
	load VolumeLoader
}

func (volumes) Kind() v1.Kind { return v1.KindFunction }

func (k volumes) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	const op = "workload.Write"
	if k.load == nil {
		return MemberFile{}, fault.Unavailablef(op, "member volume %s/%s: volumes are off", m.Name, m.Volume)
	}
	f, err := w.File(ctx, "volume/"+string(m.Name)+"/"+string(m.Volume), func(emit func(snapshot.Record) error) error {
		rc, err := k.load.Source(ctx, ns, m.Name, m.Volume)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "volume %s/%s", m.Name, m.Volume)
		}
		return streamFile(op, "volume "+string(m.Name)+"/"+string(m.Volume), rc, emit)
	})
	if err != nil {
		return MemberFile{}, err
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Volume: m.Volume, Files: []backup.StoreFile{f}}, nil
}

func (volumes) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 4 || seg[0] != "volume" {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.KindFunction, Name: v1.ObjectName(seg[1]), Volume: v1.ObjectName(seg[2])}, 0, true
}

// Destination is the volume of the Function as, or of the member's own, which that Function must declare local.
func (k volumes) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	dest := cmp.Or(rm.As, rm.Name)
	obj, err := k.st.Get(ctx, v1.KindFunction.GVK(), ns, dest)
	if err != nil {
		return nil, err
	}
	f, _ := obj.(*v1.Function)
	if f == nil || !slices.ContainsFunc(f.Spec.Volumes, func(v v1.VolumeEntry) bool { return v.Type == v1.VolumeLocal && v.VolumeName() == rm.Volume }) {
		return nil, fault.NotFoundf("backup.Restore", "function %s declares no local volume %s", dest, rm.Volume)
	}
	d := volumeDestination{load: k.load, ns: ns, fn: dest, volume: rm.Volume}
	if k.load != nil {
		if d.lost, err = k.load.Lost(ctx, ns, dest, rm.Volume); err != nil {
			return nil, err
		}
	}
	return d, nil
}

type volumeDestination struct {
	load       VolumeLoader
	ns         v1.NamespaceName
	fn, volume v1.ObjectName
	lost       bool // its Function's DataMissing names it, so what it holds dates from after the loss (#1206)
}

// Prepare stops the Function's workers until release, so none runs while its volume is emptied and loaded (ADR-0262
// Decisions 11 and 12, #1206); a Function stopped for two volumes stays stopped until both are released.
func (d volumeDestination) Prepare(ctx context.Context) (func(), error) {
	if d.load == nil {
		return none, nil
	}
	return d.load.StopWorkers(ctx, d.ns, d.fn)
}

func (d volumeDestination) IsEmpty(ctx context.Context) (bool, error) {
	if d.load == nil {
		return false, fault.Unavailablef("backup.Restore", "volumes are off: no volume loads")
	}
	if d.lost {
		return true, nil
	}
	return d.load.IsEmpty(ctx, d.ns, d.fn, d.volume)
}

// Load untars the file into the volume: only into an empty one, in place too (ADR-0262 Decision 11), but for a lost
// one, which is emptied first, since what it holds dates from after the loss (#1206).
func (d volumeDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if d.load == nil {
		return fault.Unavailablef("backup.Restore", "volumes are off: no volume loads")
	}
	if d.lost {
		if err := d.load.Clear(ctx, d.ns, d.fn, d.volume); err != nil {
			return err
		}
	}
	return notEmpty(d.load.Load(ctx, d.ns, d.fn, d.volume, &recordReader{next: open(ctx, f.Files[0])}))
}

func (d volumeDestination) Clear(ctx context.Context) error {
	if d.load == nil {
		return nil
	}
	return d.load.Clear(ctx, d.ns, d.fn, d.volume)
}
