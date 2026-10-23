//go:build linux

package containerd

import (
	"context"
	"slices"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// scenario: worker-sees-only-its-cgroup (ADR-0264). Non-integration: it applies the SpecOpt to a bare spec, twice, and
// finds the cgroup namespace and the read-only cgroup mount once each, beside what the spec already held. The lane checks
// the view inside a real worker.
func TestScenarioWorkerSeesOnlyItsCgroup(t *testing.T) {
	s := &specs.Spec{
		Linux:  &specs.Linux{Namespaces: []specs.LinuxNamespace{{Type: specs.PIDNamespace}}},
		Mounts: []specs.Mount{{Destination: "/proc", Type: "proc", Source: "proc"}},
	}
	for range 2 {
		if err := withCgroupView(context.Background(), nil, nil, s); err != nil {
			t.Fatalf("withCgroupView: %v", err)
		}
	}
	want := []specs.LinuxNamespace{{Type: specs.PIDNamespace}, {Type: specs.CgroupNamespace}}
	if !slices.Equal(s.Linux.Namespaces, want) {
		t.Errorf("namespaces = %v, want %v", s.Linux.Namespaces, want)
	}
	if len(s.Mounts) != 2 || s.Mounts[0].Destination != "/proc" {
		t.Fatalf("mounts = %v, want /proc then one cgroup mount", s.Mounts)
	}
	m := s.Mounts[1]
	if m.Destination != "/sys/fs/cgroup" || m.Type != "cgroup" || m.Source != "cgroup" ||
		!slices.Equal(m.Options, []string{"ro", "nosuid", "noexec", "nodev", "relatime"}) {
		t.Errorf("cgroup mount = %+v, want cgroup at /sys/fs/cgroup with ro,nosuid,noexec,nodev,relatime", m)
	}

	bare := &specs.Spec{}
	if err := withCgroupView(context.Background(), nil, nil, bare); err != nil {
		t.Fatalf("withCgroupView(no linux section): %v", err)
	}
	if bare.Linux == nil || len(bare.Linux.Namespaces) != 1 || len(bare.Mounts) != 1 {
		t.Errorf("a spec with no linux section gets the namespace and the mount: %+v", bare)
	}
}
