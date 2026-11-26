package nerdctl

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/provider/docker"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// fakeDirEnv names the directory of the fake nerdctl's state (internal/testkit/fakenerdctl).
const fakeDirEnv = "FUNCD_FAKE_NERDCTL_DIR"

// fakeBin is internal/testkit/fakenerdctl, built once by TestMain without the race detector, whose start costs a
// second.
var fakeBin string //nolint:gochecknoglobals // set once by TestMain, before any test runs

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakenerdctl")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "fakenerdctl")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "github.com/pyvvo/funcd/internal/testkit/fakenerdctl").CombinedOutput(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "build the fake nerdctl: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// call is one command the fake ran: its arguments and what it read on its standard input.
type call struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

// fake is a fake nerdctl on PATH over a fresh state; it returns its path and its directory.
func fake(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nerdctl")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+fakeDirEnv+"='"+dir+"' exec '"+fakeBin+"' \"$@\"\n"), 0o700))
	return bin, dir
}

func calls(t *testing.T, dir string) []call {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []call
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var c call
		require.NoError(t, json.Unmarshal(line, &c))
		out = append(out, c)
	}
	return out
}

// find is the first logged call whose arguments start with prefix and hold each of want.
func find(t *testing.T, cs []call, prefix []string, want ...string) call {
	t.Helper()
	for _, c := range cs {
		if len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) && containsAll(c.Args, want) {
			return c
		}
	}
	t.Fatalf("no call %v holding %v in %v", prefix, want, cs)
	return call{}
}

func containsAll(args, want []string) bool {
	for _, w := range want {
		if !slices.Contains(args, w) {
			return false
		}
	}
	return true
}

const key = "0123456789abcdef"

// ADR-0334 Decision 1: nerdctl on PATH; without it, Unavailable names colima nerdctl install on macOS.
func TestFind(t *testing.T) {
	bin, dir := fake(t)
	t.Setenv("PATH", dir)
	got, err := Find()
	require.NoError(t, err)
	require.Equal(t, bin, got)
	t.Setenv("PATH", t.TempDir())
	_, err = Find()
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	if runtime.GOOS == "darwin" {
		require.ErrorContains(t, err, "colima nerdctl install")
	}
}

// ADR-0334 Decisions 2, 3 and 7 through nerdctl: the engine's run with the key's labels, no restart policy, the stop
// signal SIGTERM and the grace, its port on 127.0.0.1 and its volume with no copy-up, its environment off the command
// line; the helper that sets the volume's root; the stop with the grace, then the removal; StopAll's removal of every
// container and volume of the key.
func TestEngineThroughNerdctl(t *testing.T) {
	t.Parallel()
	bin, dir := fake(t)
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ready.Close)
	_, port, err := net.SplitHostPort(ready.Listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "port"), []byte(port), 0o600))
	ctx := context.Background()

	var logs bytes.Buffer
	rt, err := New(ctx, bin, key, false, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	find(t, calls(t, dir), []string{"ps", "-a"}, "label=funcd/dev-key="+key)

	spec := provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "login"}, Image: "example.test/rauthy@sha256:" +
		strings.Repeat("a", 64), Port: 8080, Replicas: 1, Readiness: provider.ReadinessProbe{Path: "/health", ExpectStatus: http.StatusOK},
		Env:       map[string]string{"HQL_SECRET_API": "s3cret", "TRUSTED_PROXIES": "10.0.0.0/8\n127.0.0.1/32"},
		Storage:   &provider.StorageSpec{Path: "/app/data", UID: 10001, GID: 10001},
		Resources: provider.ResourceSpec{CPU: "1", Memory: "256Mi"}}
	st, err := rt.Converge(ctx, spec)
	require.NoError(t, err)
	require.True(t, st.Ready, "%+v", st)
	require.Equal(t, "127.0.0.1:"+port, st.Address)

	name := "funcd-dev-addon-" + key + "-default-login"
	cs := calls(t, dir)
	created := find(t, cs, []string{"volume", "create"}, name, "funcd/dev-key="+key, "funcd/role=storage")
	require.Equal(t, name, created.Args[len(created.Args)-1])
	chown := find(t, cs, []string{"run", "--rm"}, "-v", name+":/d", docker.HelperImage, "10001:10001")
	require.Contains(t, strings.Join(chown.Args, " "), "chown")
	run := find(t, cs, []string{"run", "-d"}, "--name", name, "--restart", "no", "--stop-signal", "SIGTERM",
		"--stop-timeout", "10", "-p", "127.0.0.1::8080", "funcd/dev-key="+key, "funcd/role=engine",
		"--mount", "type=bind,source=/var/lib/nerdctl/volumes/"+name+"/_data,target=/app/data", "--env-file", "/dev/stdin", spec.Image)
	require.NotContains(t, strings.Join(run.Args, " "), "s3cret", "no Secret on the command line")
	require.Equal(t, "HQL_SECRET_API=s3cret\n", run.Stdin)
	require.Contains(t, run.Args, "TRUSTED_PROXIES=10.0.0.0/8\n127.0.0.1/32", "a value with a newline")

	before := len(calls(t, dir))
	st, err = rt.Converge(ctx, spec)
	require.NoError(t, err)
	require.True(t, st.Ready)
	for _, c := range calls(t, dir)[before:] {
		require.NotEqual(t, "run", c.Args[0], "a running engine of the same spec is kept")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "does not enforce these CPU and memory limits"), "resources set no limit, logged once")
	require.NotContains(t, run.Args, "--cpus", "dev enforces no resources")

	require.Equal(t, fault.Conflict, fault.KindOf(rt.Reclaim(ctx, spec.Ref)), "the engine's container exists")
	require.NoError(t, rt.Teardown(ctx, spec.Ref))
	cs = calls(t, dir)
	find(t, cs, []string{"stop", "-t", "10", name})
	find(t, cs, []string{"rm", "-f", name})

	_, err = rt.Converge(ctx, spec)
	require.NoError(t, err)
	rt.StopAll()
	cs = calls(t, dir)
	require.Equal(t, []string{"stop", "-t", "10", name}, cs[len(cs)-5].Args[:4])
	find(t, cs[len(cs)-4:], []string{"rm", "-f", name})
	find(t, cs[len(cs)-3:], []string{"volume", "ls", "-q"}, "label=funcd/dev-key="+key)
	find(t, cs[len(cs)-1:], []string{"volume", "rm", name})
}

