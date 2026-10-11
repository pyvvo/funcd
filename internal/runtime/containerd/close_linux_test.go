//go:build linux

package containerd

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	gocni "github.com/containerd/go-cni"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// Close gives every worker the driver runs SIGTERM, and on the private containerd it also stops every worker an earlier
// hard-killed run left behind: a worker left running would keep serving after the daemon stops, outside the egress
// fence Shutdown removes once the runtime is closed (ADR-0115).
func TestClose_StopsEveryWorker(t *testing.T) {
	f := newCloseFixture(t, "funcd-default")
	d := f.driver(true)
	var tracked, ctrIDs, cniIDs []string
	for _, name := range []v1alpha1.ObjectName{"close-a", "close-b"} {
		ctrID, cniID := f.create(t, d, "default", name)
		tracked, ctrIDs, cniIDs = append(tracked, ctrID), append(ctrIDs, ctrID), append(cniIDs, cniID)
	}
	ctrID, cniID := f.create(t, f.driver(true), "default", "leftover") // d started with no instances
	ctrIDs, cniIDs = append(ctrIDs, ctrID), append(cniIDs, cniID)

	require.NoError(t, d.Close())
	tasks := f.client.TaskService().(*createdTasks)
	require.ElementsMatch(t, ctrIDs, tasks.killed, "Close must kill every worker's task")
	require.ElementsMatch(t, ctrIDs, tasks.deleted, "Close must delete every worker's task")
	require.Empty(t, f.ctrs.records, "Close must delete every worker's container")
	require.ElementsMatch(t, cniIDs, f.cni.removed, "Close must remove every worker's network attachment")
	for _, id := range tracked {
		require.Equal(t, uint32(syscall.SIGTERM), tasks.signal[id], "Close must give a worker it runs SIGTERM before any kill")
	}
}

// On an external containerd the funcd namespaces may hold another daemon's or a bench's workers (ADR-0055): Close
// stops the workers its driver runs and leaves every other worker running.
func TestClose_SparesAnotherOwnersWorkers(t *testing.T) {
	f := newCloseFixture(t, "funcd-default", "funcd-bench")
	d := f.driver(false)
	own, _ := f.create(t, d, "default", "own")
	other := f.driver(false)
	daemon, _ := f.create(t, other, "default", "daemon")
	bench, _ := f.create(t, other, "bench", "bench")

	require.NoError(t, d.Close())
	require.Equal(t, []string{own}, f.client.TaskService().(*createdTasks).killed, "Close must stop only its own workers")
	require.Contains(t, f.ctrs.records, daemon, "Close removed another daemon's worker")
	require.Contains(t, f.ctrs.records, bench, "Close removed a bench's worker")
}

// Close reports a worker it could not remove, its own or one an earlier run left behind, so Shutdown keeps the egress
// fence while the worker may still run.
func TestClose_ReportsWorkerItCouldNotRemove(t *testing.T) {
	own := newCloseFixture(t, "funcd-default")
	d := own.driver(false)
	own.create(t, d, "default", "own")
	own.ctrs.deleteErr = errdefs.ErrUnavailable
	require.Error(t, d.Close(), "Close must report a worker of its own it could not remove")
	requireNoneReleased(t, d)

	left := newCloseFixture(t, "funcd-default")
	left.create(t, left.driver(true), "default", "leftover")
	left.ctrs.deleteErr = errdefs.ErrUnavailable
	require.Error(t, left.driver(true).Close(), "Close must report a leftover worker it could not remove")
}

// A transient task-service error does not show a worker stopped: Close reports the worker, its own or one an earlier
// run left behind, without deleting its container, and keeps its own worker so a later Close still stops it.
func TestClose_ReportsWorkerWhoseTaskItCouldNotLoad(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "task service unavailable")
	own := newCloseFixture(t, "funcd-default")
	d := own.driver(false)
	ctrID, _ := own.create(t, d, "default", "own")
	tasks := own.client.TaskService().(*createdTasks)
	tasks.getErr = unavailable
	require.Error(t, d.Close(), "Close must report a worker of its own whose task it could not load")
	require.Empty(t, tasks.killed)
	require.Contains(t, own.ctrs.records, ctrID, "Close deleted a worker whose task it could not load")
	requireNoneReleased(t, d)
	tasks.getErr = nil
	require.NoError(t, d.Close())
	require.Equal(t, []string{ctrID}, tasks.killed, "a later Close must stop the worker it kept")

	left := newCloseFixture(t, "funcd-default")
	leftover, _ := left.create(t, left.driver(true), "default", "leftover")
	left.client.TaskService().(*createdTasks).getErr = unavailable
	require.Error(t, left.driver(true).Close(), "Close must report a leftover worker whose task it could not load")
	require.Contains(t, left.ctrs.records, leftover, "Close deleted a leftover worker whose task it could not load")
}

