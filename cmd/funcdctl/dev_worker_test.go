//go:build dev

package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	shimnode "github.com/pyvvo/funcd-typescript/shim"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/provider/docker"
	"github.com/pyvvo/funcd/internal/testkit/imageregistry"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// ADR-0346: under funcdctl dev, a worker on an Addon's runtime runs on dev's container engine.

const workerPrefix = "funcd-dev-worker-"

// mustRunOnCI fails t, from here on, at a skip when the CI variable is set: CI's Docker runs every one of these
// scenarios (ADR-0346 plan step 8), and `go test` without -v would not show that one was skipped.
func mustRunOnCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" {
		return
	}
	t.Cleanup(func() {
		if t.Skipped() {
			t.Error("CI runs this scenario, so its skip fails it")
		}
	})
}

// workerImage builds, on the engine d reads, the image of the tests' Addon runtime deno2: funcd's nodejs22 runtime image
// (images/runtime/nodejs22) with the pinned shim, which speaks ADR-0346's transport, and names it by a digest the
// engine keeps for it, as an Addon names an image. containerd's image store keeps one for an image it built; Docker's
// classic store, CI's, only for one it pulled, so the first scenario there pulls it (pulledImage) and the next ones
// find that pull's digest.
func workerImage(t *testing.T, d dockerEngine) string {
	t.Helper()
	recipe, err := os.ReadFile(filepath.Join("..", "..", "images", "runtime", "nodejs22", "Dockerfile"))
	require.NoError(t, err)
	var from string
	for line := range strings.Lines(string(recipe)) {
		if strings.HasPrefix(line, "FROM ") {
			from = strings.TrimSpace(line)
		}
	}
	require.NotEmpty(t, from, "the nodejs22 recipe names its base")
	dockerfile := from + "\nCOPY shim.mjs /opt/funcd/shim.mjs\nCMD [\"/opt/funcd/shim.mjs\"]\n"
	var ctxTar bytes.Buffer
	tw := tar.NewWriter(&ctxTar)
	for _, f := range []struct {
		name string
		body []byte
	}{{"Dockerfile", []byte(dockerfile)}, {"shim.mjs", shimnode.Shim}} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}))
		_, err := tw.Write(f.body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	sum := sha256.Sum256(ctxTar.Bytes())
	repo := "funcd-test/dev-worker"
	tag := repo + ":" + hex.EncodeToString(sum[:6])
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://docker/build?"+url.Values{"t": {tag}, "q": {"1"}}.Encode(), &ctxTar)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := d.client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Empty(t, streamError(resp.Body), "build the Addon runtime image")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, b := d.get("/images/"+tag+"/json", nil)
	var img struct{ RepoDigests []string }
	require.NoError(t, json.Unmarshal(b, &img))
	for _, ref := range img.RepoDigests {
		if name, _, _ := strings.Cut(ref, "@"); name == repo || strings.HasSuffix(name, "/"+repo) {
			return ref
		}
	}
	return pulledImage(t, d, tag, repo)
}

// pulledImage serves tag, as the engine d saves it, from a registry on this host's 127.0.0.1, which Docker trusts over
// TLS without verifying it, and pulls it from there by digest, so that the engine keeps that digest for the image. An
// engine that cannot reach this host's loopback, as one in a VM, skips the test.
func pulledImage(t *testing.T, d dockerEngine, tag, repo string) string {
	t.Helper()
	resp, err := d.client.Get("http://docker/images/" + tag + "/get")
	require.NoError(t, err)
	img, err := imageregistry.Read(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err, "read the engine's save of %s", tag)
	srv := httptest.NewTLSServer(img.Handler(repo))
	defer srv.Close()
	ref := strings.TrimPrefix(srv.URL, "https://") + "/" + repo + "@" + img.Digest()
	pull, err := d.client.Post("http://docker/images/create?"+url.Values{"fromImage": {ref}}.Encode(), "", nil)
	require.NoError(t, err)
	defer func() { _ = pull.Body.Close() }()
	if why := streamError(pull.Body); pull.StatusCode != http.StatusOK || why != "" {
		t.Skipf("the engine keeps no digest for an image it built, and cannot pull one from this host's 127.0.0.1: %s %s",
			pull.Status, why)
	}
	return ref
}

