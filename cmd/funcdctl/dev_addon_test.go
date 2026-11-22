//go:build dev

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/provider/docker"
	"github.com/pyvvo/funcd/internal/provider/nerdctl"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// whoamiImage is the engine of the tests' EchoService add-on: a small HTTP server that answers /health.
const whoamiImage = "traefik/whoami@sha256:43a68d10b9dfcfc3ffbfe4dd42100dc9aeaf29b3a5636c856337a5940f1b4f1c"

// echoAddon registers EchoService over whoami, with storage, so each object has a volume.
const echoAddon = `apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: whoami
spec:
  kind: EchoService
  image: ` + whoamiImage + `
  port: 80
  readiness:
    path: /health
  storage:
    path: /data
    uid: 10001
    gid: 10001
`

const echoObject = `apiVersion: funcd.io/v1alpha1
kind: EchoService
metadata:
  name: echo
spec: {}
`

// rauthyAddon is experiment 0025's rauthy Addon, its image published by pyvvo/funcd-rauthy.
const rauthyAddon = `apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: rauthy
spec:
  kind: IdentityProvider
  image: ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5edcb27399e23f8386fecb03b13f909966d2c3c4708095e2c31ccfe2d
  runtimes:
    - containerd
  port: 8080
  basePath: /auth/v1
  privatePaths:
    - /auth/v1/clients_dyn
  readiness:
    path: /auth/v1/ping
  storage:
    path: /app/data
    uid: 10001
    gid: 10001
  config:
    type: object
    additionalProperties: false
    properties:
      publicHost:
        type: string
      rpId:
        type: string
      origin:
        type: string
    required:
      - publicHost
      - rpId
      - origin
  env:
    HQL_NODE_ID: "1"
    LISTEN_SCHEME: http
    COOKIE_MODE: danger-insecure
    PUB_URL: ${{ config.publicHost }}
    RP_ID: ${{ config.rpId }}
    RP_ORIGIN: ${{ config.origin }}
  secretKeys:
    - HQL_SECRET_RAFT
    - HQL_SECRET_API
    - ENC_KEYS
    - ENC_KEY_ACTIVE
    - BOOTSTRAP_ADMIN_EMAIL
    - BOOTSTRAP_ADMIN_PASSWORD_PLAIN
`

const rauthyLogin = `apiVersion: funcd.io/v1alpha1
kind: IdentityProvider
metadata:
  name: login
spec:
  config:
    publicHost: login.localhost:18091
    rpId: login.localhost
    origin: http://login.localhost:18091
  bindings:
    secret:
      - rauthy-keys
  ingress:
    host: login.localhost
`

// addonProject is a dev folder of a Function, which dev needs to run, the EchoService add-on and its object echo.
func addonProject(t *testing.T) string {
	t.Helper()
	return devProject(t, merge(nodeFunction("api", "", okBuild), map[string]string{"addon.yaml": echoAddon, "echo.yaml": echoObject}))
}

// noEngine finds neither container engine, so every add-on object waits RuntimeUnavailable.
func noEngine() devAddonDrivers {
	return devAddonDrivers{
		socket: func(context.Context) (string, error) {
			return "", fault.Unavailablef("docker.Socket", "no Docker engine answers: tried the system socket /var/run/docker.sock (absent)")
		},
		nerdctl: func() (string, error) {
			return "", fault.Unavailablef("nerdctl.Find", "nerdctl is not on PATH: with colima's containerd runtime (colima start "+
				"--runtime containerd), run colima nerdctl install")
		},
	}
}

// startAddonDev starts funcdctl dev over dir with cfg until the test ends; its stop is returned.
func startAddonDev(t *testing.T, dir string, cfg devConfig) (*devInstance, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, cfg)
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
	return inst, stop
}

// waitObject waits until the add-on object kind/name meets cond and returns it.
func waitObject(t *testing.T, inst *devInstance, kind v1.Kind, name v1.ObjectName, what string, cond func(*v1.AddonObject) bool) *v1.AddonObject {
	t.Helper()
	var o *v1.AddonObject
	if !assert.Eventually(t, func() bool {
		got, err := inst.client.Get(context.Background(), kind, devNamespace, name)
		if err != nil {
			return false
		}
		o = got.(*v1.AddonObject)
		return cond(o)
	}, 3*time.Minute, 200*time.Millisecond, "%s %s: %s", kind, name, what) {
		if o != nil {
			t.Logf("last status: %+v", o.Status)
		}
		t.FailNow()
	}
	return o
}

