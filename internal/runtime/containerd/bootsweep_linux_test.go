//go:build linux

package containerd

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/ctrmanager"
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

// scenario: containerd-images-cleared-at-boot — on the private containerd the boot sweep deletes what an earlier run
// left in a funcd-<ns> namespace: its container, every image with a synchronous delete, the namespace, then its
// boot-dir parent (ADR-0186, issue #729). A namespace funcd does not own is left alone.
func TestScenarioContainerdImagesClearedAtBoot(t *testing.T) {
	imgs := &recordingImages{}
	nss := &recordingNamespaces{names: []string{"funcd-x", "other"}}
	f := newBootFixture(t, imgs, nss)
	imgs.put("funcd-x", images.Image{Name: f.image, Target: f.manifest})
	imgs.put("other", images.Image{Name: f.image, Target: f.manifest})
	f.create(t, f.driver(true), "x", "deleted")
	parent := filepath.Join(f.bootRoot, "funcd-x")
	require.DirExists(t, parent)

	require.NoError(t, f.driver(true).BootSweep(context.Background()))
	require.Empty(t, f.ctrs.records, "the leftover container is removed")
	require.Equal(t, []deletedImage{{namespace: "funcd-x", name: f.image, synchronous: true}}, imgs.deleted,
		"the image is deleted synchronously, so its layers are gone before the namespace delete")
	require.Equal(t, []string{"funcd-x"}, nss.deleted)
	require.NoDirExists(t, parent)
	require.Equal(t, []string{"other"}, nss.names)
	require.Len(t, imgs.byNS["other"], 1, "the namespace funcd does not own keeps its image")
}

// scenario: override-change-leaves-one-image — the boot sweep deletes the ref an earlier run pulled for an
// imageOverride, so after the operator changed the override the first Create pulls the new ref (ADR-0186).
func TestScenarioOverrideChangeLeavesOneImage(t *testing.T) {
	const refV1, refV2 = "ghcr.io/example/runtime-deno:1", "ghcr.io/example/runtime-deno:2"
	imgs := &recordingImages{}
	pulls := &leaseCounter{}
	f := newBootFixture(t, imgs, &recordingNamespaces{names: []string{"funcd-default"}}, containerd.WithLeasesService(pulls))
	imgs.put("funcd-default", images.Image{Name: refV1, Target: f.manifest})
	mapping := ctrmanager.Config{ImageOverride: map[string]string{"deno": refV2}}
	d := f.driver(true)
	d.cfg.Pullable = mapping.Pullable(config.DefaultImagePrefix)

	require.NoError(t, d.BootSweep(context.Background()))
	require.Equal(t, []deletedImage{{namespace: "funcd-default", name: refV1, synchronous: true}}, imgs.deleted)
	_, err := d.Create(context.Background(), runtime.WorkerSpec{
		Namespace: "default",
		Name:      "deno",
		Revision:  "deno-1",
		Image:     mapping.ImageFor(config.DefaultImagePrefix)("deno"),
		OwnerKind: v1alpha1.KindFunction,
	})
	require.ErrorIs(t, err, errPullRecorded)
	require.Equal(t, 1, pulls.n, "the first Create pulls the new override ref")
}

// scenario: curated-image-from-running-binary — the boot sweep deletes the curated image an earlier binary imported,
// so the first Create finds none and imports the tar embedded in the running binary (ADR-0186).
func TestScenarioCuratedImageFromRunningBinary(t *testing.T) {
	const curated = "docker.io/funcd/runtime-nodejs22:latest"
	imgs := &recordingImages{}
	f := newBootFixture(t, imgs, &recordingNamespaces{names: []string{"funcd-default"}})
	imgs.put("funcd-default", images.Image{Name: curated, Target: f.manifest})

	require.NoError(t, f.driver(true).BootSweep(context.Background()))
	require.Equal(t, []deletedImage{{namespace: "funcd-default", name: curated, synchronous: true}}, imgs.deleted)
	_, err := f.client.GetImage(namespaces.WithNamespace(context.Background(), "funcd-default"), curated)
	require.True(t, errdefs.IsNotFound(err), "resolveImage imports the embedded tar only when the lookup finds no image: %v", err)
}

// scenario: external-containerd-keeps-images — on an external containerd the boot sweep removes a leftover container
// and deletes no image and no namespace, which may be another owner's (ADR-0055, ADR-0186).
func TestScenarioExternalContainerdKeepsImages(t *testing.T) {
	imgs := &recordingImages{}
	nss := &recordingNamespaces{names: []string{"funcd-x"}}
	f := newBootFixture(t, imgs, nss)
	imgs.put("funcd-x", images.Image{Name: f.image, Target: f.manifest})
	f.create(t, f.driver(false), "x", "leftover")

	require.NoError(t, f.driver(false).BootSweep(context.Background()))
	require.Empty(t, f.ctrs.records, "the leftover container is removed")
	require.Empty(t, imgs.deleted)
	require.Empty(t, nss.deleted)
	require.DirExists(t, filepath.Join(f.bootRoot, "funcd-x"))
}