// streamError is the error of the engine's reply r, read to its end: an API error's message, or the first error of a
// message stream; "" when there is none.
func streamError(r io.Reader) string {
	dec := json.NewDecoder(r)
	for {
		var m struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		switch err := dec.Decode(&m); {
		case errors.Is(err, io.EOF):
			return ""
		case err != nil:
			return err.Error()
		case m.Error != "" || m.Message != "":
			return cmp.Or(m.Error, m.Message)
		}
	}
}

// denoAddon ships deno2 from image for Functions and Jobs.
func denoAddon(image string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: Addon\nmetadata:\n  name: deno\nspec:\n  runtimes:\n    - name: deno2\n" +
		"      image: " + image + "\n      workloads:\n        - Function\n        - Job\n"
}

// denoFunction is the files of a Function name on deno2: its resource file (spec lines appended), its build and its
// contract file.
func denoFunction(name, spec, build string) map[string]string {
	return map[string]string{
		name + ".yaml":          fnDoc(name, "deno2", spec),
		name + ".mjs":           build,
		name + ".contract.yaml": strings.Replace(permissiveContract, "runtime: nodejs22", "runtime: deno2", 1),
	}
}

// workers names the session's worker containers, funcd-dev-worker-<key>-<hash> (ADR-0346 Decision 7), and not their
// helpers, whose names extend a worker's: on the host's network one runs beside the worker at each read of its port file.
func (d dockerEngine) workers(key string) []string {
	var out []string
	for _, n := range d.containers(key) {
		if hash, ok := strings.CutPrefix(n, workerPrefix+key+"-"); ok && len(hash) == 16 {
			out = append(out, n)
		}
	}
	return out
}

// workerVolumes names the session's worker volumes.
func (d dockerEngine) workerVolumes(key string) []string {
	var out []string
	for _, n := range d.volumes(key) {
		if strings.HasPrefix(n, workerPrefix) {
			out = append(out, n)
		}
	}
	return out
}

// workerEnv is the environment the worker container name was started with.
func (d dockerEngine) workerEnv(name string) map[string]string {
	_, b := d.get("/containers/"+name+"/json", nil)
	var c struct {
		Config     struct{ Env []string }
		HostConfig struct{ NetworkMode string }
	}
	require.NoError(d.t, json.Unmarshal(b, &c))
	env := map[string]string{"(network)": c.HostConfig.NetworkMode}
	for _, kv := range c.Config.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}

// waitPhase waits up to three minutes, a first pull's time, until Function name has phase, and returns it.
func waitPhase(t *testing.T, inst *devInstance, name string, phase v1.Phase) *v1.Function {
	t.Helper()
	var fn *v1.Function
	require.Eventually(t, func() bool {
		got, err := inst.client.Get(context.Background(), v1.KindFunction, devNamespace, v1.ObjectName(name))
		if err != nil {
			return false
		}
		fn = got.(*v1.Function)
		return fn.Status.Phase == phase
	}, 3*time.Minute, 100*time.Millisecond, "%s turns %s", name, phase)
	return fn
}

// runJob applies Job name on deno2 from build, written under the folder's jobs/, and returns its run name-1 once it has
// ended.
func runJob(t *testing.T, inst *devInstance, dir, name, build string) *v1.JobRun {
	t.Helper()
	writeProject(t, dir, map[string]string{"jobs/" + name + "/handler.mjs": build})
	return runJobImage(t, inst, name, "file://"+filepath.Join(dir, "jobs", name, "handler.mjs"), "")
}

