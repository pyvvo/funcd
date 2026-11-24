package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/runtimecontract"
)

// procEngine is an Engine that runs each container's Cmd as a process of this host, on its network, and keeps each
// volume as a folder: a mount's target that begins an env value or an argument names its volume's folder instead.
// published is what Inspect reports a running container publishes.
type procEngine struct {
	root      string
	published map[string]string

	mu   sync.Mutex
	ctrs map[string]*procCtr
	vols map[string]map[string]string
	runs []RunSpec
}

type procCtr struct {
	labels         map[string]string
	cmd            *exec.Cmd
	done           chan struct{}
	code           int
	stdout, stderr *os.File
	followed       bool
}

var _ Engine = (*procEngine)(nil)

func newProcEngine(t *testing.T) *procEngine {
	t.Helper()
	root, err := os.MkdirTemp("", "peng")
	require.NoError(t, err)
	e := &procEngine{root: root, ctrs: map[string]*procCtr{}, vols: map[string]map[string]string{}}
	t.Cleanup(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, c := range e.ctrs {
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = os.RemoveAll(root)
	})
	return e
}

func (e *procEngine) volume(name string) string { return filepath.Join(e.root, name) }

// local is s, a path in a container with mounts, as the folder of its volume.
func (e *procEngine) local(mounts []Mount, s string) string {
	for _, m := range mounts {
		if s == m.Target || strings.HasPrefix(s, m.Target+"/") {
			return e.volume(m.Volume) + strings.TrimPrefix(s, m.Target)
		}
	}
	return s
}

func carries(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (e *procEngine) Containers(_ context.Context, labels map[string]string) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for n, c := range e.ctrs {
		if carries(c.labels, labels) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (e *procEngine) Inspect(_ context.Context, name string) (Container, bool, error) {
	e.mu.Lock()
	c, ok := e.ctrs[name]
	e.mu.Unlock()
	if !ok {
		return Container{}, false, nil
	}
	select {
	case <-c.done:
		return Container{Labels: c.labels, ExitCode: c.code, Published: map[string]string{}}, true, nil
	default:
		return Container{Running: true, Labels: c.labels, Pid: c.cmd.Process.Pid, Published: e.published}, true, nil
	}
}

func (*procEngine) Pull(context.Context, string) error { return nil }

func (e *procEngine) Run(_ context.Context, spec RunSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.ctrs[spec.Name]; ok {
		return fault.Conflictf("procEngine", "container %s exists", spec.Name)
	}
	if len(spec.Cmd) == 0 {
		return fault.Invalidf("procEngine", "this engine runs a Cmd only")
	}
	argv := make([]string, len(spec.Cmd))
	for i, a := range spec.Cmd {
		argv[i] = e.local(spec.Mounts, a)
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the test's own command
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+e.local(spec.Mounts, v))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	_, _ = outW.Close(), errW.Close()
	if err != nil {
		_, _ = outR.Close(), errR.Close()
		return err
	}
	c := &procCtr{labels: spec.Labels, cmd: cmd, done: make(chan struct{}), stdout: outR, stderr: errR}
	e.ctrs[spec.Name], e.runs = c, append(e.runs, spec)
	go func() {
		_ = cmd.Wait()
		ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
		c.code = ws.ExitStatus()
		if ws.Signaled() {
			c.code = 128 + int(ws.Signal())
		}
		close(c.done)
	}()
	return nil
}

func (e *procEngine) lookup(name string) (*procCtr, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.ctrs[name]
	return c, ok
}

func (e *procEngine) Stop(_ context.Context, name string, grace time.Duration) error {
	c, ok := e.lookup(name)
	if !ok {
		return nil
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-c.done:
	case <-time.After(grace):
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		<-c.done
	}
	return nil
}

func (e *procEngine) Remove(_ context.Context, name string) error {
	c, ok := e.lookup(name)
	if !ok {
		return nil
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	<-c.done
	e.mu.Lock()
	defer e.mu.Unlock()
	if !c.followed {
		_, _ = c.stdout.Close(), c.stderr.Close()
	}
	delete(e.ctrs, name)
	return nil
}

func (e *procEngine) Follow(_ context.Context, name string) (io.ReadCloser, io.ReadCloser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.ctrs[name]
	switch {
	case !ok:
		return nil, nil, fault.NotFoundf("procEngine", "no container %s", name)
	case c.followed:
		return nil, nil, fault.Conflictf("procEngine", "container %s is followed", name)
	}
	c.followed = true
	return c.stdout, c.stderr, nil
}

func (e *procEngine) CreateVolume(_ context.Context, name string, labels map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vols[name] = labels
	return os.MkdirAll(e.volume(name), 0o755)
}

func (e *procEngine) Volumes(_ context.Context, labels map[string]string) ([]Volume, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Volume
	for n, l := range e.vols {
		if carries(l, labels) {
			out = append(out, Volume{Name: n, Labels: l})
		}
	}
	return out, nil
}

func (e *procEngine) RemoveVolume(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.vols, name)
	return os.RemoveAll(e.volume(name))
}

func (e *procEngine) Exec(ctx context.Context, h Helper, argv ...string) ([]byte, error) {
	local := make([]string, len(argv))
	for i, a := range argv {
		local[i] = e.local(h.Mounts, a)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, local[0], local[1:]...) //nolint:gosec // the test's own command
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fault.Internalf("procEngine", "helper %s: %v: %s", h.Name, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (e *procEngine) ReadTar(_ context.Context, h Helper, p string) (io.ReadCloser, error) {
	b, err := os.ReadFile(e.local(h.Mounts, p))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fault.NotFoundf("procEngine", "no file %s", p)
	}
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: path.Base(p), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b))}); err != nil {
		return nil, err
	}
	_, _ = tw.Write(b)
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