func ready(o *v1.AddonObject) bool { return o.Status.Phase == v1.PhaseReady }

// sessionKey is the key of dev over dir with cfg (ADR-0334 Decision 6).
func sessionKey(t *testing.T, cfg devConfig, dir string) string {
	t.Helper()
	state, err := devStateDir(cfg, dir)
	require.NoError(t, err)
	key, err := devSessionKey(state)
	require.NoError(t, err)
	return key
}

// requireDocker returns the socket of a Docker engine that answers, or skips naming each socket tried.
func requireDocker(t *testing.T) string {
	t.Helper()
	sock, err := docker.Socket(context.Background())
	if err != nil {
		t.Skipf("the add-on scenarios run a real Docker engine: %v", err)
	}
	return sock
}

// dockerEngine reads the Docker engine on sock for a test, at the engine's own API version.
type dockerEngine struct {
	t      *testing.T
	client *http.Client
}

func newDockerEngine(t *testing.T, sock string) dockerEngine {
	return dockerEngine{t: t, client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}}
}

func (d dockerEngine) get(p string, q url.Values) (int, []byte) {
	d.t.Helper()
	resp, err := d.client.Get("http://docker" + p + "?" + q.Encode())
	require.NoError(d.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(d.t, err)
	return resp.StatusCode, b
}

func labelQuery(key string) url.Values {
	f, _ := json.Marshal(map[string][]string{"label": {docker.KeyLabel + "=" + key}})
	return url.Values{"all": {"true"}, "filters": {string(f)}}
}

// containers names the containers of key.
func (d dockerEngine) containers(key string) []string {
	_, b := d.get("/containers/json", labelQuery(key))
	var list []struct{ Names []string }
	require.NoError(d.t, json.Unmarshal(b, &list))
	var out []string
	for _, c := range list {
		out = append(out, strings.TrimPrefix(c.Names[0], "/"))
	}
	return out
}

// volumes names the volumes of key.
func (d dockerEngine) volumes(key string) []string {
	_, b := d.get("/volumes", labelQuery(key))
	var list struct{ Volumes []struct{ Name string } }
	require.NoError(d.t, json.Unmarshal(b, &list))
	var out []string
	for _, v := range list.Volumes {
		out = append(out, v.Name)
	}
	return out
}

// id is the id of the container name, "" when there is none.
func (d dockerEngine) id(name string) string {
	code, b := d.get("/containers/"+name+"/json", nil)
	if code == http.StatusNotFound {
		return ""
	}
	var c struct {
		ID string `json:"Id"`
	}
	require.NoError(d.t, json.Unmarshal(b, &c))
	return c.ID
}

// killSignals are the signals the engine sent the container id since then.
func (d dockerEngine) killSignals(id string, since time.Time) []string {
	f, _ := json.Marshal(map[string][]string{"container": {id}, "event": {"kill"}})
	_, b := d.get("/events", url.Values{"since": {strconv.FormatInt(since.Unix(), 10)},
		"until": {strconv.FormatInt(time.Now().Unix()+1, 10)}, "filters": {string(f)}})
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var ev struct {
			Actor struct{ Attributes map[string]string }
		}
		require.NoError(d.t, json.Unmarshal(sc.Bytes(), &ev))
		out = append(out, ev.Actor.Attributes["signal"])
	}
	return out
}

// removeKeyAtCleanup removes, at the test's end, every container and volume of key.
func removeKeyAtCleanup(t *testing.T, sock, key string) {
	t.Cleanup(func() {
		if rt, err := docker.New(context.Background(), sock, key, false, slog.New(slog.DiscardHandler)); err == nil {
			rt.StopAll()
		}
	})
}

