package v1alpha1

import (
	"fmt"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// VolumeType is the kind of a Function's volume (ADR-0262 Decision 1).
type VolumeType string

const (
	// VolumeEmptyDir is one directory per worker, removed with it.
	VolumeEmptyDir VolumeType = "emptyDir"
	// VolumeLocal is a durable directory owned by its Function like a store.
	VolumeLocal VolumeType = "local"
)

// Schema renders the huma enum for VolumeType.
func (VolumeType) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(VolumeEmptyDir), string(VolumeLocal))
}

// VolumeEntry is one volume a Function declares at its root (ADR-0262 Decision 1): by name it owns the volume, by ref
// it adopts a retained local volume no live Function holds.
type VolumeEntry struct {
	Name ObjectName `json:"name,omitempty" doc:"The volume's name, unique in the list; set name or ref, not both. The code reads the directory's path from FUNCD_VOLUME_<NAME>, the name or ref upper-cased."`
	Ref  ObjectName `json:"ref,omitempty" doc:"Instead of name: a retained local volume that this Function, or an earlier Function of the same name, dropped with retain, and that the Function takes over; with no such volume, it waits in Pending with VolumeNotFound. Only a local volume takes a ref; it sets no deletion and states the retained volume's own size, since apply refuses another size."`
	Type VolumeType `json:"type" doc:"emptyDir is a new empty directory for each worker, removed with it; local is one durable directory that keeps its files through a crash, a redeploy and a scale to zero. A Function with a local volume runs at most 1 replica, and while it redeploys, its old and new workers hold the volume at once."`
	// Size and SizeBytes are the volume's size, exactly one of them set (ADR-0266): "2GiB", or a byte count.
	Size      ByteSize       `json:"size,omitempty" doc:"The volume's size, the most its directory may hold, above zero; set size or sizeBytes, not both. Apply refuses a local volume whose size changes, or that does not fit both the capacity that volumes.capacity or volumes.capacityBytes sets and the namespace's spec.volumeBudget."`
	SizeBytes int64          `json:"sizeBytes,omitempty" minimum:"1" doc:"The same size as a number of bytes. Set size or sizeBytes, not both."`
	Deletion  DeletionPolicy `json:"deletion,omitempty" doc:"Only for a local volume declared by name: retain, the default, keeps the volume and its files with no owner when the Function drops it or is deleted, so that a ref can take it again; delete removes them."`
}

// Bytes is the entry's size in bytes, from whichever form is set (0 when neither is valid).
func (e VolumeEntry) Bytes() int64 {
	if e.Size != "" {
		n, _ := e.Size.Bytes()
		return n
	}
	return e.SizeBytes
}

// VolumeName is the entry's volume, by name or by ref.
func (e VolumeEntry) VolumeName() ObjectName {
	if e.Ref != "" {
		return e.Ref
	}
	return e.Name
}

// VolumeEnv is the env var a worker's code reads a volume's path from (ADR-0262 Decision 2): the name upper-cased.
func VolumeEnv(name ObjectName) string { return "FUNCD_VOLUME_" + strings.ToUpper(string(name)) }

// HasLocalVolume reports whether s declares a local volume.
func (s *FunctionSpec) HasLocalVolume() bool {
	for _, v := range s.Volumes {
		if v.Type == VolumeLocal {
			return true
		}
	}
	return false
}

// validateVolumes checks spec.volumes (ADR-0262 Decisions 1 and 6): exactly one of name and ref, a DNS label unique in
// the list, a known type, a positive size, deletion only on a local volume declared by name, and at most one replica
// with a local volume.
func (s *FunctionSpec) validateVolumes(op string) error {
	seen := make(map[ObjectName]bool, len(s.Volumes))
	for i, v := range s.Volumes {
		path := fmt.Sprintf("spec.volumes[%d]", i)
		if (v.Name == "") == (v.Ref == "") {
			return fault.Invalidf(op, "%s sets %s: want exactly one of name and ref", path, setNames(v.Name != "", v.Ref != ""))
		}
		n := v.VolumeName()
		if !dnsLabel.MatchString(string(n)) {
			return fault.Invalidf(op, "%s %q is not a valid DNS-1123 label", path, n)
		}
		if seen[n] {
			return fault.Invalidf(op, "%s repeats volume %q", path, n)
		}
		seen[n] = true
		switch v.Type {
		case VolumeEmptyDir:
			if v.Ref != "" {
				return fault.Invalidf(op, "%s.ref: an emptyDir volume is declared by name", path)
			}
			if v.Deletion != "" {
				return fault.Invalidf(op, "%s.deletion: an emptyDir volume goes with its worker", path)
			}
		case VolumeLocal:
			if v.Ref != "" && v.Deletion != "" {
				return fault.Invalidf(op, "%s.deletion: a ref volume takes no deletion", path)
			}
			if v.Deletion != "" && v.Deletion != DeletionRetain && v.Deletion != DeletionDelete {
				return fault.Invalidf(op, "%s.deletion %q is neither retain nor delete", path, v.Deletion)
			}
		default:
			return fault.Invalidf(op, "%s.type %q is neither emptyDir nor local", path, v.Type)
		}
		switch {
		case (v.Size == "") == (v.SizeBytes == 0):
			return fault.Invalidf(op, "%s sets %s: want exactly one of size and sizeBytes", path, sizeForms(v))
		case v.Size != "":
			n, err := v.Size.Bytes()
			if err != nil {
				return fault.Wrapf(err, fault.Invalid, op, "%s.size", path)
			}
			if n == 0 {
				return fault.Invalidf(op, "%s.size %q: want a size above 0", path, v.Size)
			}
		case v.SizeBytes < 0:
			return fault.Invalidf(op, "%s.sizeBytes %d: want a size above 0", path, v.SizeBytes)
		}
	}
	// The reconciler runs max(spec.replicas, spec.scaling.minReplicas) workers, so both are held to one.
	if i := slices.IndexFunc(s.Volumes, func(v VolumeEntry) bool { return v.Type == VolumeLocal }); i >= 0 {
		switch {
		case s.Replicas > 1:
			return fault.Invalidf(op, "spec.replicas %d with spec.volumes[%d], a local volume: a local volume allows at most 1 replica", s.Replicas, i)
		case s.Scaling.MinReplicas > 1:
			return fault.Invalidf(op, "spec.scaling.minReplicas %d with spec.volumes[%d], a local volume: a local volume allows at most 1 replica", s.Scaling.MinReplicas, i)
		}
	}
	return nil
}

func sizeForms(v VolumeEntry) string {
	if v.Size != "" {
		return "both size and sizeBytes"
	}
	return "neither size nor sizeBytes"
}

func setNames(name, ref bool) string {
	switch {
	case name && ref:
		return "both name and ref"
	default:
		return "neither name nor ref"
	}
}
