//go:build linux

package containerd

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/containerd/containerd/v2/pkg/namespaces"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: containerd-leftover-removed-at-boot — a funcd container an earlier run left in a funcd-<ns> namespace is
// removed by the boot sweep with its snapshot and CNI attachment, on any containerd (ADR-0167 Decision 8); a
// namespace funcd does not own is left alone.
func TestScenarioContainerdLeftoverRemovedAtBoot(t *testing.T) {
	f := newCloseFixture(t, "funcd-default", "funcd-team", "other")
	var ctrID string
	var cniIDs, snaps []string
	for _, w := range []struct {
		ns   v1alpha1.NamespaceName
		name v1alpha1.ObjectName
	}{{"default", "deleted"}, {"team", "deleted-too"}} {
		id, cniID := f.create(t, f.driver(false), w.ns, w.name)
		ctrID = id
		cniIDs = append(cniIDs, cniID)
	}
	for _, c := range f.ctrs.records {
		snaps = append(snaps, c.SnapshotKey)
	}
	require.Len(t, snaps, 2)
	foreign := f.ctrs.records[ctrID]
	foreign.ID, foreign.SnapshotKey = "foreign", "foreign"
	_, err := f.ctrs.Create(namespaces.WithNamespace(context.Background(), "other"), foreign)
	require.NoError(t, err)

	d := f.driver(false)
	require.NoError(t, d.SweepAll(context.Background()))
	require.Equal(t, []string{"foreign"}, slices.Collect(maps.Keys(f.ctrs.records)),
		"the leftover containers are deleted and the container in the namespace funcd does not own is kept")
	require.ElementsMatch(t, cniIDs, f.cni.removed, "their network attachments are removed")
	for _, key := range snaps {
		require.False(t, f.snap.keys[key], "snapshot %s is removed", key)
	}
}