// Once containerd has exited no call can be answered, yet each would first redial the socket containerd removed, which
// the client retries for its connect timeout (10 s). Close fails at once whether containerd exited before Close or
// during it, and it still fails, so Shutdown keeps the egress fence.
func TestClose_FailsFastOnceContainerdExited(t *testing.T) {
	t.Run("before Close", func(t *testing.T) {
		c := startStubContainerd(t, nil)
		_, err := c.client.NamespaceService().List(context.Background())
		require.True(t, errdefs.IsNotImplemented(err), "the client must reach the stub: %v", err)
		c.exit(t)
		d := c.driver()
		done := make(chan error, 1)
		go func() { done <- d.Close() }()
		requireFailsWithinASecond(t, done, time.Now())
		requireNoneReleased(t, d)
	})
	t.Run("during Close", func(t *testing.T) {
		called := make(chan struct{}, 1)
		answer := func(_ any, stream grpc.ServerStream) error { //nolint:forbidigo // grpc.StreamHandler's signature
			select {
			case called <- struct{}{}:
			default:
			}
			<-stream.Context().Done()
			return stream.Context().Err()
		}
		c := startStubContainerd(t, answer)
		d := c.driver()
		done := make(chan error, 1)
		go func() { done <- d.Close() }()
		<-called // Close waits on containerd
		c.exit(t)
		requireFailsWithinASecond(t, done, time.Now())
		requireNoneReleased(t, d)
	})
}

// Close watches for containerd's exit only while it runs: it ends the watcher and waits for it, so Close returns while
// containerd runs and no watcher outlives it.
func TestClose_JoinsItsExitWatcher(t *testing.T) {
	for name, exited := range map[string]chan struct{}{
		"external containerd": nil,
		"private containerd":  make(chan struct{}),
	} {
		t.Run(name, func(t *testing.T) {
			d := newCloseFixture(t).driver(false)
			d.cfg.Exited = exited
			done := make(chan error, 1)
			go func() { done <- d.Close() }()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("Close did not return while containerd runs: its exit watcher never ended")
			}
		})
	}
}

// stubContainerd stands in for the private containerd: a gRPC server on a short unix socket that answers every call
// with handler, or Unimplemented when handler is nil.
type stubContainerd struct {
	socket string
	srv    *grpc.Server
	client *containerd.Client
	exited chan struct{}
}

func startStubContainerd(t *testing.T, handler grpc.StreamHandler) *stubContainerd {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	c := &stubContainerd{socket: filepath.Join(dir, "containerd.sock"), exited: make(chan struct{})}
	ln, err := net.Listen("unix", c.socket)
	require.NoError(t, err)
	var opts []grpc.ServerOption
	if handler != nil {
		opts = append(opts, grpc.UnknownServiceHandler(handler))
	}
	c.srv = grpc.NewServer(opts...)
	go func() { _ = c.srv.Serve(ln) }()
	t.Cleanup(c.srv.Stop)
	c.client, err = containerd.New(c.socket)
	require.NoError(t, err)
	return c
}

// exit stops the stub as containerd exits, closing and removing its socket, then closes exited as the manager does.
func (c *stubContainerd) exit(t *testing.T) {
	t.Helper()
	c.srv.Stop()
	require.NoFileExists(t, c.socket)
	close(c.exited)
}

// driver runs one worker on the stub.
func (c *stubContainerd) driver() *driver {
	id := runtime.NewInstanceID("default", "exited", "exited-1", 0)
	ctrID, cniID := workerNames("default", "exited", "exited-1", "0")
	return &driver{
		cfg: Config{
			Logger:  slog.Default(),
			Private: true,
			Exited:  c.exited,
		},
		client: c.client,
		cni:    &removedCNI{},
		instances: map[runtime.InstanceID]*worker{
			id: {ctrID: ctrID, cniID: cniID, namespace: "default", name: "exited", revision: "exited-1", ownerKind: v1alpha1.KindFunction},
		},
	}
}