// runJobImage applies Job name on deno2 from image at digest, whose runs need no approval (ADR-0348), and its run
// name-1, and returns the run once it has ended.
func runJobImage(t *testing.T, inst *devInstance, name, image, digest string) *v1.JobRun {
	t.Helper()
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindJob)
	job := obj.(*v1.Job)
	setMeta(&job.ObjectMeta, v1.ObjectName(name))
	job.Spec = v1.JobSpec{Runtime: "deno2", Handler: "handle", Image: image, ImageDigest: digest, Approval: v1.JobApproval{Bypass: true}}
	_, err := inst.client.Apply(ctx, job)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, gerr := inst.client.Get(ctx, v1.KindJob, devNamespace, v1.ObjectName(name))
		return gerr == nil && got.(*v1.Job).Status.Phase == v1.PhaseReady
	}, 3*time.Minute, 100*time.Millisecond, "Job %s turns Ready", name)
	robj, _ := v1.NewObject(v1.KindJobRun)
	run := robj.(*v1.JobRun)
	setMeta(&run.ObjectMeta, v1.ObjectName(name+"-1"))
	run.Spec = v1.JobRunSpec{Job: v1.ObjectName(name), Input: json.RawMessage(`{"name":"ada"}`)}
	_, err = inst.client.Apply(ctx, run)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, gerr := inst.client.Get(ctx, v1.KindJobRun, devNamespace, run.Name)
		if gerr != nil {
			return false
		}
		run = got.(*v1.JobRun)
		return run.Status.Phase.Ended()
	}, 3*time.Minute, 100*time.Millisecond, "run %s ends", run.Name)
	return run
}

