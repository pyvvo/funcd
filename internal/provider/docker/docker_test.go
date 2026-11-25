package docker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
)

// whoami is the engine the tests run: a small HTTP server that answers /health, run as its image's user.
const whoami = "traefik/whoami@sha256:43a68d10b9dfcfc3ffbfe4dd42100dc9aeaf29b3a5636c856337a5940f1b4f1c"

// The method set of ADR-0333's workload.AddonFiles, which the Runtime implements structurally.
var _ interface {
	Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (string, io.ReadCloser, error)
	Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, r io.Reader, uid, gid int) error
	Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error
} = (*Runtime)(nil)

// requireDocker returns the socket of a Docker engine that answers, or skips naming each socket tried.
func requireDocker(t *testing.T) string {
	t.Helper()
	sock, err := Socket(context.Background())
	if err != nil {
		t.Skipf("these tests run a real Docker engine: %v", err)
	}
	return sock
}

// testKey is a session key no other test or session holds.
func testKey(t *testing.T) string {
	t.Helper()
	var b [8]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return hex.EncodeToString(b[:])
}

// newRuntime is a Runtime of a fresh key over the Docker engine; the test's end removes all it made.
func newRuntime(t *testing.T, sock string, persist bool) (*Runtime, string) {
	t.Helper()
	key := testKey(t)
	rt, err := New(context.Background(), sock, key, persist, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { cleanupKey(t, sock, key) })
	return rt, key
}

// cleanupKey removes every container and volume of key.
func cleanupKey(t *testing.T, sock, key string) {
	t.Helper()
	rt, err := New(context.Background(), sock, key, false, slog.New(slog.DiscardHandler))
	if err == nil {
		rt.StopAll()
	}
}

func whoamiSpec(ns, name string, storage bool) provider.ProviderSpec {
	s := provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)},
		Image: whoami, Port: 80, Env: map[string]string{"WHOAMI_NAME": name}, Replicas: 1,
		Readiness: provider.ReadinessProbe{Path: "/health", ExpectStatus: http.StatusOK}}
	if storage {
		s.Storage = &provider.StorageSpec{Path: "/data", UID: 10001, GID: 10001}
	}
	return s
}

// converge converges spec until it is Ready.
func converge(t *testing.T, rt *Runtime, spec provider.ProviderSpec) provider.ProviderStatus {
	t.Helper()
	var st provider.ProviderStatus
	require.Eventually(t, func() bool {
		var err error
		st, err = rt.Converge(context.Background(), spec)
		require.NoError(t, err)
		return st.Ready
	}, 2*time.Minute, 200*time.Millisecond, "the engine of %s/%s turns Ready", spec.Ref.Namespace, spec.Ref.Name)
	return st
}

func api(rt *Runtime) *engineAPI { return rt.eng.(*engineAPI) }

