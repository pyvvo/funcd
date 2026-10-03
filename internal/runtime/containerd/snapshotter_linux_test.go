//go:build linux

package containerd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/introspection"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	gocni "github.com/containerd/go-cni"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/pyvvo/funcd/internal/runtime"
)

// Issue 370: resolveImage unpacks the image into the configured snapshotter, so Create must prepare the worker's
// snapshot there, and remove a leftover one from there, not from containerd's default snapshotter.
func TestIssue370_CreateUsesConfiguredSnapshotter(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "issue370")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "issue370",
		Revision:  "issue370-1",
		Image:     "funcd/issue370:latest",
		LogPath:   filepath.Join(t.TempDir(), "worker.log"),
	}
	ctrID, _ := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	rootfs := t.TempDir()
	overlay := &memSnapshotter{rootfs: rootfs, keys: map[string]bool{}}
	native := &memSnapshotter{rootfs: rootfs, keys: map[string]bool{layer.String(): true, ctrID + "-snap": true}}
	ctrs := &memContainers{records: map[string]containers.Container{}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": overlay, "native": native})
	d := &driver{cfg: Config{Snapshotter: "native"}, client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.NoError(t, err, "the image layers are in the configured snapshotter, so the worker's snapshot must be prepared there")
	require.Equal(t, "native", ctrs.records[ctrID].Snapshotter)
	require.Empty(t, overlay.keys)
}

// fakeImage writes a one-layer image to a new content store and returns the store, the layer (the parent snapshot a
// worker's snapshot is prepared from) and the image manifest.
func fakeImage(t *testing.T) (content.Store, digest.Digest, ocispec.Descriptor) {
	t.Helper()
	cs, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	layer := writeBlob(t, cs, ocispec.MediaTypeImageLayer, []byte("layer")).Digest
	cfgJSON, err := json.Marshal(ocispec.Image{
		Platform: ocispec.Platform{OS: "linux"},
		RootFS: ocispec.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{layer},
		},
	})
	require.NoError(t, err)
	manifestJSON, err := json.Marshal(ocispec.Manifest{
		Versioned: ocispecs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    writeBlob(t, cs, ocispec.MediaTypeImageConfig, cfgJSON),
	})
	require.NoError(t, err)
	return cs, layer, writeBlob(t, cs, ocispec.MediaTypeImageManifest, manifestJSON)
}

// fakeClient is a containerd client over in-memory services: it creates containers and tasks but runs nothing.
func fakeClient(t *testing.T, cs content.Store, img images.Image, ctrs *memContainers, snaps map[string]snapshots.Snapshotter) *containerd.Client {
	t.Helper()
	client, err := containerd.New("", containerd.WithServices(
		containerd.WithContentStore(cs),
		containerd.WithImageStore(oneImage{img: img}),
		containerd.WithContainerStore(ctrs),
		containerd.WithSnapshotters(snaps),
		containerd.WithNamespaceService(noNamespaceLabels{}),
		containerd.WithIntrospectionService(anySnapshotPlugin{}),
		containerd.WithTaskClient(createdTasks{}),
	))
	require.NoError(t, err)
	return client
}

func writeBlob(t *testing.T, cs content.Store, mediaType string, b []byte) ocispec.Descriptor {
	t.Helper()
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(b), Size: int64(len(b))}
	require.NoError(t, content.WriteBlob(context.Background(), cs, desc.Digest.String(), bytes.NewReader(b), desc))
	return desc
}

// memSnapshotter keeps snapshot keys in memory and fails like containerd: Prepare needs the parent and a free key.
type memSnapshotter struct {
	snapshots.Snapshotter
	rootfs string
	keys   map[string]bool
}

func (s *memSnapshotter) Prepare(ctx context.Context, key, parent string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	if !s.keys[parent] {
		return nil, fmt.Errorf("parent snapshot %s does not exist: %w", parent, errdefs.ErrNotFound)
	}
	if s.keys[key] {
		return nil, fmt.Errorf("snapshot %s: %w", key, errdefs.ErrAlreadyExists)
	}
	s.keys[key] = true
	return s.Mounts(ctx, key)
}

func (s *memSnapshotter) Mounts(context.Context, string) ([]mount.Mount, error) {
	return []mount.Mount{{Type: "bind", Source: s.rootfs, Options: []string{"rbind", "ro"}}}, nil
}

func (s *memSnapshotter) Remove(_ context.Context, key string) error {
	if !s.keys[key] {
		return fmt.Errorf("snapshot %s: %w", key, errdefs.ErrNotFound)
	}
	delete(s.keys, key)
	return nil
}

type memContainers struct {
	containers.Store
	records map[string]containers.Container
}

func (s *memContainers) Get(_ context.Context, id string) (containers.Container, error) {
	c, ok := s.records[id]
	if !ok {
		return containers.Container{}, fmt.Errorf("container %q: %w", id, errdefs.ErrNotFound)
	}
	return c, nil
}

func (s *memContainers) Create(_ context.Context, c containers.Container) (containers.Container, error) {
	s.records[c.ID] = c
	return c, nil
}

type oneImage struct {
	images.Store
	img images.Image
}

func (s oneImage) Get(context.Context, string) (images.Image, error) { return s.img, nil }

type noNamespaceLabels struct{ namespaces.Store }

func (noNamespaceLabels) Labels(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

type anySnapshotPlugin struct{ introspection.Service }

func (anySnapshotPlugin) Plugins(context.Context, ...string) (*introspectionapi.PluginsResponse, error) {
	return &introspectionapi.PluginsResponse{Plugins: []*introspectionapi.Plugin{{}}}, nil
}

type createdTasks struct{ tasksapi.TasksClient }

func (createdTasks) Create(context.Context, *tasksapi.CreateTaskRequest, ...grpc.CallOption) (*tasksapi.CreateTaskResponse, error) {
	return &tasksapi.CreateTaskResponse{Pid: 1}, nil
}

type attachedCNI struct{ gocni.CNI }

func (attachedCNI) Setup(context.Context, string, string, ...gocni.NamespaceOpts) (*gocni.Result, error) {
	return &gocni.Result{}, nil
}