// ADR-0334 Decision 4 through nerdctl: Newest lists the folder twice with the helper's find and stat, and streams only
// the newest file the two listings agree on with the helper's tar; Put extracts a tar of the copy owned by uid and gid
// with the helper's tar, making its directory; Clear empties the folder with the helper's rm.
func TestFilesThroughNerdctl(t *testing.T) {
	t.Parallel()
	bin, dir := fake(t)
	ctx := context.Background()
	rt, err := New(ctx, bin, key, false, nil)
	require.NoError(t, err)
	name := "funcd-dev-addon-" + key + "-default-login"
	state, _ := json.Marshal(map[string]map[string]map[string]string{"volumes": {name: {
		"funcd/dev-key": key, "funcd/role": "storage", "funcd/namespace": "default", "funcd/name": "login"}}})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), state, 0o600))
	const at = "2026-11-19 05:00:00.000000001 +0000"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "listing-0"), []byte("3|"+at+"|/d/backups/100\n5|"+at+"|/d/backups/200\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "listing-1"), []byte("3|"+at+"|/d/backups/100\n7|"+at+"|/d/backups/200\n"), 0o600))
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "100", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}))
	_, err = tw.Write([]byte("old"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tar"), archive.Bytes(), 0o600))

	file, body, err := rt.Newest(ctx, "default", "login", "backups", time.Millisecond)
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "100", file, "200 grew between the listings")
	require.Equal(t, "old", string(got))
	cs := calls(t, dir)
	var stats int
	for _, c := range cs {
		if c.Args[0] == "run" && strings.Contains(strings.Join(c.Args, " "), "stat -c") {
			stats++
			require.Contains(t, c.Args, "/d/backups")
		}
	}
	require.Equal(t, 2, stats, "two listings")
	find(t, cs, []string{"run", "--rm"}, "-v", name+":/d", "tar", "-c", "-f", "-", "-C", "/d/backups", "100")

	require.NoError(t, rt.Put(ctx, "default", "login", "restore", "100", strings.NewReader("copy"), 10001, 10001))
	put := find(t, calls(t, dir), []string{"run", "--rm"}, "-i", "-v", name+":/d", "/d/restore", "10001:10001")
	tr := tar.NewReader(strings.NewReader(put.Stdin))
	h, err := tr.Next()
	require.NoError(t, err)
	require.Equal(t, "100", h.Name)
	require.Equal(t, 10001, h.Uid)
	require.Equal(t, 10001, h.Gid)
	require.Equal(t, int64(0o600), h.Mode)
	content, err := io.ReadAll(tr)
	require.NoError(t, err)
	require.Equal(t, "copy", string(content))

	before := len(calls(t, dir))
	require.NoError(t, rt.Clear(ctx, "default", "login", "restore"))
	clear := find(t, calls(t, dir)[before:], []string{"run", "--rm"}, "-v", name+":/d", "/d/restore")
	require.Contains(t, strings.Join(clear.Args, " "), "rm -rf")

	_, _, err = rt.Newest(ctx, "default", "nobody", "backups", 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no volume")
}

// A failed nerdctl's error holds its fatal messages, not a pull's progress before them.
func TestLastWords(t *testing.T) {
	t.Parallel()
	stderr := "ghcr.io/pyvvo/funcd-rauthy@sha256:7686: fetching image content\nPulling from OCI Registry\n" +
		`time="2026-11-19T07:52:22+01:00" level=fatal msg="invalid field 'volume-nocopy' must be a key=value pair"` + "\n" +
		`time="2026-11-19T07:52:22+01:00" level=fatal msg="exit status 1"` + "\n"
	require.Equal(t, "invalid field 'volume-nocopy' must be a key=value pair; exit status 1", lastWords(stderr))
	require.Equal(t, "the last line", lastWords("a pull\nthe last line\n"))
	require.Equal(t, "level=error no msg", lastWords("level=error no msg\n"), "a line with no msg= is kept whole")
	require.Empty(t, lastWords(""))
}

// ADR-0346 Decisions 4, 5 and 7 through nerdctl: a worker's bundle volume of the role worker, filled through the
// helper's tar and mounted read-only as the volume's own directory; its run with the key, FUNCD_PORT published on
// 127.0.0.1, funcd.host at the VM's host, its limits and FUNCD_LOG_STDOUT; it listens once its readiness path
// answers; Remove takes its container and volume.
func TestWorkersThroughNerdctl(t *testing.T) {
	t.Parallel()
	bin, dir := fake(t)
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ready.Close)
	_, port, err := net.SplitHostPort(ready.Listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "port"), []byte(port), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hosts"), []byte("192.168.5.2 host.lima.internal\n"), 0o600))
	ctx := context.Background()
	w, err := docker.NewWorkers(ctx, cli{bin: bin}, key, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	require.Equal(t, v1.PlatformLinuxARM64, w.(containerrt.ImageRunner).ImagePlatform(), "nerdctl's own platform")
	find(t, calls(t, dir), []string{"version", "--format", "{{.Client.Os}}/{{.Client.Arch}}"})

	bundle := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "handler.mjs"), []byte("export function handle() {}\n"), 0o600))
	spec := containerrt.WorkerSpec{Namespace: "default", Name: "greeter", OwnerKind: v1.KindFunction, Revision: "greeter-1",
		Image: "example.test/deno@sha256:" + strings.Repeat("a", 64), Env: map[string]string{"FUNCD_PORT": "8080"},
		Mounts: []containerrt.Mount{{Source: bundle, Target: "/var/funcd/artifact", ReadOnly: true}},
		Limits: containerrt.Limits{CPUs: 0.5, MemoryBytes: 64 << 20}}
	inst, err := w.Create(ctx, spec)
	require.NoError(t, err)
	require.NoError(t, w.Start(ctx, inst.ID))

	cs := calls(t, dir)
	vol := find(t, cs, []string{"volume", "create"}, "funcd/role=worker", "funcd/dev-key="+key)
	name := vol.Args[len(vol.Args)-1]
	require.Regexp(t, "^funcd-dev-worker-"+key+"-[0-9a-f]{16}$", name)
	fill := find(t, cs, []string{"run", "--rm"}, "-i", "-v", name+":/d", docker.HelperImage)
	require.Contains(t, fill.Stdin, "handler.mjs", "the helper extracts the bundle's tar")
	run := find(t, cs, []string{"run", "-d"}, "--name", name, "-p", "127.0.0.1::8080", "--add-host", "funcd.host:192.168.5.2",
		"--cpus", "0.5", "--memory", "67108864", "funcd/role=worker", "--mount",
		"type=bind,source=/var/lib/nerdctl/volumes/"+name+"/_data,target=/var/funcd/artifact,readonly", spec.Image)
	require.Contains(t, run.Stdin, "FUNCD_LOG_STDOUT=1\n")
	require.NotContains(t, run.Args, "--network", "an engine in a VM: the worker's own network")

	got, err := w.Status(ctx, inst.ID)
	require.NoError(t, err)
	require.True(t, got.Listened, "a 503 is an answer")
	require.Equal(t, "127.0.0.1", got.IP)
	require.Equal(t, port, strconv.Itoa(got.Port))
	require.NoError(t, w.Stop(ctx, inst.ID))
	require.NoError(t, w.Remove(ctx, inst.ID))
	cs = calls(t, dir)
	find(t, cs, []string{"rm", "-f", name})
	find(t, cs, []string{"volume", "rm", name})
}

// ADR-0359 Decision 4: HasImage runs nerdctl image inspect, an image nerdctl does not hold is no error, and a load keeps
// the references the tar names.
func TestHasImageAndLoad(t *testing.T) {
	t.Parallel()
	bin, _ := fake(t)
	c := cli{bin: bin}
	ctx := context.Background()
	const ref = "funcd/runtime-nodejs22:dev-0123456789ab"
	has, err := c.HasImage(ctx, ref)
	require.NoError(t, err)
	require.False(t, has, "no such image is no error")

	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	manifest := `[{"RepoTags":["funcd/runtime-nodejs22:latest","` + ref + `"]}]`
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manifest))}))
	_, err = io.WriteString(tw, manifest)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, c.Load(ctx, &b))
	has, err = c.HasImage(ctx, ref)
	require.NoError(t, err)
	require.True(t, has)
}