func (e *procEngine) WriteTar(_ context.Context, h Helper, dir string, _, _ int, archive io.Reader) error {
	root := e.local(h.Mounts, dir)
	tr := tar.NewReader(archive)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(root, filepath.FromSlash(hd.Name))
		switch hd.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(p, os.FileMode(hd.Mode))
		case tar.TypeSymlink:
			err = os.Symlink(hd.Linkname, p)
		case tar.TypeReg:
			var b []byte
			if b, err = io.ReadAll(tr); err == nil {
				err = os.WriteFile(p, b, os.FileMode(hd.Mode))
			}
		}
		if err != nil {
			return err
		}
	}
}

// onHost and inVM are how the tests' workers reach funcd: on the host's network, or from an engine in a VM.
func onHost(context.Context, Engine, Helper) (workerNet, error) { return workerNet{host: true}, nil }
func inVM(context.Context, Engine, Helper) (workerNet, error)   { return workerNet{}, nil }

func newTestWorkers(t *testing.T, e *procEngine, find netFinder) *workers {
	t.Helper()
	w, err := newWorkers(context.Background(), e, testKey(t), slog.New(slog.DiscardHandler), find)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// ADR-0346 plan step 4: the worker driver keeps the worker port's contract (ADR-0011) over an engine, and enforces
// limits.
func TestWorkersContract(t *testing.T) {
	t.Parallel()
	runtimecontract.RunContract(t, func(t *testing.T) containerrt.Runtime {
		return newTestWorkers(t, newProcEngine(t), onHost)
	})
	runtimecontract.RequireLimits(t, newTestWorkers(t, newProcEngine(t), onHost), true)
}

// workerSpec is a worker of fn's revision with its bundle at /var/funcd/artifact, as the reconciler builds it.
func workerSpec(t *testing.T, env map[string]string, cmd ...string) containerrt.WorkerSpec {
	t.Helper()
	bundle := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "handler.mjs"), []byte("export function handle() {}\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(bundle, "lib"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "lib", "util.mjs"), []byte("export {}\n"), 0o600))
	return containerrt.WorkerSpec{Namespace: "team-a", Name: "greeter", OwnerKind: v1.KindFunction, Revision: "greeter-1",
		Image: "registry.example.com/deno@sha256:aa", Command: cmd, Env: env,
		Mounts: []containerrt.Mount{{Source: bundle, Target: "/var/funcd/artifact", ReadOnly: true}},
		Limits: containerrt.Limits{CPUs: 0.5, MemoryBytes: 64 << 20}}
}

// ADR-0346 Decisions 4, 5 and 7: a worker's container is named and labelled for the session's key and the role worker,
// runs its image within its limits with FUNCD_LOG_STDOUT, and mounts its bundle, copied into a volume of its own,
// read-only; on the host's network it binds the loopback and writes its port file to its run volume, and from an engine
// in a VM it publishes FUNCD_PORT and resolves funcd.host to the engine's host gateway.
func TestWorkersRunTheSpecOnTheEngine(t *testing.T) {
	t.Parallel()
	for _, host := range []bool{true, false} {
		e := newProcEngine(t)
		find := inVM
		if host {
			find = onHost
		}
		w := newTestWorkers(t, e, find)
		spec := workerSpec(t, map[string]string{"FUNCD_PORT": "8080", "FUNCD_ARTIFACT": "/var/funcd/artifact/handler.mjs"},
			"sh", "-c", `cat "$FUNCD_ARTIFACT" > /dev/null && sleep 30`)
		inst, err := w.Create(context.Background(), spec)
		require.NoError(t, err)
		require.NoError(t, w.Start(context.Background(), inst.ID))

		require.Len(t, e.runs, 1)
		run := e.runs[0]
		require.Regexp(t, `^funcd-dev-worker-`+w.key+`-[0-9a-f]{16}$`, run.Name)
		require.Equal(t, map[string]string{KeyLabel: w.key, roleLabel: roleWorker, namespaceLabel: "team-a", nameLabel: "greeter"},
			run.Labels)
		require.Equal(t, spec.Image, run.Image)
		require.Equal(t, "1", run.Env["FUNCD_LOG_STDOUT"], "the shim prints its records to stdout")
		require.Equal(t, int64(5e8), run.NanoCPUs)
		require.Equal(t, int64(64<<20), run.MemoryBytes)
		require.Equal(t, Mount{Volume: run.Name, Target: "/var/funcd/artifact", ReadOnly: true}, run.Mounts[0],
			"the bundle is a volume of its own, never a bind of the host's folder")
		copied, err := os.ReadFile(filepath.Join(e.volume(run.Name), "lib", "util.mjs"))
		require.NoError(t, err)
		require.Equal(t, "export {}\n", string(copied), "the helper copied the bundle in")
		vols, err := e.Volumes(context.Background(), map[string]string{KeyLabel: w.key, roleLabel: roleWorker})
		require.NoError(t, err)
		if host {
			require.Equal(t, containerrt.LoopbackHost, w.EndpointHost())
			require.True(t, run.HostNetwork)
			require.Zero(t, run.Port)
			require.Empty(t, run.ExtraHosts)
			require.Equal(t, "/run/funcd/port", run.Env["FUNCD_PORTFILE"])
			require.Equal(t, Mount{Volume: run.Name + "-run", Target: "/run/funcd"}, run.Mounts[1])
			require.Len(t, vols, 2, "the bundle's and the run volume")
		} else {
			require.Equal(t, "funcd.host", w.EndpointHost())
			require.False(t, run.HostNetwork)
			require.Equal(t, 8080, run.Port)
			require.Equal(t, []string{"funcd.host:host-gateway"}, run.ExtraHosts)
			require.NotContains(t, run.Env, "FUNCD_PORTFILE")
			require.Len(t, run.Mounts, 1)
			require.Len(t, vols, 1)
		}
		ok, why := w.RunsImages()
		require.True(t, ok)
		require.Empty(t, why)

		require.NoError(t, w.Stop(context.Background(), inst.ID))
		require.NoError(t, w.Remove(context.Background(), inst.ID))
		vols, err = e.Volumes(context.Background(), map[string]string{KeyLabel: w.key})
		require.NoError(t, err)
		require.Empty(t, vols, "Remove takes the worker's volumes")
	}
}

// ADR-0346 Decision 4: from an engine in a VM, which gives no port file, a worker listens once its readiness path
// answers, with any status, at its published port.
func TestWorkersListenOnceReadinessAnswers(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	e := newProcEngine(t)
	e.published = map[string]string{"8080/tcp": "127.0.0.1:1"}
	w := newTestWorkers(t, e, inVM)
	inst, err := w.Create(context.Background(), workerSpec(t, map[string]string{"FUNCD_PORT": "8080"}, "sleep", "30"))
	require.NoError(t, err)
	require.NoError(t, w.Start(context.Background(), inst.ID))
	got, err := w.Status(context.Background(), inst.ID)
	require.NoError(t, err)
	require.False(t, got.Listened, "nothing answers at the published port yet")
	require.Zero(t, got.Port)

	e.mu.Lock()
	e.published = map[string]string{"8080/tcp": addr}
	e.mu.Unlock()
	got, err = w.Status(context.Background(), inst.ID)
	require.NoError(t, err)
	require.True(t, got.Listened, "a 503 is an answer: the worker listens")
	require.Equal(t, containerrt.LoopbackHost, got.IP)
	require.Equal(t, atoi(port), got.Port)
}

// ADR-0346 Decision 5: an engine that shares this host's kernel but not its network, as a rootless one, runs no worker,
// and says why.
func TestWorkersRefuseAnEngineWithoutTheHostsNetwork(t *testing.T) {
	t.Parallel()
	require.Equal(t, workerNet{host: true}, netOf("boot-a\n", "net:[1]", []byte("boot-a\nnet:[1]\n")), "the host's kernel and network")
	require.Equal(t, workerNet{}, netOf("boot-a\n", "net:[1]", []byte("boot-b\nnet:[9]\n")), "another kernel: a VM")
	require.Equal(t, workerNet{}, netOf("boot-a\n", "net:[1]", []byte("garbage")), "an answer that does not parse")
	rootless := netOf("boot-a\n", "net:[1]", []byte("boot-a\nnet:[2]\n"))
	require.Contains(t, rootless.refused, "rootless")

	w := newTestWorkers(t, newProcEngine(t), func(context.Context, Engine, Helper) (workerNet, error) { return rootless, nil })
	ok, why := w.RunsImages()
	require.False(t, ok)
	require.Equal(t, rootless.refused, why)
	_, err := w.Create(context.Background(), workerSpec(t, nil, "sleep", "30"))
	require.ErrorIs(t, err, containerrt.ErrImageUnavailable)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// ADR-0346 Decision 7: the worker driver reaps only the key's workers, and under --persist the add-on runtime's reap
// still takes the workers' volumes while it keeps the engines'.
func TestWorkersReapOnlyTheirRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newProcEngine(t)
	key, other := testKey(t), testKey(t)
	for _, v := range []struct {
		name string
		key  string
		role string
	}{{"engine-data", key, roleStorage}, {"worker-bundle", key, roleWorker}, {"other-bundle", other, roleWorker}} {
		require.NoError(t, e.CreateVolume(ctx, v.name, map[string]string{KeyLabel: v.key, roleLabel: v.role}))
	}
	for _, c := range []struct {
		name, key, role string
	}{{"engine", key, roleEngine}, {"worker", key, roleWorker}, {"other", other, roleWorker}} {
		require.NoError(t, e.Run(ctx, RunSpec{Name: c.name, Cmd: []string{"sleep", "30"},
			Labels: map[string]string{KeyLabel: c.key, roleLabel: c.role}}))
	}
	_, err := newWorkers(ctx, e, key, nil, inVM)
	require.NoError(t, err)
	names, err := e.Containers(ctx, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"engine", "other"}, names, "the key's worker container goes")
	vols, err := e.Volumes(ctx, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"engine-data", "other-bundle"}, volumeNames(vols))

	require.NoError(t, e.CreateVolume(ctx, "worker-bundle", map[string]string{KeyLabel: key, roleLabel: roleWorker}))
	_, err = NewRuntime(ctx, e, key, true, nil)
	require.NoError(t, err)
	names, err = e.Containers(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"other"}, names, "the add-on runtime's reap takes every container of the key")
	vols, err = e.Volumes(ctx, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"engine-data", "other-bundle"}, volumeNames(vols), "--persist keeps the engines' volumes only")
}

