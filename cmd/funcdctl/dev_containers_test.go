//go:build dev

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider/docker"
	"github.com/pyvvo/funcd/internal/runtime/embedimg"
)

// ADR-0359: under funcdctl dev --containers, funcd's own runtimes run in containers, from images funcdctl carries.

// ownEngine is an engine on platform that holds the images in held; Load keeps the reference that a test's tar is,
// whole. The other methods are the nil Engine's, which these tests never reach.
type ownEngine struct {
	docker.Engine
	platform v1.OCIPlatform
	held     map[string]bool
	loads    int
}

func (e *ownEngine) Platform(context.Context) (v1.OCIPlatform, error) { return e.platform, nil }

func (e *ownEngine) HasImage(_ context.Context, ref string) (bool, error) { return e.held[ref], nil }

func (e *ownEngine) Load(_ context.Context, tar io.Reader) error {
	b, err := io.ReadAll(tar)
	e.held[string(b)], e.loads = true, e.loads+1
	return err
}

// testTars gives each worker runtime a tar that is its dev reference, as ownEngine reads it.
func testTars(rt string) (io.Reader, string, bool) {
	ref := "funcd/runtime-" + rt + ":dev-0123456789ab"
	return strings.NewReader(ref), ref, true
}

func ownPlatform() v1.OCIPlatform { return v1.OCIPlatform("linux/" + runtime.GOARCH) }

// ADR-0359 Decision 4, the unit side of scenario dev-containers-load-once: dev loads an image only when the engine lacks
// its reference, and names that reference.
func TestLoadOwnImagesOnce(t *testing.T) {
	t.Parallel()
	eng := &ownEngine{platform: ownPlatform(), held: map[string]bool{}}
	logger := slog.New(slog.DiscardHandler)
	got, err := loadOwnImages(context.Background(), eng, testTars, logger)
	require.NoError(t, err)
	require.Equal(t, map[v1.RuntimeName]string{"nodejs22": "funcd/runtime-nodejs22:dev-0123456789ab",
		"python314": "funcd/runtime-python314:dev-0123456789ab"}, got)
	require.Equal(t, 2, eng.loads)
	_, err = loadOwnImages(context.Background(), eng, testTars, logger)
	require.NoError(t, err)
	require.Equal(t, 2, eng.loads, "the engine holds both, so nothing is loaded again")
}

// scenario: dev-containers-refused — with --containers, dev refuses to start, saying why, with neither engine, on an
// engine of another platform than the images', and with the committed placeholders.
func TestScenarioDevContainersRefused(t *testing.T) {
	t.Parallel()
	dir := devProject(t, nodeFunction("api", "", okBuild))
	inst, err := (&cli{out: io.Discard}).startDev(context.Background(), dir,
		devConfig{cacheDir: t.TempDir(), containers: true, addonDrivers: noEngine()})
	if inst != nil {
		t.Cleanup(func() { _ = inst.stop() })
	}
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "the workers cannot run in containers")
	require.ErrorContains(t, err, "/var/run/docker.sock", "the engine search's reason")

	logger := slog.New(slog.DiscardHandler)
	_, err = loadOwnImages(context.Background(), &ownEngine{platform: "linux/s390x", held: map[string]bool{}}, testTars, logger)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "the engine runs linux/s390x, and this funcdctl carries images for "+string(ownPlatform()))

	placeholder := func(string) (io.Reader, string, bool) { return nil, "", false }
	_, err = loadOwnImages(context.Background(), &ownEngine{platform: ownPlatform(), held: map[string]bool{}}, placeholder, logger)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "built without just build-worker-images")
}

// scenario: dev-containers-key — dev.containers: true in funcdctl.config.yaml runs dev as --containers does, and the
// flag --containers=false wins.
func TestScenarioDevContainersKey(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		args []string
		want bool
	}{
		"the key":     {want: true},
		"a flag wins": {args: []string{"--containers=false"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := devProject(t, map[string]string{devConfigFile: "dev:\n  containers: true\n"})
			cmd := (&cli{out: io.Discard}).devCmd()
			require.NoError(t, cmd.Flags().Parse(tc.args))
			var cfg devConfig
			cfg.containers, _ = cmd.Flags().GetBool("containers")
			_, _, err := resolveDevRun("test", start, cmd.Flags().Args(), cmd.Flags(), &cfg)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.containers)
		})
	}
}

// requireOwnImages skips unless this test binary embeds funcd's worker images, which just build-worker-images writes
// (ADR-0359 plan step 8): CI builds none, so these scenarios do not run there.
func requireOwnImages(t *testing.T) {
	t.Helper()
	for _, rt := range []string{"nodejs22", "python314"} {
		if _, _, ok := embedimg.WorkerTar(rt); !ok {
			t.Skip("the --containers scenarios run funcd's own images: run just build-worker-images first")
		}
	}
}

const (
	nodePlatformBuild = "export function handle() { return { platform: process.platform }; }\n"
	pyPlatformBuild   = "import sys\n\n\ndef handle(ctx, event):\n    return {'platform': sys.platform}\n"
)

// pyFunction is the files of a python314 Function name: its resource file, its build and its permissive contract file.
func pyFunction(name, build string) map[string]string {
	return map[string]string{
		name + ".yaml":          fnDoc(name, "python314", ""),
		name + ".py":            build,
		name + ".contract.yaml": strings.Replace(permissiveContract, "nodejs22", "python314", 1),
	}
}

// containersDev starts dev --containers over dir on the Docker engine of sock, writing its output to out, and returns
// it with its session key; the session's workers go at the test's end.
func containersDev(t *testing.T, sock, dir string, out io.Writer) (*devInstance, string, func()) {
	t.Helper()
	cfg := devConfig{cacheDir: t.TempDir(), containers: true}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: out}).startDev(ctx, dir, cfg)
	require.NoError(t, err)
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			_ = inst.stop()
		}
	}
	t.Cleanup(stop)
	return inst, key, stop
}