// containerConfig is what the tests read back of an engine's container.
type containerConfig struct {
	ID     string `json:"Id"`
	Config struct {
		Labels      map[string]string `json:"Labels"`
		StopSignal  string            `json:"StopSignal"`
		StopTimeout *int              `json:"StopTimeout"`
		User        string            `json:"User"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		PortBindings map[string][]portBinding `json:"PortBindings"`
		Mounts       []mountBody              `json:"Mounts"`
	} `json:"HostConfig"`
}

func inspectConfig(t *testing.T, rt *Runtime, name string) containerConfig {
	t.Helper()
	_, b, err := api(rt).send(context.Background(), http.MethodGet, "/containers/"+name+"/json", nil, nil)
	require.NoError(t, err)
	var c containerConfig
	require.NoError(t, json.Unmarshal(b, &c))
	return c
}

// shell runs a helper over ref's volume and returns its output.
func shell(t *testing.T, rt *Runtime, ref provider.ProviderRef, script string, args ...string) string {
	t.Helper()
	out, err := rt.exec(context.Background(), rt.helper(ref, Mount{Volume: rt.name(ref), Target: helperRoot}),
		append([]string{"sh", "-c", script, "sh"}, args...)...)
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// ADR-0334 Decision 1: the sockets in their order, the active context's endpoint, and an Unavailable naming each one
// tried; the first that answers wins.
func TestSocketOrder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := map[string]string{"FUNCD_DEV_DOCKER_HOST": "unix:///first.sock", "DOCKER_HOST": "tcp://10.0.0.1:2375"}
	lookup := func(k string) string { return env[k] }
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".docker"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(`{"currentContext":"colima"}`), 0o600))
	sum := sha256.Sum256([]byte("colima"))
	meta := filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(meta, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(meta, "meta.json"),
		[]byte(`{"Name":"colima","Endpoints":{"docker":{"Host":"unix:///context.sock"}}}`), 0o600))
	var got []string
	for _, c := range candidates(lookup, home, "/system.sock") {
		got = append(got, c.from+"="+c.path)
	}
	require.Equal(t, []string{
		"FUNCD_DEV_DOCKER_HOST=/first.sock",
		"DOCKER_HOST tcp://10.0.0.1:2375=",
		"the Docker context colima=/context.sock",
		"colima=" + filepath.Join(home, ".colima", "default", "docker.sock"),
		"Docker Desktop=" + filepath.Join(home, ".docker", "run", "docker.sock"),
		"the system socket=/system.sock",
	}, got)

	env["DOCKER_CONTEXT"] = "default"
	for _, c := range candidates(lookup, home, "/system.sock") {
		require.NotContains(t, c.from, "context", "the default context is DOCKER_HOST or the system socket")
	}

	_, err := findSocket(context.Background(), lookup, home, filepath.Join(home, "system.sock"))
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	for _, want := range []string{"FUNCD_DEV_DOCKER_HOST /first.sock (absent)", "not a unix:// address", "colima", "Docker Desktop",
		"the system socket"} {
		require.ErrorContains(t, err, want)
	}

	sock := filepath.Join(shortDir(t), "d.sock")
	serveDockerPing(t, sock)
	env["DOCKER_HOST"] = "unix://" + sock
	got1, err := findSocket(context.Background(), lookup, home, "/system.sock")
	require.NoError(t, err)
	require.Equal(t, sock, got1, "the first socket that answers, after one that does not")
}

// shortDir is a directory whose socket paths stay within the unix limit.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveDockerPing answers GET /_ping on a unix socket at p, as an engine of API 1.53 does.
func serveDockerPing(t *testing.T, p string) {
	t.Helper()
	l, err := net.Listen("unix", p)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.53")
		_, _ = io.WriteString(w, "OK")
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// ADR-0334 Decision 6: a key is 16 hex digits, so no Runtime reaps by a label that is not one; the Engine API version
// is the engine's own, at most the driver's.
func TestSessionKeyAndVersion(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", "0123", "0123456789ABCDEF", "0123456789abcdefg"} {
		_, err := NewRuntime(context.Background(), nil, key, false, nil)
		require.Equal(t, fault.Invalid, fault.KindOf(err), key)
	}
	require.Equal(t, maxAPIVersion, apiVersion("1.53"))
	require.Equal(t, "1.41", apiVersion("1.41"))
	require.Equal(t, maxAPIVersion, apiVersion(""), "an engine that names no version")
}

// ADR-0145 Decision 6: the engine's platform is the one its version names; an answer that names none is an error.
func TestEnginePlatform(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]v1.OCIPlatform{
		`{"Os":"linux","Arch":"arm64","Version":"29.2.1"}`: v1.PlatformLinuxARM64,
		`{"Os":"linux"}`: "",
		`not json`:       "",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/version", r.URL.Path)
			_, _ = io.WriteString(w, body)
		}))
		got, err := (&engineAPI{http: srv.Client(), base: srv.URL}).Platform(context.Background())
		srv.Close()
		require.Equal(t, want, got, body)
		if want == "" {
			require.Equal(t, fault.Internal, fault.KindOf(err), body)
			require.ErrorContains(t, err, "version")
		} else {
			require.NoError(t, err)
		}
	}
}

// ADR-0334 Decision 4: the newest file whose size and time are the same in both listings, the engine's times compared
// only with each other; a file that changed between them is not quiet.
func TestNewestQuiet(t *testing.T) {
	t.Parallel()
	at := func(s string) string { return "2026-11-19 05:00:" + s + ".000000001 +0000" }
	first := []fileStat{{"a", "10", at("01")}, {"b", "10", at("03")}, {"c", "10", at("05")}, {"d", "10", at("02")}}
	second := []fileStat{{"a", "10", at("01")}, {"b", "10", at("03")}, {"c", "11", at("06")}, {"d", "10", at("02")}, {"e", "1", at("09")}}
	got, ok := newestQuiet(first, second)
	require.True(t, ok)
	require.Equal(t, "b", got, "c changed and e is new since the first listing")
	_, ok = newestQuiet(nil, second)
	require.False(t, ok, "nothing held for both listings")
	got, _ = newestQuiet([]fileStat{{"x", "1", at("01")}, {"y", "1", at("01")}}, []fileStat{{"x", "1", at("01")}, {"y", "1", at("01")}})
	require.Equal(t, "y", got, "a tie goes to the greater name")

	for _, dir := range []string{"", ".", "..", "../x", "/abs", "a/../b", "a/"} {
		_, err := helperDir("t", dir)
		require.Equal(t, fault.Invalid, fault.KindOf(err), dir)
	}
	p, err := helperDir("t", "state_machine/backups")
	require.NoError(t, err)
	require.Equal(t, "/d/state_machine/backups", p)
}

// ADR-0334 Decision 2 and the Review checklist: one container per object with no restart policy, the stop signal
// SIGTERM and the grace, the image's own user, its port on 127.0.0.1, and its key and spec hash; a changed spec
// replaces it; a stop sends SIGTERM and removes it; the volume, mounted with no copy-up, has its root owned by the
// storage's uid and gid at mode 0700, and stays.
func TestEngineContainer(t *testing.T) {
	t.Parallel()
	sock := requireDocker(t)
	ctx := context.Background()
	rt, key := newRuntime(t, sock, false)
	spec := whoamiSpec("team-a", "login", true)
	st := converge(t, rt, spec)
	host, _, err := net.SplitHostPort(st.Address)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)

	name := rt.name(spec.Ref)
	require.Equal(t, "funcd-dev-addon-"+key+"-team-a-login", name)
	c := inspectConfig(t, rt, name)
	require.Equal(t, key, c.Config.Labels[KeyLabel])
	require.Equal(t, specHash(spec), c.Config.Labels[specLabel])
	require.Equal(t, "no", c.HostConfig.RestartPolicy.Name)
	require.Equal(t, "SIGTERM", c.Config.StopSignal)
	require.NotNil(t, c.Config.StopTimeout)
	require.Equal(t, int(stopGrace/time.Second), *c.Config.StopTimeout)
	require.Empty(t, c.Config.User, "the image's own user")
	require.Equal(t, "127.0.0.1", c.HostConfig.PortBindings["80/tcp"][0].HostIP)
	require.Len(t, c.HostConfig.Mounts, 1)
	require.Equal(t, name, c.HostConfig.Mounts[0].Source)
	require.True(t, c.HostConfig.Mounts[0].VolumeOptions.NoCopy)
	require.Equal(t, "10001:10001:700", shell(t, rt, spec.Ref, `stat -c '%u:%g:%a' /d`))

	spec.Env["WHOAMI_NAME"] = "login-2"
	converge(t, rt, spec)
	again := inspectConfig(t, rt, name)
	require.NotEqual(t, c.ID, again.ID, "a changed spec replaces the engine")
	require.Equal(t, specHash(spec), again.Config.Labels[specLabel])

	since := time.Now().Add(-time.Second)
	require.NoError(t, rt.Teardown(ctx, spec.Ref))
	_, exists, err := rt.inspect(ctx, name)
	require.NoError(t, err)
	require.False(t, exists)
	require.Contains(t, killSignals(t, rt, again.ID, since), "15", "the stop sends SIGTERM")
	refs, err := rt.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []provider.ProviderRef{spec.Ref}, refs, "the volume stays")
	require.NoError(t, rt.Teardown(ctx, spec.Ref), "a second Teardown finds nothing")
}

// killSignals are the signals the engine sent the container id since then.
func killSignals(t *testing.T, rt *Runtime, id string, since time.Time) []string {
	t.Helper()
	filters, err := json.Marshal(map[string][]string{"container": {id}, "event": {"kill"}})
	require.NoError(t, err)
	q := url.Values{"since": {strconv.FormatInt(since.Unix(), 10)}, "until": {strconv.FormatInt(time.Now().Unix()+1, 10)},
		"filters": {string(filters)}}
	resp, err := api(rt).do(context.Background(), http.MethodGet, "/events", q, nil, "")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev struct {
			Actor struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &ev))
		out = append(out, ev.Actor.Attributes["signal"])
	}
	return out
}

// ADR-0334 Decision 7: New reaps every leftover container of its key, helpers included, and its volumes unless persist;
// another key's engine is untouched.
func TestReap(t *testing.T) {
	t.Parallel()
	sock := requireDocker(t)
	ctx := context.Background()
	other, _ := newRuntime(t, sock, false)
	otherSpec := whoamiSpec("default", "other", false)
	converge(t, other, otherSpec)

	killed, key := newRuntime(t, sock, true)
	spec := whoamiSpec("default", "login", true)
	converge(t, killed, spec)
	left := killed.helper(spec.Ref, Mount{Volume: killed.name(spec.Ref), Target: helperRoot})
	_, err := api(killed).helperContainer(ctx, left, nil)
	require.NoError(t, err, "a helper a killed session left")

	persisted, err := New(ctx, sock, key, true, nil)
	require.NoError(t, err)
	names, err := persisted.eng.Containers(ctx, map[string]string{KeyLabel: key})
	require.NoError(t, err)
	require.Empty(t, names, "the engine and the helper are gone")
	refs, err := persisted.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []provider.ProviderRef{spec.Ref}, refs, "--persist keeps the volume")

	_, err = New(ctx, sock, key, false, nil)
	require.NoError(t, err)
	refs, err = persisted.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs, "without --persist the volume goes too")

	st, err := other.Converge(ctx, otherSpec)
	require.NoError(t, err)
	require.True(t, st.Ready, "another key's engine still serves")
}

// The runtime's half of the scenario dev-addon-deletion (ADR-0334 Decision 3): Reclaim removes the volume under --persist
// too, Conflict while the engine's container exists, and a new object of that name starts empty; Quarantine keeps a
// volume as an entry of its own, labelled with the key, the object and its since, holding data/ and its state.
func TestDevAddonDeletionAtTheRuntime(t *testing.T) {
	t.Parallel()
	sock := requireDocker(t)
	ctx := context.Background()
	rt, key := newRuntime(t, sock, true)
	spec := whoamiSpec("default", "login", true)
	converge(t, rt, spec)
	require.NoError(t, rt.Put(ctx, "default", "login", "db", "users", strings.NewReader("alice"), 10001, 10001))

	require.Equal(t, fault.Conflict, fault.KindOf(rt.Reclaim(ctx, spec.Ref)), "the engine's container exists")
	require.NoError(t, rt.Teardown(ctx, spec.Ref))
	require.NoError(t, rt.Reclaim(ctx, spec.Ref))
	refs, err := rt.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs, "delete: the volume is gone, --persist or not")
	converge(t, rt, spec)
	require.Empty(t, shell(t, rt, spec.Ref, `ls -A /d`), "a new login starts empty")

	require.NoError(t, rt.Put(ctx, "default", "login", "db", "users", strings.NewReader("bob"), 10001, 10001))
	q := rt.Quarantine()
	qref := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindAddonObject, Namespace: "default", Name: "login"}}
	_, err = q.Move(ctx, qref, time.Now())
	require.Equal(t, fault.Conflict, fault.KindOf(err), "the engine's container exists: %v", err)
	require.NoError(t, rt.Teardown(ctx, spec.Ref))
	now := time.Date(2026, 11, 19, 6, 0, 0, 123_000_000, time.UTC)
	moved, err := q.Move(ctx, qref, now)
	require.NoError(t, err)
	require.Equal(t, quarantine.Entry{Ref: qref, Since: now, State: quarantine.Kept}, moved)
	refs, err = rt.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs, "retain: the object's volume moved")
	listed, err := q.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{moved}, listed)
	vols, err := rt.eng.Volumes(ctx, map[string]string{KeyLabel: key, roleLabel: roleQuarantine})
	require.NoError(t, err)
	require.Len(t, vols, 1)
	require.Equal(t, quarantine.Stamp(now), vols[0].Labels[sinceLabel])
	require.Equal(t, "login", vols[0].Labels[nameLabel])
	out, err := rt.exec(ctx, rt.helper(spec.Ref, Mount{Volume: vols[0].Name, Target: helperRoot}), "sh", "-c",
		`cat /d/state; echo; cat /d/data/db/users`)
	require.NoError(t, err)
	require.Equal(t, "kept\nbob", string(out))

	expired, err := q.Expire(ctx, now)
	require.NoError(t, err)
	require.Empty(t, expired, "an entry is kept for its period")
	expired, err = q.Expire(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{moved}, expired)
	listed, err = q.List(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
}

// The runtime's half of the scenario dev-addon-backup (ADR-0334 Decision 4, ADR-0333 Decisions 2 and 3): the files of
// ADR-0333's backup member and in-place Restore, reached only through the helper. Newest streams the newest quiet
// snapshot, skipping a name that ends in ~; Put writes a copy owned by the storage's uid and gid, making its
// directory; Clear empties the restore directory.
func TestDevAddonBackupAtTheRuntime(t *testing.T) {
	t.Parallel()
	sock := requireDocker(t)
	ctx := context.Background()
	rt, _ := newRuntime(t, sock, false)
	spec := whoamiSpec("default", "login", true)
	converge(t, rt, spec)
	const snaps = "state_machine/backups"

	_, _, err := rt.Newest(ctx, "default", "login", snaps, 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no snapshot yet: %v", err)
	require.NoError(t, rt.Put(ctx, "default", "login", snaps, "1700000000", strings.NewReader("old"), 10001, 10001))
	require.NoError(t, rt.Put(ctx, "default", "login", snaps, "1700000900", strings.NewReader("new"), 10001, 10001))
	shell(t, rt, spec.Ref, `touch "$1/1800000000~"`, "/d/"+snaps)

	file, body, err := rt.Newest(ctx, "default", "login", snaps, 100*time.Millisecond)
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, "1700000900", file)
	require.Equal(t, "new", string(got))

	require.NoError(t, rt.Put(ctx, "default", "login", "restore", "1700000900", strings.NewReader("copy"), 10001, 10001))
	require.Equal(t, "10001:10001:700 10001:10001:600", shell(t, rt, spec.Ref,
		`echo $(stat -c '%u:%g:%a' /d/restore) $(stat -c '%u:%g:%a' /d/restore/1700000900)`))
	require.Equal(t, "10001:10001:700", shell(t, rt, spec.Ref, `stat -c '%u:%g:%a' /d/state_machine`), "each new parent too")
	require.Equal(t, fault.Invalid, fault.KindOf(rt.Put(ctx, "default", "login", "restore", "../x", strings.NewReader(""), 0, 0)))

	require.NoError(t, rt.Clear(ctx, "default", "login", "restore"))
	require.Empty(t, shell(t, rt, spec.Ref, `ls -A /d/restore`), "restore.path is empty")
	require.NoError(t, rt.Clear(ctx, "default", "login", "never-made"), "nothing to clear")
	require.NoError(t, rt.Clear(ctx, "default", "nobody", "restore"), "no volume holds nothing to clear")
	_, _, err = rt.Newest(ctx, "default", "nobody", snaps, 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// afterEntryEngine runs after once, when a Move has created its entry's volume and before its helper writes the state.
type afterEntryEngine struct {
	Engine
	after func(name string)
}

func (e *afterEntryEngine) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	err := e.Engine.CreateVolume(ctx, name, labels)
	if err == nil && labels[roleLabel] == roleQuarantine && e.after != nil {
		f := e.after
		e.after = nil
		f(name)
	}
	return err
}

// ADR-0288 Decision 2 under dev: the sweep's Expire, which removes an entry with no state, waits while a retain move
// has created its entry and not yet written the state, so the moved data stays a quarantine entry.
func TestQuarantineMoveAndExpireTakeTurns(t *testing.T) {
	sock := requireDocker(t)
	ctx := context.Background()
	rt, _ := newRuntime(t, sock, true)
	spec := whoamiSpec("default", "login", true)
	converge(t, rt, spec)
	require.NoError(t, rt.Put(ctx, "default", "login", "db", "users", strings.NewReader("bob"), 10001, 10001))
	require.NoError(t, rt.Teardown(ctx, spec.Ref))
	q := rt.Quarantine()
	swept := make(chan error, 1)
	eng := rt.eng
	// An Expire that does not wait ends well within the 3 s: one volume list and one helper.
	rt.eng = &afterEntryEngine{Engine: eng, after: func(name string) {
		// An Expire that removed the entry leaves the copy in a volume with no labels, which cleanupKey misses.
		t.Cleanup(func() { _ = eng.RemoveVolume(context.Background(), name) })
		go func() {
			_, err := q.Expire(ctx, time.Now().Add(-time.Hour))
			swept <- err
		}()
		select {
		case err := <-swept:
			swept <- err
		case <-time.After(3 * time.Second):
		}
	}}
	_, err := q.Move(ctx, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindAddonObject, Namespace: "default", Name: "login"}}, time.Now())
	require.NoError(t, err)
	require.NoError(t, <-swept)
	es, err := q.List(ctx)
	require.NoError(t, err)
	require.Len(t, es, 1, "the sweep waited for the move")
}
