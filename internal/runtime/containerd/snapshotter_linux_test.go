//go:build linux

package containerd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
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
func fakeClient(t *testing.T, cs content.Store, img images.Image, ctrs containers.Store, snaps map[string]snapshots.Snapshotter) *containerd.Client {
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

// Issue 456: an image already in the function's namespace but not unpacked into the configured snapshotter (the
// snapshotter changed, or the unpack after an import failed) must be unpacked before Create prepares its snapshot.
func TestIssue456_CreateUnpacksPresentImage(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "issue456")
	cs, err := local.NewLabeledStore(t.TempDir(), memLabels{})
	require.NoError(t, err)
	layer := writeBlob(t, cs, ocispec.MediaTypeImageLayer, []byte("layer"))
	cfgJSON, err := json.Marshal(ocispec.Image{
		Platform: ocispec.Platform{OS: "linux"},
		RootFS: ocispec.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{layer.Digest},
		},
	})
	require.NoError(t, err)
	manifestJSON, err := json.Marshal(ocispec.Manifest{
		Versioned: ocispecs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    writeBlob(t, cs, ocispec.MediaTypeImageConfig, cfgJSON),
		Layers:    []ocispec.Descriptor{layer},
	})
	require.NoError(t, err)
	manifest := writeBlob(t, cs, ocispec.MediaTypeImageManifest, manifestJSON)

	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "issue456",
		Revision:  "issue456-1",
		Image:     "funcd/issue456:latest",
		LogPath:   filepath.Join(t.TempDir(), "worker.log"),
	}
	rootfs := t.TempDir()
	overlay := &memSnapshotter{rootfs: rootfs, keys: map[string]bool{layer.Digest.String(): true}}
	native := &memSnapshotter{rootfs: rootfs, keys: map[string]bool{}}
	client, err := containerd.New("", containerd.WithServices(
		containerd.WithContentStore(cs),
		containerd.WithImageStore(oneImage{img: images.Image{Name: spec.Image, Target: manifest}}),
		containerd.WithContainerStore(&memContainers{records: map[string]containers.Container{}}),
		containerd.WithSnapshotters(map[string]snapshots.Snapshotter{"overlayfs": overlay, "native": native}),
		containerd.WithDiffService(uncompressedApplier{}),
		containerd.WithNamespaceService(noNamespaceLabels{}),
		containerd.WithIntrospectionService(anySnapshotPlugin{}),
		containerd.WithTaskClient(createdTasks{}),
	))
	require.NoError(t, err)
	d := &driver{cfg: Config{Snapshotter: "native"}, client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	_, err = d.Create(ctx, spec)
	require.NoError(t, err, "the image is present but its layers are only in another snapshotter, so Create must unpack it first")
	require.True(t, native.keys[layer.Digest.String()], "the layer must be unpacked into the configured snapshotter")
}

// Issue 493: containerd imports a curated tar under its normalized name (docker.io/funcd/runtime-<rt>:latest), so a
// later Create of the short curated ref must find that image instead of importing the embedded tar again.
func TestIssue493_CreateFindsImportedCuratedImage(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "issue493")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "issue493",
		Revision:  "issue493-1",
		Image:     "funcd/runtime-nodejs22:latest",
		LogPath:   filepath.Join(t.TempDir(), "worker.log"),
	}
	overlay := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: "docker.io/" + spec.Image, Target: manifest},
		&memContainers{records: map[string]containers.Container{}}, map[string]snapshots.Snapshotter{"overlayfs": overlay})
	d := &driver{client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.NoError(t, err, "the curated image is already imported under its normalized name, so Create must use it, not import the embedded tar again")
}

// A runtime image under the default prefix (funcd/runtime-<rt>:latest) with no curated embed must be pulled from
// Docker Hub. containerd's resolver reads a ref's first path element as the registry host, so resolveImage must pull
// the normalized name. Found while judging draft ADR-0149.
func TestResolveImage_PullsShortRefFromDockerHub(t *testing.T) {
	ctx, cancel := context.WithCancel(leases.WithLease(context.Background(), "pull"))
	cancel() // the resolver builds the registry request and fails before dialing, so no network is needed
	cs, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	client := fakeClient(t, cs, images.Image{}, &memContainers{records: map[string]containers.Container{}},
		map[string]snapshots.Snapshotter{"overlayfs": &memSnapshotter{keys: map[string]bool{}}})
	d := &driver{client: client, instances: map[runtime.InstanceID]*worker{}}

	_, err = d.resolveImage(ctx, "create", "funcd/runtime-deno:latest")
	var uerr *url.Error
	require.ErrorAs(t, err, &uerr, "the pull must reach the registry request")
	u, err := url.Parse(uerr.URL)
	require.NoError(t, err)
	require.Equal(t, "registry-1.docker.io", u.Host, "a short ref names a Docker Hub repository, not a registry host")
	require.Equal(t, "/v2/funcd/runtime-deno/manifests/latest", u.Path)
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
	if parent != "" && !s.keys[parent] {
		return nil, fmt.Errorf("parent snapshot %s does not exist: %w", parent, errdefs.ErrNotFound)
	}
	if s.keys[key] {
		return nil, fmt.Errorf("snapshot %s: %w", key, errdefs.ErrAlreadyExists)
	}
	s.keys[key] = true
	return s.Mounts(ctx, key)
}

func (s *memSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	if !s.keys[key] {
		return snapshots.Info{}, fmt.Errorf("snapshot %s: %w", key, errdefs.ErrNotFound)
	}
	return snapshots.Info{Name: key, Kind: snapshots.KindCommitted}, nil
}

func (s *memSnapshotter) Commit(_ context.Context, name, key string, _ ...snapshots.Opt) error {
	if !s.keys[key] {
		return fmt.Errorf("snapshot %s: %w", key, errdefs.ErrNotFound)
	}
	delete(s.keys, key)
	s.keys[name] = true
	return nil
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

// memLabels keeps content labels in memory, so the content store accepts the labels Unpack writes.
type memLabels map[digest.Digest]map[string]string

func (l memLabels) Get(d digest.Digest) (map[string]string, error) { return l[d], nil }

func (l memLabels) Set(d digest.Digest, labels map[string]string) error {
	l[d] = labels
	return nil
}

func (l memLabels) Update(d digest.Digest, update map[string]string) (map[string]string, error) {
	if l[d] == nil {
		l[d] = map[string]string{}
	}
	for k, v := range update {
		if v == "" {
			delete(l[d], k)
		} else {
			l[d][k] = v
		}
	}
	return l[d], nil
}

// uncompressedApplier applies an uncompressed layer, whose diff ID is its blob digest.
type uncompressedApplier struct{ containerd.DiffService }

func (uncompressedApplier) Apply(_ context.Context, desc ocispec.Descriptor, _ []mount.Mount, _ ...diff.ApplyOpt) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayer, Digest: desc.Digest, Size: desc.Size}, nil
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

// Get matches names exactly, like containerd's image store.
func (s oneImage) Get(_ context.Context, name string) (images.Image, error) {
	if name != s.img.Name {
		return images.Image{}, fmt.Errorf("image %q: %w", name, errdefs.ErrNotFound)
	}
	return s.img, nil
}

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