// answers waits until Function name is Ready and answers want.
func answers(t *testing.T, inst *devInstance, name, want string) {
	t.Helper()
	waitPhase(t, inst, name, v1.PhaseReady)
	resp, body := post(t, inst.gatewayURL+"/function/"+name, `{"data":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", name, body)
	require.JSONEq(t, want, body, name)
}

// scenario: dev-containers — given dev --containers on Docker and a folder with a Function on nodejs22, a pooled one, one
// on python314 and a Job on nodejs22, each Function answers from a Linux container of its own and the run succeeds in one.
func TestScenarioDevContainers(t *testing.T) {
	sock := requireDocker(t)
	requireOwnImages(t)
	d := newDockerEngine(t, sock)
	dir := devProject(t, merge(nodeFunction("api", "", nodePlatformBuild),
		nodeFunction("pooled", "  pooling:\n    worker: w1\n", nodePlatformBuild), pyFunction("intake", pyPlatformBuild)))
	inst, key, _ := containersDev(t, sock, dir, io.Discard)
	for _, name := range []string{"api", "pooled", "intake"} {
		answers(t, inst, name, `{"platform":"linux"}`)
	}
	require.Len(t, d.workers(key), 3, "each Function's worker is a container of its own")
	run := runJob(t, inst, dir, "hello", "nodejs22", nodePlatformBuild)
	require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "%+v", run.Status.Conditions)
	require.JSONEq(t, `{"platform":"linux"}`, string(run.Status.Result))
}

// scenario: dev-containers-no-host-runtime — on a PATH with no node and no Python, dev --containers serves the folder's
// Functions, and dev without it refuses to start, as it does today.
func TestScenarioDevContainersNoHostRuntime(t *testing.T) {
	sock := requireDocker(t)
	requireOwnImages(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("FUNCD_NODE", "")
	t.Setenv("FUNCD_PYTHON", "")
	dir := devProject(t, merge(nodeFunction("api", "", nodePlatformBuild), pyFunction("intake", pyPlatformBuild)))
	inst, err := (&cli{out: io.Discard}).startDev(context.Background(), dir, devConfig{cacheDir: t.TempDir()})
	if inst != nil {
		t.Cleanup(func() { _ = inst.stop() })
	}
	require.ErrorContains(t, err, "no node interpreter found")
	inst, _, _ = containersDev(t, sock, dir, io.Discard)
	answers(t, inst, "api", `{"platform":"linux"}`)
	answers(t, inst, "intake", `{"platform":"linux"}`)
}

// scenario: dev-containers-save — when a Function's source file changes, its new answer is served from a new container.
func TestScenarioDevContainersSave(t *testing.T) {
	sock := requireDocker(t)
	requireOwnImages(t)
	d := newDockerEngine(t, sock)
	dir := devProject(t, nodeFunction("api", "", "export function handle() { return { v: 1 }; }\n"))
	inst, key, _ := containersDev(t, sock, dir, io.Discard)
	answers(t, inst, "api", `{"v":1}`)
	before := d.workers(key)
	writeProject(t, dir, map[string]string{"api.mjs": "export function handle() { return { v: 2 }; }\n"})
	eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, http.StatusOK, `"v":2`)
	require.True(t, slices.ContainsFunc(d.workers(key), func(n string) bool { return !slices.Contains(before, n) }),
		"the new answer comes from a new container")
}

// scenario: dev-containers-load-once — given an engine without funcd's images, dev loads both before it serves; when it
// starts again, it loads neither.
func TestScenarioDevContainersLoadOnce(t *testing.T) {
	sock := requireDocker(t)
	requireOwnImages(t)
	d := newDockerEngine(t, sock)
	for _, rt := range []string{"nodejs22", "python314"} {
		_, ref, _ := embedimg.WorkerTar(rt)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
			"http://docker/images/"+ref+"?"+url.Values{"force": {"1"}}.Encode(), nil)
		require.NoError(t, err)
		resp, err := d.client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Contains(t, []int{http.StatusOK, http.StatusNotFound}, resp.StatusCode, "remove %s", ref)
	}
	dir := devProject(t, nodeFunction("api", "", nodePlatformBuild))
	for i, want := range []int{2, 0} {
		var out lockedBuffer
		inst, _, stop := containersDev(t, sock, dir, &out)
		answers(t, inst, "api", `{"platform":"linux"}`)
		stop()
		require.Equal(t, want, strings.Count(out.String(), "loaded a runtime image"), "start %d: %s", i+1, out.String())
	}
}

// scenario: dev-containers-linked-build — a Python Function in folder mode and a Node Function beside a node_modules,
// both delivered as links to the folder, read their sources and their modules in the container.
func TestScenarioDevContainersLinkedBuild(t *testing.T) {
	sock := requireDocker(t)
	requireOwnImages(t)
	dir := devProject(t, merge(
		nodeFunction("api", "", "import { greet } from 'greet';\nexport function handle() { return { msg: greet() }; }\n"),
		pyFunction("intake", "from helper import word\n\n\ndef handle(ctx, event):\n    return {'msg': word()}\n"),
		map[string]string{
			"node_modules/greet/package.json": `{"name":"greet","type":"module","exports":"./index.mjs"}`,
			"node_modules/greet/index.mjs":    "export const greet = () => 'from a module';\n",
			"helper.py":                       "def word():\n    return 'from a sibling'\n",
		}))
	inst, _, _ := containersDev(t, sock, dir, io.Discard)
	answers(t, inst, "api", `{"msg":"from a module"}`)
	answers(t, inst, "intake", `{"msg":"from a sibling"}`)
}