// scenario: dev-addon-worker — given funcdctl dev on Docker and the Addon deno, whose deno2 lists Function and Job, a
// Function and a Job on deno2 run deno2's image on the engine: the Function turns Ready and answers, the run succeeds;
// a Function on nodejs22 beside them runs on the process driver.
func TestScenarioDevAddonWorker(t *testing.T) {
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	image := workerImage(t, d)
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(image)},
		denoFunction("greeter", "", "export function handle(ctx, event) { return { from: 'deno2', name: event.data.name }; }\n"),
		nodeFunction("api", "", okBuild)))
	cfg := devConfig{cacheDir: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, _ := startAddonDev(t, dir, cfg)

	waitPhase(t, inst, "greeter", v1.PhaseReady)
	resp, body := post(t, inst.gatewayURL+"/function/greeter", `{"data":{"name":"ada"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.JSONEq(t, `{"from":"deno2","name":"ada"}`, body)
	waitPhase(t, inst, "api", v1.PhaseReady)
	require.Len(t, d.workers(key), 1, "greeter's worker is a container; api's is a process")

	run := runJob(t, inst, dir, "hello", "export function handle() { return { done: true }; }\n")
	require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "%+v", run.Status.Conditions)
	require.JSONEq(t, `{"done":true}`, string(run.Status.Result))
	require.Eventually(t, func() bool { return len(d.workers(key)) == 1 }, time.Minute, 100*time.Millisecond,
		"the run's worker is removed with its run")
}

// engineBundles pushes, into an OCI layout of t's own, a deno2 bundle for each of linux/amd64 and linux/arm64 whose
// handler answers the platform it was built for, and indexes them; it returns the index's ref and digest.
func engineBundles(t *testing.T) (ref, digest string) {
	t.Helper()
	ctx := context.Background()
	layout := "oci-layout://" + filepath.Join(t.TempDir(), "layout")
	var sources []string
	for _, p := range []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64} {
		file := filepath.Join(t.TempDir(), "handler.mjs")
		require.NoError(t, os.WriteFile(file, []byte("export function handle() { return { bundle: '"+string(p)+"' }; }\n"), 0o600))
		source := layout + ":" + p.Arch()
		_, err := artifact.Push(ctx, source, file, []byte(`{"dialect":"https://json-schema.org/draft/2020-12/schema",`+
			`"input":{"type":"object"},"output":{"type":"object"}}`), "deno2", p)
		require.NoError(t, err)
		sources = append(sources, source)
	}
	ref = layout + ":both"
	digest, err := artifact.PushIndex(ctx, ref, sources)
	require.NoError(t, err)
	return ref, digest
}

// platform is the platform the engine runs its containers on.
func (d dockerEngine) platform() v1.OCIPlatform {
	_, b := d.get("/version", nil)
	var v struct{ Os, Arch string }
	require.NoError(d.t, json.Unmarshal(b, &v))
	return v1.OCIPlatform(v.Os + "/" + v.Arch)
}

// scenario: dev-addon-worker-platform (ADR-0145 Decision 6) — given funcdctl dev on Docker, a Function and a Job on
// deno2 whose image is an index of Linux bundles run the bundle of the engine's platform, on a Mac too, whose own
// platform the index lacks.
func TestScenarioDevAddonWorkerPlatform(t *testing.T) {
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	ctx := context.Background()
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(workerImage(t, d))}, nodeFunction("api", "", okBuild)))
	cfg := devConfig{cacheDir: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, _ := startAddonDev(t, dir, cfg)
	ref, digest := engineBundles(t)
	want := `{"bundle":"` + string(d.platform()) + `"}`

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	setMeta(&fn.ObjectMeta, "bundled")
	fn.Spec = v1.FunctionSpec{Runtime: "deno2", Handler: "handle", Image: ref, ImageDigest: digest, Replicas: 1}
	_, err := inst.client.Apply(ctx, fn)
	require.NoError(t, err)
	waitPhase(t, inst, "bundled", v1.PhaseReady)
	resp, body := post(t, inst.gatewayURL+"/function/bundled", `{"data":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.JSONEq(t, want, body)

	run := runJobImage(t, inst, "bundled-job", ref, digest)
	require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "%+v", run.Status.Conditions)
	require.JSONEq(t, want, string(run.Status.Result))
}

// scenario: dev-addon-worker-bindings — given such a worker that binds a KVStore, owns a Secret and links to pricing,
// its handler's ctx.kv, ctx.secret.put and ctx.invoke all reach funcd; a request on its listener with another token or
// none gets 403. That pricing never sees the token is the listener's (TestDevWorkerListenerChecksItsToken).
func TestScenarioDevAddonWorkerBindings(t *testing.T) {
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	image := workerImage(t, d)
	const secret = "apiVersion: funcd.io/v1alpha1\nkind: Secret\nmetadata:\n  name: tokens\nspec:\n  owner:\n    kind: Function\n" +
		"    name: bridge\n  data:\n    SEED: c2VlZA==\n"
	const spec = "  kv:\n    - name: notes\n      tables:\n        - name: seen\n" +
		"  bindings:\n    kv:\n      - alias: seen\n        store: notes\n        table: seen\n" +
		"    secret:\n      - tokens\n    links:\n      - alias: pricing\n        target: pricing\n" +
		"  secrets:\n    - name: tokens\n      keys:\n        - SEED\n"
	const bridge = `export async function handle(ctx) {
  try {
    await ctx.kv.put('seen', 'k', 'v1');
    const kv = await ctx.kv.getText('seen', 'k');
    await ctx.secret.put('tokens', 'WRITTEN', 'w1');
    const priced = await ctx.invoke('pricing', { data: { item: 7 } });
    return { kv, priced };
  } catch (e) { return { error: String((e && e.message) || e) }; }
}
`
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(image), "tokens.yaml": secret},
		denoFunction("bridge", spec, bridge),
		nodeFunction("pricing", "", "export function handle(ctx, event) { return { priced: event.data.item }; }\n")))
	cfg := devConfig{cacheDir: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, _ := startAddonDev(t, dir, cfg)
	waitPhase(t, inst, "bridge", v1.PhaseReady)
	waitPhase(t, inst, "pricing", v1.PhaseReady)

	resp, body := post(t, inst.gatewayURL+"/function/bridge", `{"data":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.JSONEq(t, `{"kv":"v1","priced":{"priced":7}}`, body, "the KV write and read, the Secret write and the link reach funcd")

	workers := d.workers(key)
	require.Len(t, workers, 1)
	env := d.workerEnv(workers[0])
	require.NotContains(t, env, "FUNCD_INVOKE_SOCKET")
	listener, err := url.Parse(env["FUNCD_INVOKE_URL"])
	require.NoError(t, err)
	at := "http://127.0.0.1:" + listener.Port() + "/kv/seen/k"
	call := func(token ...string) (int, string) {
		req, rerr := http.NewRequestWithContext(context.Background(), http.MethodGet, at, nil)
		require.NoError(t, rerr)
		for _, tok := range token {
			req.Header.Set("X-Funcd-Invoke-Token", tok)
		}
		r, derr := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		require.NoError(t, derr)
		defer func() { _ = r.Body.Close() }()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	code, got := call(env["FUNCD_INVOKE_TOKEN"])
	require.Equal(t, http.StatusOK, code, got)
	require.Equal(t, "v1", got, "the worker's token reads its binding")
	code, _ = call()
	require.Equal(t, http.StatusForbidden, code, "no token")
	code, _ = call(strings.Repeat("0", 64))
	require.Equal(t, http.StatusForbidden, code, "another token")
}

// scenario: dev-addon-worker-no-engine — given dev with neither engine, a Function on deno2 fails RuntimeUnavailable
// naming what dev tried, and a Function on nodejs22 runs.
func TestScenarioDevAddonWorkerNoEngine(t *testing.T) {
	mustRunOnCI(t)
	requireRuntime(t)
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon("registry.example.com/deno@sha256:" + strings.Repeat("a", 64))},
		denoFunction("greeter", "", okBuild), nodeFunction("api", "", okBuild)))
	inst, _ := startAddonDev(t, dir, devConfig{cacheDir: t.TempDir(), addonDrivers: noEngine()})
	fn := waitPhase(t, inst, "greeter", v1.PhaseFailed)
	var ready v1.Condition
	for _, c := range fn.Status.Conditions {
		if c.Type == "Ready" {
			ready = c
		}
	}
	require.Equal(t, "RuntimeUnavailable", ready.Reason)
	require.Contains(t, ready.Message, `runtime "deno2" is not available on this node: it is an Addon's image, which no worker driver of this server runs`)
	require.Contains(t, ready.Message, "/var/run/docker.sock")
	require.Contains(t, ready.Message, "colima nerdctl install")
	waitPhase(t, inst, "api", v1.PhaseReady)
}

// scenario: dev-addon-worker-output — when such a worker prints a line and its run exits 3, funcdctl job logs shows the
// line and the run records exit code 3.
func TestScenarioDevAddonWorkerOutput(t *testing.T) {
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	image := workerImage(t, d)
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(image)}, nodeFunction("api", "", okBuild)))
	cfg := devConfig{cacheDir: t.TempDir()}
	removeKeyAtCleanup(t, sock, sessionKey(t, cfg, dir))
	inst, _ := startAddonDev(t, dir, cfg)

	run := runJob(t, inst, dir, "loud", "console.log('loud-line');\nprocess.exit(3);\nexport function handle() { return {}; }\n")
	require.NotEqual(t, v1.JobRunSucceeded, run.Status.Phase)
	var ended string
	for _, c := range run.Status.Conditions {
		ended += c.Message + "\n"
	}
	require.Contains(t, ended, "exit code 3", "the run records the worker's exit code")
	var out bytes.Buffer
	require.Eventually(t, func() bool {
		out.Reset()
		root := newRootCmd(&out)
		root.SetArgs([]string{"job", "logs", string(run.Name), "--server", inst.controlURL, "--token", funcd.DevToken})
		return root.Execute() == nil && strings.Contains(out.String(), "loud-line")
	}, time.Minute, 500*time.Millisecond, "funcdctl job logs shows the worker's line")
}