func volumeNames(vols []Volume) []string {
	out := make([]string, 0, len(vols))
	for _, v := range vols {
		out = append(out, v.Name)
	}
	return out
}

// ADR-0346 Decisions 4 and 5 on a real engine: a worker serves its bundle, copied into its read-only volume, at the
// port it listens on, reaches a listener of this host on 127.0.0.1 by the host the driver names, and an ended run keeps
// its exit code and its output. busybox stands in for an Addon's runtime image.
func TestWorkersOnDocker(t *testing.T) {
	t.Parallel()
	sock := requireDocker(t)
	ctx := context.Background()
	rt, key := newRuntime(t, sock, false)
	w, err := NewWorkers(ctx, rt.Engine(), key, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	ir := w.(containerrt.ImageRunner)
	ok, why := ir.RunsImages()
	require.True(t, ok, why)
	host := ir.EndpointHost()

	funcd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "funcd-answers") }))
	t.Cleanup(funcd.Close)
	_, funcdPort, err := net.SplitHostPort(strings.TrimPrefix(funcd.URL, "http://"))
	require.NoError(t, err)

	spec := workerSpec(t, map[string]string{"FUNCD_PORT": "8080"},
		"httpd", "-f", "-p", "8080", "-h", "/var/funcd/artifact")
	spec.Image = HelperImage
	if host == containerrt.LoopbackHost {
		l, lerr := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, lerr)
		_, p, _ := net.SplitHostPort(l.Addr().String())
		require.NoError(t, l.Close())
		spec.Env = map[string]string{}
		spec.Command = []string{"sh", "-c", `echo ` + p + ` > "$FUNCD_PORTFILE" && exec httpd -f -p 127.0.0.1:` + p + ` -h /var/funcd/artifact`}
	}
	inst, err := w.Create(ctx, spec)
	require.NoError(t, err)
	require.NoError(t, w.Start(ctx, inst.ID))
	var got containerrt.Instance
	require.Eventually(t, func() bool {
		got, err = w.Status(ctx, inst.ID)
		require.NoError(t, err)
		return got.Listened
	}, time.Minute, 100*time.Millisecond, "the worker listens")
	require.Equal(t, containerrt.LoopbackHost, got.IP)
	resp, err := httpx.NodeClient(5 * time.Second).Get("http://127.0.0.1:" + strconv.Itoa(got.Port) + "/lib/util.mjs")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	httpx.CloseBody(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "export {}\n", string(body), "the worker serves its copied bundle")

	c := inspectConfig(t, rt, workerName(w, inst.ID))
	require.Equal(t, roleWorker, c.Config.Labels[roleLabel])
	require.Equal(t, key, c.Config.Labels[KeyLabel])
	require.Len(t, c.HostConfig.Mounts, map[bool]int{true: 2, false: 1}[host == containerrt.LoopbackHost])
	require.Equal(t, "/var/funcd/artifact", c.HostConfig.Mounts[0].Target)
	require.True(t, c.HostConfig.Mounts[0].ReadOnly, "the bundle is read-only")
	require.Equal(t, "volume", c.HostConfig.Mounts[0].Type, "never a bind of the host's folder")
	if host != containerrt.LoopbackHost {
		require.Equal(t, "127.0.0.1", c.HostConfig.PortBindings["8080/tcp"][0].HostIP, "published on the loopback only")
	}

	reach := workerSpec(t, nil, "sh", "-c", `echo out-line; wget -qO- http://`+host+`:`+funcdPort+`/; exit 3`)
	reach.Image, reach.Name = HelperImage, "reach"
	inst, err = w.Create(ctx, reach)
	require.NoError(t, err)
	require.NoError(t, w.Start(ctx, inst.ID))
	require.Eventually(t, func() bool {
		got, err = w.Status(ctx, inst.ID)
		require.NoError(t, err)
		return got.State.Terminal()
	}, time.Minute, 100*time.Millisecond, "the run ends")
	require.Equal(t, containerrt.Exit{Cause: containerrt.ExitByCode, Code: 3}, got.Exit)
	rc, err := w.Logs(ctx, inst.ID)
	require.NoError(t, err)
	tail, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "out-line\nfuncd-answers", strings.TrimSpace(string(tail)), "its output, and funcd's answer at "+host)
}