// scenario: namespace-not-empty-retried-next-boot — containerd refuses to delete a namespace that still holds
// something, such as a lease an interrupted import left: the boot sweep reports it and keeps the boot-dir parent, and
// the next boot deletes both (ADR-0186 Decision 5).
func TestScenarioNamespaceNotEmptyRetriedNextBoot(t *testing.T) {
	imgs := &recordingImages{}
	nss := &recordingNamespaces{
		names:     []string{"funcd-x"},
		deleteErr: fmt.Errorf("namespace funcd-x must be empty: %w", errdefs.ErrFailedPrecondition),
	}
	f := newBootFixture(t, imgs, nss)
	imgs.put("funcd-x", images.Image{Name: f.image, Target: f.manifest})
	f.create(t, f.driver(true), "x", "leftover")
	parent := filepath.Join(f.bootRoot, "funcd-x")

	require.ErrorIs(t, f.driver(true).BootSweep(context.Background()), errdefs.ErrFailedPrecondition)
	require.Len(t, imgs.deleted, 1)
	require.Empty(t, imgs.byNS["funcd-x"], "the images are deleted")
	require.DirExists(t, parent, "the boot-dir parent stays while the namespace does")

	nss.deleteErr = nil
	require.NoError(t, f.driver(true).BootSweep(context.Background()))
	require.Equal(t, []string{"funcd-x", "funcd-x"}, nss.deleted, "the next boot tries again")
	require.NoDirExists(t, parent)
}

// scenario: shutdown-keeps-images — a clean shutdown on the private containerd removes the containers and deletes no
// image and no namespace: only the boot sweep does (ADR-0186 Decision 1).
func TestScenarioShutdownKeepsImages(t *testing.T) {
	imgs := &recordingImages{}
	nss := &recordingNamespaces{names: []string{"funcd-x"}}
	f := newBootFixture(t, imgs, nss)
	imgs.put("funcd-x", images.Image{Name: f.image, Target: f.manifest})
	d := f.driver(true)
	f.create(t, d, "x", "running")

	require.NoError(t, d.Close())
	require.Empty(t, f.ctrs.records)
	require.Empty(t, imgs.deleted)
	require.Empty(t, nss.deleted)
}

// The boot sweep deletes no image while a container it could not remove may still use it (ADR-0186 Decision 2).
func TestBootSweep_ContainerSweepErrorDeletesNoImage(t *testing.T) {
	imgs := &recordingImages{}
	nss := &recordingNamespaces{names: []string{"funcd-x"}}
	f := newBootFixture(t, imgs, nss)
	imgs.put("funcd-x", images.Image{Name: f.image, Target: f.manifest})
	f.create(t, f.driver(true), "x", "stuck")
	f.ctrs.deleteErr = errdefs.ErrUnavailable

	require.Error(t, f.driver(true).BootSweep(context.Background()))
	require.Empty(t, imgs.deleted)
	require.Empty(t, nss.deleted)
}

// newBootFixture is the close fixture over the images and namespaces the test seeds and inspects.
func newBootFixture(t *testing.T, imgs *recordingImages, nss *recordingNamespaces, extra ...containerd.ServicesOpt) *closeFixture {
	return closeFixtureWith(t, append([]containerd.ServicesOpt{containerd.WithImageStore(imgs), containerd.WithNamespaceService(nss)}, extra...)...)
}

// recordingImages keeps images per containerd namespace and records every Delete with whether it was synchronous.
type recordingImages struct {
	images.Store
	mu      sync.Mutex
	byNS    map[string]map[string]images.Image
	deleted []deletedImage
}

type deletedImage struct {
	namespace   string
	name        string
	synchronous bool
}

func (s *recordingImages) put(ns string, img images.Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byNS == nil {
		s.byNS = map[string]map[string]images.Image{}
	}
	if s.byNS[ns] == nil {
		s.byNS[ns] = map[string]images.Image{}
	}
	s.byNS[ns][img.Name] = img
}

func (s *recordingImages) Get(ctx context.Context, name string) (images.Image, error) {
	ns, _ := namespaces.Namespace(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.byNS[ns][name]
	if !ok {
		return images.Image{}, fmt.Errorf("image %q: %w", name, errdefs.ErrNotFound)
	}
	return img, nil
}

func (s *recordingImages) List(ctx context.Context, _ ...string) ([]images.Image, error) {
	ns, _ := namespaces.Namespace(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Values(s.byNS[ns])), nil
}

func (s *recordingImages) Delete(ctx context.Context, name string, opts ...images.DeleteOpt) error {
	var o images.DeleteOptions
	for _, opt := range opts {
		if err := opt(ctx, &o); err != nil {
			return err
		}
	}
	ns, _ := namespaces.Namespace(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, deletedImage{namespace: ns, name: name, synchronous: o.Synchronous})
	if _, ok := s.byNS[ns][name]; !ok {
		return fmt.Errorf("image %q: %w", name, errdefs.ErrNotFound)
	}
	delete(s.byNS[ns], name)
	return nil
}

// recordingNamespaces lists the containerd namespaces the test holds and records every Delete, which fails with
// deleteErr when it is set.
type recordingNamespaces struct {
	noNamespaceLabels
	mu        sync.Mutex
	names     []string
	deleted   []string
	deleteErr error
}

func (n *recordingNamespaces) List(context.Context) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.names), nil
}

func (n *recordingNamespaces) Delete(_ context.Context, name string, _ ...namespaces.DeleteOpts) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deleted = append(n.deleted, name)
	if n.deleteErr != nil {
		return n.deleteErr
	}
	n.names = slices.DeleteFunc(n.names, func(s string) bool { return s == name })
	return nil
}