// scenario: dev-addon-worker-reap — given dev killed while such a worker runs, the next start removes the session's
// worker containers and volumes, and --persist keeps the engines' volumes.
func TestScenarioDevAddonWorkerReap(t *testing.T) {
	if runDevChild(t) {
		return
	}
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	image := workerImage(t, d)
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(image), "echo-addon.yaml": echoAddon, "echo.yaml": echoObject},
		denoFunction("greeter", "", okBuild), nodeFunction("api", "", okBuild)))
	spec := "persist\n" + t.TempDir() + "\n" + dir
	cfg, _ := crashConfig(spec)
	state, err := devStateDir(cfg, dir)
	require.NoError(t, err)
	reapAtCleanup(t, state)
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)

	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), devCrashEnv+"="+spec)
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	engine := "funcd-dev-addon-" + key + "-default-echo"
	require.Eventually(t, func() bool { return len(d.workers(key)) == 1 && d.id(engine) != "" }, 3*time.Minute,
		200*time.Millisecond, "the killed run's worker and engine start")
	old := d.workers(key)[0]
	oldID := d.id(old)
	require.Contains(t, d.workerVolumes(key), old, "the worker's bundle volume")
	d.putMarker(old)
	require.NoError(t, child.Process.Kill())
	_ = child.Wait()
	require.Equal(t, oldID, d.id(old), "the killed run left its worker running")

	startAddonDev(t, dir, cfg)
	require.NotEqual(t, oldID, d.id(old), "the old worker's container is gone once dev has started")
	require.False(t, d.holds(key, old, "notes/marker"), "its volume too, under --persist: one of that name is a new worker's")
	require.Contains(t, d.volumes(key), engine, "--persist keeps the engine's volume")
}