// serveFakeDocker answers, on a unix socket it returns, what docker.New asks: a ping, and no container or volume.
func serveFakeDocker(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.53")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, "[]")
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			_, _ = io.WriteString(w, `{"Volumes":[]}`)
		default:
			_, _ = io.WriteString(w, "OK")
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// scenario: dev-addon-runtime-choice — given both Docker and nerdctl, dev uses Docker; given dev.addons.runtime:
// containerd, nerdctl; given neither, the object stays Pending, RuntimeUnavailable, naming what dev looked for. Another
// value of the key is refused at start.
func TestScenarioDevAddonRuntimeChoice(t *testing.T) {
	ctx := context.Background()
	sock := serveFakeDocker(t)
	nerd := filepath.Join(t.TempDir(), "nerdctl")
	require.NoError(t, os.WriteFile(nerd, []byte("#!/bin/sh\nexit 0\n"), 0o700), "a nerdctl that lists nothing")
	both := devAddonDrivers{socket: func(context.Context) (string, error) { return sock, nil }, nerdctl: func() (string, error) { return nerd, nil }}
	logger := slog.New(slog.DiscardHandler)
	const key = "0123456789abcdef"
	require.IsType(t, &docker.Runtime{}, chooseAddonRuntime(ctx, devConfig{addonDrivers: both}, key, logger), "Docker first")
	require.IsType(t, &nerdctl.Runtime{}, chooseAddonRuntime(ctx, devConfig{addonDrivers: both, addonsRuntime: devAddonsContainerd}, key, logger))
	forced := chooseAddonRuntime(ctx, devConfig{addonDrivers: devAddonDrivers{socket: noEngine().socket, nerdctl: both.nerdctl},
		addonsRuntime: devAddonsDocker}, key, logger)
	require.IsType(t, devAddonsUnavailable{}, forced, "docker forced, and no Docker engine answers")
	require.NotContains(t, forced.(devAddonsUnavailable).why, "nerdctl")

	for _, body := range []string{"dev:\n  addons:\n    runtime: podman\n", "dev:\n  addons:\n    engine: docker\n", "dev:\n  addons: docker\n"} {
		_, err := decodeDevFile("funcdctl dev", []byte(body))
		require.Equal(t, fault.Invalid, fault.KindOf(err), body)
	}
	keys, err := decodeDevFile("funcdctl dev", []byte("dev:\n  addons:\n    runtime: containerd\n"))
	require.NoError(t, err)
	require.Equal(t, devAddonsContainerd, keys.Addons.Runtime)

	requireRuntime(t)
	inst, _ := startAddonDev(t, addonProject(t), devConfig{cacheDir: t.TempDir(), addonDrivers: noEngine()})
	o := waitObject(t, inst, "EchoService", "echo", "Pending, RuntimeUnavailable", func(o *v1.AddonObject) bool {
		return len(o.Status.Conditions) > 0 && o.Status.Conditions[0].Reason == reasonRuntimeUnavailable
	})
	require.Equal(t, v1.PhasePending, o.Status.Phase)
	msg := o.Status.Conditions[0].Message
	require.Contains(t, msg, "/var/run/docker.sock")
	require.Contains(t, msg, "colima nerdctl install")
}

// scenario: dev-addon-register — an Addon in the folder or in App mode's apply list is registered, with no namespace,
// before any object of its kind; funcdctl applies one with the dev admin token, and dev's developer token gets 403.
func TestScenarioDevAddonRegister(t *testing.T) {
	requireRuntime(t)
	ctx := context.Background()
	inst, _ := startAddonDev(t, addonProject(t), devConfig{cacheDir: t.TempDir(), addonDrivers: noEngine()})
	requireRegistered(t, inst)

	second := filepath.Join(t.TempDir(), "second.yaml")
	require.NoError(t, os.WriteFile(second, []byte(strings.ReplaceAll(strings.ReplaceAll(echoAddon, "name: whoami", "name: whoami-2"),
		"kind: EchoService", "kind: EchoServiceTwo")), 0o600))
	apply := func(token string) (string, error) {
		var out bytes.Buffer
		root := newRootCmd(&out)
		root.SetArgs([]string{"apply", "-f", second, "--server", inst.controlURL, "--token", token})
		err := root.Execute()
		return out.String(), err
	}
	_, err := apply(funcd.DevToken)
	require.ErrorContains(t, err, "may not act on cluster-scoped kind", "dev's developer token gets 403")
	out, err := apply(funcd.DevAdminToken)
	require.NoError(t, err)
	require.Contains(t, out, "Addon/whoami-2")
	admin, err := sdk.New(inst.controlURL, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	_, err = admin.Get(ctx, v1.KindAddon, "", "whoami-2")
	require.NoError(t, err)

	t.Run("app-mode", func(t *testing.T) {
		requireApp(t)
		t.Setenv("TODO_API_TOKEN", todoToken)
		root := appCopy(t, "todo")
		require.NoError(t, os.WriteFile(filepath.Join(root, "addon.yaml"), []byte(echoAddon+"---\n"+echoObject), 0o600))
		r := startApp(t, root, todoCfg(func(c *devConfig) { c.apply, c.addonDrivers = []string{"addon.yaml"}, noEngine() }))
		requireRegistered(t, r.inst)
	})
}

// requireRegistered asserts that dev registered the whoami Addon with no namespace or resource group, and applied echo,
// an object of its kind, which only its Addon admits.
func requireRegistered(t *testing.T, inst *devInstance) {
	t.Helper()
	ctx := context.Background()
	admin, err := sdk.New(inst.controlURL, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	a, err := admin.Get(ctx, v1.KindAddon, "", "whoami")
	require.NoError(t, err)
	require.Empty(t, a.GetNamespace())
	require.Empty(t, a.GetObjectMeta().ResourceGroup)
	require.Equal(t, devManagedBy, a.GetObjectMeta().Tags[devManagedTag])
	_, err = inst.client.Get(ctx, "EchoService", devNamespace, "echo")
	require.NoError(t, err, "the object of its kind is applied after the Addon")
}

// scenario: dev-addon-docker — given Docker's socket and the Addon rauthy, the IdentityProvider login applied under
// funcdctl dev turns Ready, and GET /auth/v1/ping on login.localhost answers 200.
func TestScenarioDevAddonDocker(t *testing.T) {
	requireRuntime(t)
	sock := requireDocker(t)
	keys, err := os.ReadFile(filepath.Join("..", "..", "e2e", "fixtures", "addon-rauthy-keys.yaml"))
	require.NoError(t, err)
	dir := devProject(t, merge(nodeFunction("api", "", okBuild),
		map[string]string{"rauthy.yaml": rauthyAddon, "rauthy-keys.yaml": string(keys), "login.yaml": rauthyLogin}))
	cfg := devConfig{cacheDir: t.TempDir()}
	removeKeyAtCleanup(t, sock, sessionKey(t, cfg, dir))
	inst, _ := startAddonDev(t, dir, cfg)
	waitObject(t, inst, "IdentityProvider", "login", "Ready", ready)
	require.Eventually(t, func() bool {
		code, _ := get(t, inst.gatewayURL+"/auth/v1/ping", "login.localhost")
		return code == http.StatusOK
	}, time.Minute, 200*time.Millisecond, "the edge serves rauthy's ping at login.localhost")
}

// scenario: dev-addon-data — when dev stops, each engine gets SIGTERM and the grace, and its container and volume are
// removed; with --persist the volume stays, and the next run starts on it.
func TestScenarioDevAddonData(t *testing.T) {
	requireRuntime(t)
	sock := requireDocker(t)
	ctx := context.Background()
	d := newDockerEngine(t, sock)

	dir := addonProject(t)
	cfg := devConfig{cacheDir: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, stop := startAddonDev(t, dir, cfg)
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	require.NoError(t, inst.addons.Put(ctx, devNamespace, "echo", "notes", "marker", strings.NewReader("kept"), 10001, 10001))
	id := d.id("funcd-dev-addon-" + key + "-default-echo")
	since := time.Now().Add(-time.Second)
	stop()
	require.Empty(t, d.containers(key), "the engine's container is removed")
	require.Empty(t, d.volumes(key), "its volume too, without --persist")
	require.Contains(t, d.killSignals(id, since), "15", "the stop sends SIGTERM")

	cfg = devConfig{persist: true, persistTo: t.TempDir()}
	key = sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	inst, stop = startAddonDev(t, dir, cfg)
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	require.NoError(t, inst.addons.Put(ctx, devNamespace, "echo", "notes", "marker", strings.NewReader("kept"), 10001, 10001))
	stop()
	require.Empty(t, d.containers(key))
	require.Equal(t, []string{"funcd-dev-addon-" + key + "-default-echo"}, d.volumes(key), "--persist keeps the volume")
	inst, _ = startAddonDev(t, dir, cfg)
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	requireMarker(t, inst, true)
}

// requireMarker asserts whether echo's volume holds the marker an earlier run put in it.
func requireMarker(t *testing.T, inst *devInstance, kept bool) {
	t.Helper()
	file, body, err := inst.addons.Newest(context.Background(), devNamespace, "echo", "notes", 0)
	if !kept {
		require.Equal(t, fault.NotFound, fault.KindOf(err), "the new engine starts empty: %v", err)
		return
	}
	require.NoError(t, err)
	b, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "marker", file)
	require.Equal(t, "kept", string(b), "the next run starts on its data")
}

// scenario: dev-addon-crash-reaped — given a dev run killed with SIGKILL while echo's engine runs, the next start in that
// project removes the old container before anything converges; without --persist its volume goes too and echo starts
// empty; with --persist the new engine starts on its data.
func TestScenarioDevAddonCrashReaped(t *testing.T) {
	if runDevChild(t) {
		return
	}
	requireRuntime(t)
	sock := requireDocker(t)
	for _, mode := range []string{"cache", "persist"} {
		t.Run(mode, func(t *testing.T) {
			if runDevChild(t) {
				return
			}
			d := newDockerEngine(t, sock)
			dir := addonProject(t)
			spec := mode + "\n" + t.TempDir() + "\n" + dir
			cfg, _ := crashConfig(spec)
			state, err := devStateDir(cfg, dir)
			require.NoError(t, err)
			reapAtCleanup(t, state)
			key := sessionKey(t, cfg, dir)
			removeKeyAtCleanup(t, sock, key)
			name := "funcd-dev-addon-" + key + "-default-echo"

			child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
			child.Env = append(os.Environ(), devCrashEnv+"="+spec)
			require.NoError(t, child.Start())
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			var old string
			require.Eventually(t, func() bool { old = d.id(name); return old != "" }, 3*time.Minute, 200*time.Millisecond,
				"the killed run's engine starts")
			d.putMarker(name)
			require.NoError(t, child.Process.Kill())
			_ = child.Wait()
			require.Equal(t, old, d.id(name), "the killed run left its engine running")

			inst, _ := startAddonDev(t, dir, cfg)
			status, _ := d.get("/containers/"+old+"/json", nil)
			require.Equal(t, http.StatusNotFound, status, "the old container is gone once dev has started")
			waitObject(t, inst, "EchoService", "echo", "Ready", ready)
			requireMarker(t, inst, mode == "persist")
		})
	}
}

// putMarker writes notes/marker, kept, owned by the engine's user, into the volume name, with a container of the
// test's own: the volume is another process's session's.
func (d dockerEngine) putMarker(name string) {
	d.t.Helper()
	body, err := json.Marshal(map[string]json.RawMessage{
		"Image":      json.RawMessage(strconv.Quote(docker.HelperImage)),
		"Cmd":        json.RawMessage(`["sh","-c","mkdir -p /d/notes && printf kept > /d/notes/marker && chown -R 10001:10001 /d/notes"]`),
		"HostConfig": json.RawMessage(`{"Mounts":[{"Type":"volume","Source":` + strconv.Quote(name) + `,"Target":"/d"}]}`),
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
	require.Zero(d.t, waited.StatusCode)
}

func (d dockerEngine) post(p string, body []byte) []byte {
	d.t.Helper()
	if strings.HasSuffix(p, "/remove") {
		req, err := http.NewRequest(http.MethodDelete, "http://docker"+strings.TrimSuffix(p, "/remove")+"?force=true", nil)
		require.NoError(d.t, err)
		resp, err := d.client.Do(req)
		require.NoError(d.t, err)
		_ = resp.Body.Close()
		return nil
	}
	resp, err := d.client.Post("http://docker"+p, "application/json", bytes.NewReader(body))
	require.NoError(d.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(d.t, err)
	require.Less(d.t, resp.StatusCode, http.StatusMultipleChoices, "POST %s: %s", p, b)
	return b
}

// scenario: dev-addon-two-projects — two projects that each run echo under dev at once each have their own container and
// volume, and stopping one leaves the other serving.
func TestScenarioDevAddonTwoProjects(t *testing.T) {
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	start := func() (*devInstance, func(), string) {
		dir := addonProject(t)
		cfg := devConfig{cacheDir: t.TempDir()}
		key := sessionKey(t, cfg, dir)
		removeKeyAtCleanup(t, sock, key)
		inst, stop := startAddonDev(t, dir, cfg)
		return inst, stop, key
	}
	a, stopA, keyA := start()
	b, _, keyB := start()
	waitObject(t, a, "EchoService", "echo", "Ready", ready)
	echoB := waitObject(t, b, "EchoService", "echo", "Ready", ready)
	require.NotEqual(t, keyA, keyB)
	require.Equal(t, []string{"funcd-dev-addon-" + keyA + "-default-echo"}, d.containers(keyA))
	require.Equal(t, []string{"funcd-dev-addon-" + keyB + "-default-echo"}, d.containers(keyB))
	require.Equal(t, []string{"funcd-dev-addon-" + keyB + "-default-echo"}, d.volumes(keyB))

	stopA()
	require.Empty(t, d.containers(keyA))
	require.Len(t, d.containers(keyB), 1, "the other project's engine is untouched")
	code, _ := get(t, "http://"+echoB.Status.Address+"/health", "")
	require.Equal(t, http.StatusOK, code, "the other project's engine still serves")
}

// ADR-0334 Decision 5: a prune deletes an add-on object before every Addon, and both with the admin token; the rest
// with dev's developer token.
func TestDevPruneDeletesObjectsBeforeTheirAddon(t *testing.T) {
	t.Parallel()
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes = append(deletes, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+" "+r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	admin, err := sdk.New(srv.URL, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	decode := func(doc string) v1.Object {
		o, derr := sdk.DecodeManifest([]byte(doc))
		require.NoError(t, derr)
		return o
	}
	addon, echo := decode(echoAddon), decode(echoObject)
	echo.GetObjectMeta().Namespace = devNamespace
	secret := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindSecret}}
	secret.Name, secret.Namespace = "keys", devNamespace
	kept := pruneRemoved(context.Background(), c, admin, []v1.Object{addon, secret, echo}, nil, true, slog.New(slog.DiscardHandler))
	require.Empty(t, kept)
	require.Equal(t, []string{
		funcd.DevAdminToken + " /apis/funcd.io/v1alpha1/namespaces/default/echoservices/echo",
		funcd.DevToken + " /apis/funcd.io/v1alpha1/namespaces/default/secrets/keys",
		funcd.DevAdminToken + " /apis/funcd.io/v1alpha1/addons/whoami",
	}, deletes)
}

// scenario: addon-template-dev — dev applies the template's addon.yaml with its admin token, then the App, whose
// addons part it holds; a changed addon.yaml is applied again at the next reload, and removing it deletes the Addon.
func TestScenarioAddonTemplateDev(t *testing.T) {
	requireRuntime(t)
	requireApp(t)
	ctx := context.Background()
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	addonPath := filepath.Join(root, "app", template.AddonFile)
	require.NoError(t, os.WriteFile(addonPath, []byte(echoAddon), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app", "resources", "echo.yaml"),
		[]byte("addons:\n  - name: ${{ app.name + \"-echo\" }}\n    kind: EchoService\n"), 0o600))
	r := startApp(t, root, todoCfg(func(c *devConfig) { c.addonDrivers = noEngine() }))
	admin, err := sdk.New(r.inst.controlURL, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	readiness := func() string {
		obj, err := admin.Get(ctx, v1.KindAddon, "", "whoami")
		if err != nil {
			return ""
		}
		return obj.(*v1.Addon).Spec.Readiness.Path
	}
	require.Equal(t, "/health", readiness(), "the template's Addon is registered before the App")
	require.Eventually(t, func() bool {
		_, err := r.inst.client.Get(ctx, "EchoService", devNamespace, "todo-echo")
		return err == nil
	}, time.Minute, 100*time.Millisecond, "the App writes its addons part")

	require.NoError(t, os.WriteFile(addonPath, []byte(strings.Replace(echoAddon, "path: /health", "path: /ready", 1)), 0o600))
	require.Eventually(t, func() bool { return readiness() == "/ready" }, time.Minute, 100*time.Millisecond,
		"the reload applies the changed addon.yaml")

	require.NoError(t, os.Remove(addonPath))
	require.Eventually(t, func() bool {
		_, err := admin.Get(ctx, v1.KindAddon, "", "whoami")
		return fault.KindOf(err) == fault.NotFound
	}, time.Minute, 100*time.Millisecond, "removing addon.yaml deletes the Addon")
}