// requireFailsWithinASecond waits for Close's error and fails when it came over a second after since.
func requireFailsWithinASecond(t *testing.T, done <-chan error, since time.Time) {
	t.Helper()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "containerd exited", "Close must fail, so Shutdown keeps the egress fence")
	case <-time.After(time.Second):
		err := <-done
		t.Fatalf("Close returned %s after containerd exited: %v", time.Since(since).Round(time.Millisecond), err)
	}
}

// Stop releases a worker only when containerd no longer has its container: a transient error must not release a
// worker that may still run.
func TestStop_KeepsWorkerOnLoadError(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "stop")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "stop",
		Revision:  "stop-1",
		Image:     "funcd/stop:latest",
		OwnerKind: v1alpha1.KindFunction,
	}
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	d := &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: attachedCNI{}, bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}}

	inst, err := d.Create(ctx, spec)
	require.NoError(t, err)
	ctrs.getErr = errdefs.ErrUnavailable
	require.Error(t, d.Stop(ctx, inst.ID))
	require.False(t, d.instances[inst.ID].released, "Stop released a worker whose container it could not load")
}

func requireNoneReleased(t *testing.T, d *driver) {
	t.Helper()
	require.NotEmpty(t, d.instances)
	for id, sb := range d.instances {
		require.False(t, sb.released, "Close released worker %s, which may still run", id)
	}
}

// closeFixture is a fake containerd holding one image and the funcd namespaces it lists.
type closeFixture struct {
	bootRoot string
	fifoDir  string
	client   *containerd.Client
	ctrs     *memContainers
	cni      *removedCNI
	snap     *memSnapshotter
	image    string
	manifest ocispec.Descriptor
}

func newCloseFixture(t *testing.T, namespaces ...string) *closeFixture {
	return closeFixtureWith(t, containerd.WithNamespaceService(listedNamespaces{names: namespaces}))
}

// closeFixtureWith builds the fixture's fake containerd with extra in place of fakeClient's matching defaults.
func closeFixtureWith(t *testing.T, extra ...containerd.ServicesOpt) *closeFixture {
	cs, layer, manifest := fakeImage(t)
	const image = "funcd/close:latest"
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap}, extra...)
	return &closeFixture{client: client, ctrs: ctrs, cni: &removedCNI{}, snap: snap, image: image, manifest: manifest, bootRoot: t.TempDir(), fifoDir: t.TempDir()}
}

// driver starts a driver on the fixture's containerd with no instances, as a daemon or a bench does.
func (f *closeFixture) driver(private bool) *driver {
	return &driver{cfg: Config{Logger: slog.Default(), Private: private}, client: f.client, cni: f.cni, bootRoot: f.bootRoot, fifoDir: f.fifoDir, instances: map[runtime.InstanceID]*worker{}}
}

// create runs a worker through d and returns its container and network attachment ids.
func (f *closeFixture) create(t *testing.T, d *driver, ns v1alpha1.NamespaceName, name v1alpha1.ObjectName) (ctrID, cniID string) {
	spec := runtime.WorkerSpec{
		Namespace: ns,
		Name:      name,
		Revision:  name + "-1",
		Image:     f.image,
		OwnerKind: v1alpha1.KindFunction,
	}
	_, err := d.Create(leases.WithLease(context.Background(), "close"), spec)
	require.NoError(t, err)
	return workerNames(string(ns), string(name), string(spec.Revision), "0")
}

// listedNamespaces lists the containerd namespaces the test holds workers in.
type listedNamespaces struct {
	noNamespaceLabels
	names []string
}

func (n listedNamespaces) List(context.Context) ([]string, error) { return n.names, nil }

// removedCNI attaches every worker and records the id of each Remove; Close removes them concurrently.
type removedCNI struct {
	attachedCNI
	mu      sync.Mutex
	removed []string
}

func (c *removedCNI) Remove(_ context.Context, id, _ string, _ ...gocni.NamespaceOpts) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = append(c.removed, id)
	return nil
}