// holds reports whether the session's volume vol, when it exists, holds file.
func (d dockerEngine) holds(key, vol, file string) bool {
	d.t.Helper()
	if !slices.Contains(d.volumes(key), vol) {
		return false
	}
	body, err := json.Marshal(map[string]json.RawMessage{
		"Image":      json.RawMessage(strconv.Quote(docker.HelperImage)),
		"Cmd":        json.RawMessage(`["test","-f","/d/` + file + `"]`),
		"HostConfig": json.RawMessage(`{"Mounts":[{"Type":"volume","Source":` + strconv.Quote(vol) + `,"Target":"/d","ReadOnly":true}]}`),
	})
	require.NoError(d.t, err)
	var created struct {
		ID string `json:"Id"`
	}
	require.NoError(d.t, json.Unmarshal(d.post("/containers/create", body), &created))
	defer d.post("/containers/"+created.ID+"/remove", nil)
	d.post("/containers/"+created.ID+"/start", nil)
	var waited struct{ StatusCode int }
	require.NoError(d.t, json.Unmarshal(d.post("/containers/"+created.ID+"/wait", nil), &waited))
	return waited.StatusCode == 0
}

// scenario: dev-addon-worker-linux — given dev on a Linux host, such a worker shares the host's network and listens on
// the host's loopback only. The rootless half is the driver's (TestWorkersRefuseAnEngineWithoutTheHostsNetwork) and
// the gate's (TestDevAddonWorkerGateNamesWhy).
func TestScenarioDevAddonWorkerLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("a worker shares the host's network on a Linux host only")
	}
	mustRunOnCI(t)
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	image := workerImage(t, d)
	dir := devProject(t, merge(map[string]string{"addon.yaml": denoAddon(image)}, denoFunction("greeter", "", okBuild),
		nodeFunction("api", "", okBuild)))
	cfg := devConfig{cacheDir: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, _ := startAddonDev(t, dir, cfg)
	waitPhase(t, inst, "greeter", v1.PhaseReady)
	workers := d.workers(key)
	require.Len(t, workers, 1)
	env := d.workerEnv(workers[0])
	if env["(network)"] != "host" {
		t.Skipf("this engine runs in a VM, or is rootless: the worker's network is %q", env["(network)"])
	}
	require.NotContains(t, env, "FUNCD_PORT", "the shim binds the loopback, at a port of its own")
	require.Equal(t, "/run/funcd/port", env["FUNCD_PORTFILE"])
	require.True(t, strings.HasPrefix(env["FUNCD_INVOKE_URL"], "http://127.0.0.1:"), env["FUNCD_INVOKE_URL"])
	resp, body := post(t, inst.gatewayURL+"/function/greeter", `{"data":{}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
}