func workerName(w containerrt.Runtime, id containerrt.InstanceID) string {
	return w.(*workers).nameOf(id)
}

// ADR-0346 Decisions 4 and 5 at the Engine API: a worker's container on the host's network publishes nothing; one on an
// engine in a VM publishes its port on 127.0.0.1 alone and resolves funcd.host to the host gateway; both mount their
// volumes with no copy-up, the bundle read-only, run Cmd as the whole of their argv, and take their limits; a helper may
// run on the host's network.
func TestRunSpecAtTheEngineAPI(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "api")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	var mu sync.Mutex
	var created []createBody
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			var b createBody
			require.NoError(t, json.NewDecoder(r.Body).Decode(&b))
			mu.Lock()
			created = append(created, b)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"Id":"c1"}`)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			_, _ = io.WriteString(w, `{"StatusCode":0}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	e := &engineAPI{http: socketClient(sock), base: "http://docker/v" + maxAPIVersion}
	ctx := context.Background()

	mounts := []Mount{{Volume: "w", Target: "/var/funcd/artifact", ReadOnly: true}, {Volume: "w-run", Target: "/run/funcd"}}
	require.NoError(t, e.Run(ctx, RunSpec{Name: "w", Image: "img", Cmd: []string{"sh", "-c", "true"}, Mounts: mounts,
		HostNetwork: true, Port: 8080, NanoCPUs: 5e8, MemoryBytes: 64 << 20}))
	require.NoError(t, e.Run(ctx, RunSpec{Name: "v", Image: "img", Mounts: mounts[:1], Port: 8080,
		ExtraHosts: []string{"funcd.host:host-gateway"}}))
	_, err = e.Exec(ctx, Helper{Name: "h", Image: HelperImage, HostNetwork: true}, "true")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, created, 3)
	host, vm, helper := created[0], created[1], created[2]
	require.Equal(t, "host", host.HostConfig.NetworkMode)
	require.Empty(t, host.HostConfig.PortBindings, "on the host's network nothing is published")
	require.Empty(t, host.ExposedPorts)
	require.Equal(t, []string{"sh", "-c", "true"}, host.Entrypoint, "Cmd is the whole argv")
	require.Empty(t, host.Cmd)
	require.Equal(t, int64(5e8), host.HostConfig.NanoCPUs)
	require.Equal(t, int64(64<<20), host.HostConfig.Memory)
	require.Len(t, host.HostConfig.Mounts, 2)
	for i, m := range host.HostConfig.Mounts {
		require.Equal(t, "volume", m.Type)
		require.True(t, m.VolumeOptions.NoCopy)
		require.Equal(t, i == 0, m.ReadOnly, "%s", m.Target)
	}
	require.Empty(t, vm.HostConfig.NetworkMode)
	require.Equal(t, map[string][]portBinding{"8080/tcp": {{HostIP: "127.0.0.1"}}}, vm.HostConfig.PortBindings,
		"published on 127.0.0.1 at a free port")
	require.Contains(t, vm.ExposedPorts, "8080/tcp")
	require.Equal(t, []string{"funcd.host:host-gateway"}, vm.HostConfig.ExtraHosts)
	require.Empty(t, vm.Entrypoint, "the image's own entrypoint")
	require.Equal(t, "host", helper.HostConfig.NetworkMode)
}
