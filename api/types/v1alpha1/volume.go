package v1alpha1

import (
	"fmt"
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
	Name      ObjectName     `json:"name,omitempty"`
	Ref       ObjectName     `json:"ref,omitempty"`
	Type      VolumeType     `json:"type"`
	SizeBytes int64          `json:"sizeBytes" minimum:"1"`
	Deletion  DeletionPolicy `json:"deletion,omitempty"`
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
		if v.SizeBytes <= 0 {
			return fault.Invalidf(op, "%s.sizeBytes %d: want a size above 0", path, v.SizeBytes)
		}
	}
	if s.Replicas > 1 {
		for i, v := range s.Volumes {
			if v.Type == VolumeLocal {
				return fault.Invalidf(op, "spec.replicas %d with spec.volumes[%d], a local volume: a local volume allows at most 1 replica", s.Replicas, i)
			}
		}
	}
	return nil
}

func setNames(name, ref bool) string {
	switch {
	case name && ref:
		return "both name and ref"
	default:
		return "neither name nor ref"
	}
}
