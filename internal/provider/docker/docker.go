// Package docker runs the add-on engines of funcdctl dev on the developer's own container engine (ADR-0334): one
// container per add-on object, started from its Addon's image with its port published on 127.0.0.1, its storage a named
// volume mounted with no copy-up, and its files reached only through a helper container. Runtime implements
// provider.Runtime, provider.Volumes and ADR-0333's AddonFiles over an Engine: Docker's Engine API over a unix socket
// here, the nerdctl CLI in internal/provider/nerdctl. Every container and volume carries the session's key in its name
// and in its funcd/dev-key label, and a Runtime touches no other.
package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// The labels every container and volume of a Runtime carries (Decision 6), and the roles they name.
const (
	KeyLabel       = "funcd/dev-key"
	roleLabel      = "funcd/role"
	namespaceLabel = "funcd/namespace"
	nameLabel      = "funcd/name"
	specLabel      = "funcd/spec"  // an engine's spec hash (Decision 2)
	sinceLabel     = "funcd/since" // a quarantine entry's since (Decision 3)

	roleEngine     = "engine"
	roleHelper     = "helper"
	roleStorage    = "storage"
	roleQuarantine = "quarantine"
)

const (
	namePrefix = "funcd-dev-addon-"
	// HelperImage reaches an engine's volume (Decision 4): busybox 1.36.1, pinned by digest.
	HelperImage = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
	// helperRoot is where a helper mounts the volume it works on, and helperSource the source of a copy move.
	helperRoot   = "/d"
	helperSource = "/s"
	// stopGrace is ADR-0277's default grace between an engine's SIGTERM and its kill (Decision 2).
	stopGrace = 10 * time.Second
	// probeTimeout bounds one readiness probe, as the server's add-on runtime bounds it.
	probeTimeout = 2 * time.Second
	// pingTimeout bounds the ping of one socket of Decision 1.
	pingTimeout = 2 * time.Second
	// requestTimeout bounds one call to the engine that copies no data.
	requestTimeout = 30 * time.Second
	// pullTimeout bounds a start that may pull an image.
	pullTimeout = 10 * time.Minute
	// helperTimeout bounds a helper run, which may copy a whole volume.
	helperTimeout = 10 * time.Minute
	// maxReplyBytes bounds what is read whole: an engine's answer, a helper's output.
	maxReplyBytes = 8 << 20
	// maxAPIVersion is the newest Engine API version the driver speaks; an older engine is spoken to at its own.
	maxAPIVersion = "1.47"
	// systemSocket is the last socket of Decision 1.
	systemSocket = "/var/run/docker.sock"
	// reasonEngineNotReady is the status reason of an engine that does not answer its readiness path yet.
	reasonEngineNotReady = "EngineNotReady"
	// fileMode and dirMode are a copy Put writes and the directories it makes (ADR-0231 Decision 2's mode).
	fileMode = 0o600
	dirMode  = 0o700
	// statLayout is busybox stat's %y, the modification time a listing compares.
	statLayout = "2006-01-02 15:04:05.999999999 -0700"
)

// sessionKey is a session's key: the first 16 hex digits of a sha256 (Decision 6).
var sessionKey = regexp.MustCompile(`^[0-9a-f]{16}$`)

// fileName is a snapshot's or a copy's name: one path segment (ADR-0333 Decision 6).
var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Engine is the container engine a Runtime drives. Labels select by equality, and a missing container or volume is no
// error where a method says so. An error is a fault error: Unavailable when the engine does not answer.
type Engine interface {
	// Containers names the containers, running or not, that carry every label.
	Containers(ctx context.Context, labels map[string]string) ([]string, error)
	// Inspect reads the container name; ok is false when there is none.
	Inspect(ctx context.Context, name string) (c Container, ok bool, err error)
	// Pull makes image present, pulling it with no credentials when it is not.
	Pull(ctx context.Context, image string) error
	// Run creates and starts a container from spec.
	Run(ctx context.Context, spec RunSpec) error
	// Stop sends the container its stop signal and kills it after grace; nil when it is stopped or gone.
	Stop(ctx context.Context, name string, grace time.Duration) error
	// Remove removes the container; nil when it is gone.
	Remove(ctx context.Context, name string) error
	// CreateVolume creates the volume name with labels.
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	// Volumes lists the volumes that carry every label.
	Volumes(ctx context.Context, labels map[string]string) ([]Volume, error)
	// RemoveVolume removes the volume name: Conflict while a container uses it, nil when it is gone.
	RemoveVolume(ctx context.Context, name string) error
	// Exec runs the helper h with argv to its end and returns what it wrote on its standard output; a run that ends
	// with another status than 0 is an error holding what it wrote on its standard error.
	Exec(ctx context.Context, h Helper, argv ...string) ([]byte, error)
	// ReadTar streams a tar that holds p, a file in one of h's mounts.
	ReadTar(ctx context.Context, h Helper, p string) (io.ReadCloser, error)
	// WriteTar extracts archive into dir, a directory in one of h's mounts, each entry keeping its owner, and makes dir
	// and each missing parent with mode 0700, owned by uid and gid.
	WriteTar(ctx context.Context, h Helper, dir string, uid, gid int, archive io.Reader) error
}

// Container is what Inspect reads of a container.
type Container struct {
	Running bool
	Labels  map[string]string
	// Published maps each published port, "<port>/tcp", to the host address it is published at.
	Published map[string]string
}

// Volume is a volume and its labels.
type Volume struct {
	Name   string
	Labels map[string]string
}

// RunSpec is an engine's container (Decision 2): Port published on 127.0.0.1 at a free port, Volume, when set, mounted
// at Target with no copy-up, no restart policy, the stop signal SIGTERM and StopGrace before a kill. The image's own
// user runs it.
type RunSpec struct {
	Name      string
	Image     string
	Env       map[string]string
	Labels    map[string]string
	Port      int
	Volume    string
	Target    string
	StopGrace time.Duration
}

// Helper is a helper container of Image, the volumes of Mounts at their targets (Decision 4).
type Helper struct {
	Name   string
	Image  string
	Labels map[string]string
	Mounts []Mount
}

// Mount is a volume mounted at an absolute path.
type Mount struct{ Volume, Target string }

// Runtime is the add-on runtime of one dev session over an Engine.
type Runtime struct {
	eng     Engine
	key     string
	persist bool
	logger  *slog.Logger
	probe   *http.Client
	limits  containerrt.LimitWarner // logs once per object that dev enforces no resources (Decision 2)
}

var (
	_ provider.Runtime = (*Runtime)(nil)
	_ provider.Volumes = (*Runtime)(nil)
)

// Socket returns the first unix socket of Decision 1 whose engine answers GET /_ping: FUNCD_DEV_DOCKER_HOST,
// DOCKER_HOST, the active Docker context's endpoint, colima's socket, Docker Desktop's and /var/run/docker.sock.
// Unavailable names each one tried.
func Socket(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return findSocket(ctx, os.Getenv, home, systemSocket)
}

// candidate is one place Decision 1 looks for a socket; path is "" when the place names none, and note says why.
type candidate struct {
	from, path, note string
}

func findSocket(ctx context.Context, env func(string) string, home, system string) (string, error) {
	var tried []string
	seen := map[string]bool{}
	for _, c := range candidates(env, home, system) {
		switch {
		case c.path == "":
			tried = append(tried, c.from+" ("+c.note+")")
			continue
		case seen[c.path]:
			continue
		}
		seen[c.path] = true
		if _, err := ping(ctx, c.path); err != nil {
			tried = append(tried, c.from+" "+c.path+" ("+pingProblem(err)+")")
			continue
		}
		return c.path, nil
	}
	return "", fault.Unavailablef("docker.Socket", "no Docker engine answers: tried %s", strings.Join(tried, ", "))
}

// candidates are Decision 1's places in order; an unset variable is no place.
func candidates(env func(string) string, home, system string) []candidate {
	var out []candidate
	for _, v := range []string{"FUNCD_DEV_DOCKER_HOST", "DOCKER_HOST"} {
		if h := env(v); h != "" {
			out = append(out, unixCandidate(v, h))
		}
	}
	if c, ok := contextCandidate(env, home); ok {
		out = append(out, c)
	}
	if home != "" {
		out = append(out,
			candidate{from: "colima", path: filepath.Join(home, ".colima", "default", "docker.sock")},
			candidate{from: "Docker Desktop", path: filepath.Join(home, ".docker", "run", "docker.sock")})
	}
	return append(out, candidate{from: "the system socket", path: system})
}

func unixCandidate(from, host string) candidate {
	if p, ok := strings.CutPrefix(host, "unix://"); ok && p != "" {
		return candidate{from: from, path: p}
	}
	return candidate{from: from + " " + host, note: "not a unix:// address"}
}

// contextCandidate is the endpoint of the active Docker context: DOCKER_CONTEXT, else currentContext in
// ~/.docker/config.json. The default context is DOCKER_HOST or the system socket, which are tried anyway.
func contextCandidate(env func(string) string, home string) (candidate, bool) {
	name := env("DOCKER_CONTEXT")
	if name == "" && home != "" {
		var cfg struct {
			CurrentContext string `json:"currentContext"`
		}
		if b, err := readSmall(filepath.Join(home, ".docker", "config.json")); err == nil {
			_ = json.Unmarshal(b, &cfg)
		}
		name = cfg.CurrentContext
	}
	if name == "" || name == "default" || home == "" {
		return candidate{}, false
	}
	from := "the Docker context " + name
	sum := sha256.Sum256([]byte(name))
	b, err := readSmall(filepath.Join(home, ".docker", "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json"))
	if err != nil {
		return candidate{from: from, note: "no such context"}, true
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return candidate{from: from, note: "its meta.json does not parse"}, true
	}
	return unixCandidate(from, meta.Endpoints.Docker.Host), true
}

// readSmall reads a file of at most maxReplyBytes.
func readSmall(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // the developer's own Docker config
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxReplyBytes))
}

func pingProblem(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "absent"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	}
	return err.Error()
}

// ping asks the engine on socket for GET /_ping and returns its Engine API version.
func ping(ctx context.Context, socket string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return "", err
	}
	resp, err := socketClient(socket).Do(req)
	if err != nil {
		return "", err
	}
	httpx.CloseBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /_ping answered %d", resp.StatusCode)
	}
	return resp.Header.Get("Api-Version"), nil
}

// socketClient is an HTTP client whose every connection dials socket.
func socketClient(socket string) *http.Client {
	tr := httpx.NodeTransport()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &http.Client{Transport: tr}
}

// apiVersion is the Engine API version to speak with an engine of version server: its own, at most maxAPIVersion.
func apiVersion(server string) string {
	if versionLess(server, maxAPIVersion) {
		return server
	}
	return maxAPIVersion
}

// versionLess reports whether a, of the form <major>.<minor>, is older than b; a that does not parse is not.
func versionLess(a, b string) bool {
	parse := func(s string) (int, int, bool) {
		maj, min, ok := strings.Cut(s, ".")
		x, err1 := strconv.Atoi(maj)
		y, err2 := strconv.Atoi(min)
		return x, y, ok && err1 == nil && err2 == nil
	}
	ax, ay, aok := parse(a)
	bx, by, _ := parse(b)
	return aok && (ax < bx || ax == bx && ay < by)
}

// New is the Docker driver on socket for the session key (Decision 1). It reaps the key's leftover containers, helpers
// included, and, unless persist, its volumes, quarantine entries included, before it returns (Decision 7).
func New(ctx context.Context, socket, key string, persist bool, logger *slog.Logger) (*Runtime, error) {
	version, err := ping(ctx, socket)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "docker.New", "no Docker engine answers at %s", socket)
	}
	return NewRuntime(ctx, &engineAPI{http: socketClient(socket), base: "http://docker/v" + apiVersion(version)}, key, persist, logger)
}

// NewRuntime is the runtime of the session key over eng, reaped as New reaps it. internal/provider/nerdctl builds its
// driver with it.
func NewRuntime(ctx context.Context, eng Engine, key string, persist bool, logger *slog.Logger) (*Runtime, error) {
	const op = "docker.NewRuntime"
	if !sessionKey.MatchString(key) {
		return nil, fault.Invalidf(op, "session key %q is not 16 hex digits", key)
	}
	r := &Runtime{eng: eng, key: key, persist: persist, logger: cmp.Or(logger, slog.Default()).With("component", "addon-dev-runtime"),
		probe: httpx.NodeClient(probeTimeout)}
	if err := r.reap(ctx); err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "remove what an earlier session of key %s left", key)
	}
	return r, nil
}

// name is ref's container and volume name (Decision 6).
func (r *Runtime) name(ref provider.ProviderRef) string {
	return namePrefix + r.key + "-" + string(ref.Namespace) + "-" + string(ref.Name)
}

// labels are the labels of ref's container or volume of role.
func (r *Runtime) labels(role string, ref provider.ProviderRef) map[string]string {
	return map[string]string{KeyLabel: r.key, roleLabel: role, namespaceLabel: string(ref.Namespace), nameLabel: string(ref.Name)}
}

// helper is a helper of ref with mounts, its name the object's with a suffix of its own, so helpers never collide.
func (r *Runtime) helper(ref provider.ProviderRef, mounts ...Mount) Helper {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return Helper{Name: r.name(ref) + "-helper-" + hex.EncodeToString(b[:]), Image: HelperImage,
		Labels: r.labels(roleHelper, ref), Mounts: mounts}
}

// exec runs a helper of ref, bounded by helperTimeout.
func (r *Runtime) exec(ctx context.Context, h Helper, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	return r.eng.Exec(ctx, h, argv...)
}

// Converge runs spec's engine (Decision 2): one container labelled with the hash of what it runs, replaced when that
// changes and started again when it stopped, since it has no restart policy. It is Ready once its readiness path
// answers on its published port, which status.address holds. resources set no limit under dev: it logs so once per
// object.
func (r *Runtime) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	const op = "docker.Converge"
	switch {
	case spec.Replicas != 1:
		return provider.ProviderStatus{}, fault.Invalidf(op, "an add-on engine under dev runs one replica; got %d", spec.Replicas)
	case spec.Image == "":
		return provider.ProviderStatus{}, fault.Invalidf(op, "spec.Image is required")
	case spec.Port <= 0:
		return provider.ProviderStatus{}, fault.Invalidf(op, "spec.Port must be > 0; got %d", spec.Port)
	}
	r.limits.Warn(ctx, r.logger, nil, string(spec.Ref.Namespace)+"/"+string(spec.Ref.Name),
		containerrt.LimitsFrom(spec.Resources.CPU, spec.Resources.Memory))
	name, hash := r.name(spec.Ref), specHash(spec)
	c, ok, err := r.inspect(ctx, name)
	if err != nil {
		return provider.ProviderStatus{}, err
	}
	if ok && (!c.Running || c.Labels[specLabel] != hash) {
		if err := r.retire(ctx, name); err != nil {
			return provider.ProviderStatus{}, fault.Wrapf(err, fault.KindOf(err), op, "replace the engine of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
		}
		ok = false
	}
	if !ok {
		if c, err = r.start(ctx, spec, name, hash); err != nil {
			return provider.ProviderStatus{}, fault.Wrapf(err, fault.KindOf(err), op, "start the engine of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
		}
	}
	addr := c.Published[portKey(spec.Port)]
	st := provider.ProviderStatus{Running: 1, Address: addr, Reason: reasonEngineNotReady}
	if addr != "" && r.ready(ctx, addr, spec.Readiness) {
		st.Ready, st.Reason = true, ""
	}
	return st, nil
}

func portKey(port int) string { return strconv.Itoa(port) + "/tcp" }

// specHash is the hash of what an engine's container runs: its image, port, environment and storage. Of the keys
// issued per engine only the names count, as for the server's engines (ADR-0270 Decision 5).
func specHash(spec provider.ProviderSpec) string {
	env := maps.Clone(spec.Env)
	if spec.IssueEnv != nil {
		if env == nil {
			env = map[string]string{}
		}
		for k := range spec.IssueEnv() {
			env[k] = ""
		}
	}
	b, _ := json.Marshal(struct {
		Image   string
		Port    int
		Env     map[string]string
		Storage *provider.StorageSpec
	}{spec.Image, spec.Port, env, spec.Storage})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) inspect(ctx context.Context, name string) (Container, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return r.eng.Inspect(ctx, name)
}

// start pulls the image, prepares the volume and runs the engine, then reads it back.
func (r *Runtime) start(ctx context.Context, spec provider.ProviderSpec, name, hash string) (Container, error) {
	const op = "docker.start"
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	if err := r.eng.Pull(pctx, spec.Image); err != nil {
		return Container{}, err
	}
	run := RunSpec{Name: name, Image: spec.Image, Env: spec.LaunchEnv(), Labels: r.labels(roleEngine, spec.Ref), Port: spec.Port,
		StopGrace: stopGrace}
	run.Labels[specLabel] = hash
	if st := spec.Storage; st != nil {
		if err := r.prepareStorage(ctx, spec.Ref, name, st); err != nil {
			return Container{}, err
		}
		run.Volume, run.Target = name, st.Path
	}
	if err := r.eng.Run(pctx, run); err != nil {
		return Container{}, err
	}
	c, ok, err := r.inspect(ctx, name)
	switch {
	case err != nil:
		return Container{}, err
	case !ok:
		return Container{}, fault.Internalf(op, "the engine container %s is gone right after its start", name)
	}
	return c, nil
}

// prepareStorage creates ref's volume when it has none, and sets its root to the engine's user, mode 0700, through the
// helper (Decision 3), at each engine start, as ADR-0231 Decision 2 sets the leaf.
func (r *Runtime) prepareStorage(ctx context.Context, ref provider.ProviderRef, vol string, st *provider.StorageSpec) error {
	ok, err := r.hasVolume(ctx, ref)
	if err != nil {
		return err
	}
	if !ok {
		cctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		if err := r.eng.CreateVolume(cctx, vol, r.labels(roleStorage, ref)); err != nil {
			return err
		}
	}
	_, err = r.exec(ctx, r.helper(ref, Mount{Volume: vol, Target: helperRoot}),
		"sh", "-c", `chown "$1" "$2" && chmod 700 "$2"`, "sh", strconv.Itoa(st.UID)+":"+strconv.Itoa(st.GID), helperRoot)
	return err
}

// hasVolume reports whether ref has its volume.
func (r *Runtime) hasVolume(ctx context.Context, ref provider.ProviderRef) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	vols, err := r.eng.Volumes(ctx, r.labels(roleStorage, ref))
	return len(vols) > 0, err
}

// ready probes the engine's readiness path at addr.
func (r *Runtime) ready(ctx context.Context, addr string, p provider.ReadinessProbe) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+p.Path, nil)
	if err != nil {
		return false
	}
	resp, err := r.probe.Do(req)
	if err != nil {
		return false
	}
	httpx.CloseBody(resp.Body)
	return resp.StatusCode == cmp.Or(p.ExpectStatus, http.StatusOK)
}

// retire stops a container with its stop signal and the grace, then removes it (Decision 2).
func (r *Runtime) retire(ctx context.Context, name string) error {
	sctx, cancel := context.WithTimeout(ctx, stopGrace+requestTimeout)
	defer cancel()
	if err := r.eng.Stop(sctx, name, stopGrace); err != nil {
		return err
	}
	rctx, rcancel := context.WithTimeout(ctx, requestTimeout)
	defer rcancel()
	return r.eng.Remove(rctx, name)
}

// Teardown stops and removes ref's engine; its volume stays.
func (r *Runtime) Teardown(ctx context.Context, ref provider.ProviderRef) error {
	return r.retire(ctx, r.name(ref))
}

// Reclaim removes ref's volume, under --persist too; Conflict while its engine's container exists (Decision 3).
func (r *Runtime) Reclaim(ctx context.Context, ref provider.ProviderRef) error {
	_, ok, err := r.inspect(ctx, r.name(ref))
	switch {
	case err != nil:
		return err
	case ok:
		return fault.Conflictf("docker.Reclaim", "%s/%s still has its engine container; tear it down first", ref.Namespace, ref.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return r.eng.RemoveVolume(ctx, r.name(ref))
}

// List returns the objects of the key that have a volume, sorted.
func (r *Runtime) List(ctx context.Context) ([]provider.ProviderRef, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	vols, err := r.eng.Volumes(ctx, map[string]string{KeyLabel: r.key, roleLabel: roleStorage})
	if err != nil {
		return nil, err
	}
	out := make([]provider.ProviderRef, 0, len(vols))
	for _, v := range vols {
		out = append(out, provider.ProviderRef{Namespace: v1.NamespaceName(v.Labels[namespaceLabel]), Name: v1.ObjectName(v.Labels[nameLabel])})
	}
	slices.SortFunc(out, func(a, b provider.ProviderRef) int {
		return cmp.Or(strings.Compare(string(a.Namespace), string(b.Namespace)), strings.Compare(string(a.Name), string(b.Name)))
	})
	return out, nil
}

// StopAll stops every container of the key at once, with SIGTERM and the grace, removes them, then removes the key's
// volumes unless persist (Decision 7). funcdctl dev calls it once its platform has stopped.
func (r *Runtime) StopAll() {
	if err := r.reap(context.Background()); err != nil {
		r.logger.Warn("could not remove every add-on engine of this session", "key", r.key, "err", err)
	}
}

// reap is StopAll's work, which New does first.
func (r *Runtime) reap(ctx context.Context) error {
	lctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	names, err := r.eng.Containers(lctx, map[string]string{KeyLabel: r.key})
	if err != nil {
		return err
	}
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Go(func() { errs[i] = r.retire(ctx, n) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil || r.persist {
		return err
	}
	vols, err := r.eng.Volumes(lctx, map[string]string{KeyLabel: r.key})
	if err != nil {
		return err
	}
	for _, v := range vols {
		errs = append(errs, r.eng.RemoveVolume(lctx, v.Name))
	}
	return errors.Join(errs...)
}

// helperDir is dir, relative to the storage's root, as a path in a helper: a clean relative path that stays in it.
func helperDir(op, dir string) (string, error) {
	if dir == "" || dir == "." || path.IsAbs(dir) || path.Clean(dir) != dir || dir == ".." || strings.HasPrefix(dir, "../") {
		return "", fault.Invalidf(op, "dir %q is not a clean relative path under the storage's root", dir)
	}
	return path.Join(helperRoot, dir), nil
}

// fileStat is one regular file a listing found: its name, size and modification time as the engine's clock wrote it.
type fileStat struct{ name, size, mtime string }

// Newest streams the newest quiet file of dir in ref's volume (ADR-0333 Decision 2, ADR-0334 Decision 4): it lists dir
// twice, quiet apart, and takes the newest regular file whose name is one path segment not ending in ~ and whose size
// and modification time match in both listings, so no clock of the engine's VM is compared with the host's. NotFound
// when there is none.
func (r *Runtime) Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (string, io.ReadCloser, error) {
	const op = "docker.Newest"
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	in, err := helperDir(op, dir)
	if err != nil {
		return "", nil, err
	}
	if err := r.needVolume(ctx, op, ref); err != nil {
		return "", nil, err
	}
	first, err := r.listFiles(ctx, ref, in)
	if err != nil {
		return "", nil, err
	}
	t := time.NewTimer(quiet)
	select {
	case <-ctx.Done():
		t.Stop()
		return "", nil, fault.Wrapf(ctx.Err(), fault.Unavailable, op, "wait %s between the two listings", quiet)
	case <-t.C:
	}
	second, err := r.listFiles(ctx, ref, in)
	if err != nil {
		return "", nil, err
	}
	file, ok := newestQuiet(first, second)
	if !ok {
		return "", nil, fault.NotFoundf(op, "%s/%s holds no file in %s that has not changed for %s", ns, name, dir, quiet)
	}
	rc, err := r.eng.ReadTar(ctx, r.helper(ref, Mount{Volume: r.name(ref), Target: helperRoot}), path.Join(in, file))
	if err != nil {
		return "", nil, err
	}
	body, err := tarFile(rc)
	if err != nil {
		return "", nil, fault.Wrapf(err, fault.KindOf(err), op, "read %s of %s/%s", file, ns, name)
	}
	return file, body, nil
}

func (r *Runtime) needVolume(ctx context.Context, op string, ref provider.ProviderRef) error {
	ok, err := r.hasVolume(ctx, ref)
	if err == nil && !ok {
		err = fault.NotFoundf(op, "%s/%s has no storage", ref.Namespace, ref.Name)
	}
	return err
}

// listFiles lists the regular files right under in, a directory of ref's volume, with busybox's stat.
func (r *Runtime) listFiles(ctx context.Context, ref provider.ProviderRef, in string) ([]fileStat, error) {
	out, err := r.exec(ctx, r.helper(ref, Mount{Volume: r.name(ref), Target: helperRoot}), "sh", "-c",
		`[ -d "$1" ] || exit 0; find "$1" -mindepth 1 -maxdepth 1 -type f -exec stat -c '%s|%y|%n' {} +`, "sh", in)
	if err != nil {
		return nil, err
	}
	var files []fileStat
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		if len(parts) != 3 {
			continue
		}
		if n := path.Base(parts[2]); fileName.MatchString(n) && !strings.HasSuffix(n, "~") {
			files = append(files, fileStat{name: n, size: parts[0], mtime: parts[1]})
		}
	}
	return files, sc.Err()
}

// newestQuiet is the newest file of second that first holds unchanged; a tie on the time goes to the greater name.
func newestQuiet(first, second []fileStat) (string, bool) {
	held := make(map[string]fileStat, len(first))
	for _, f := range first {
		held[f.name] = f
	}
	var best string
	var bestAt time.Time
	for _, f := range second {
		at, err := time.Parse(statLayout, f.mtime)
		if err != nil || held[f.name] != f {
			continue
		}
		if best == "" || at.After(bestAt) || at.Equal(bestAt) && f.name > best {
			best, bestAt = f.name, at
		}
	}
	return best, best != ""
}

// tarFile is the content of the first regular file in a tar stream; closing it closes rc.
func tarFile(rc io.ReadCloser) (io.ReadCloser, error) {
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err != nil {
			_ = rc.Close()
			if errors.Is(err, io.EOF) {
				return nil, fault.NotFoundf("docker.tarFile", "the archive holds no regular file")
			}
			return nil, fault.Wrapf(err, fault.Internal, "docker.tarFile", "read the archive")
		}
		if h.Typeflag == tar.TypeReg {
			return readCloser{Reader: tr, close: rc.Close}, nil
		}
	}
}

// readCloser reads Reader and closes with close.
type readCloser struct {
	io.Reader
	close func() error
}

func (c readCloser) Close() error { return c.close() }

// Put writes file into dir of ref's volume through the helper, owned by uid and gid, making dir when it is missing
// (ADR-0333 Decision 3).
func (r *Runtime) Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, src io.Reader, uid, gid int) error {
	const op = "docker.Put"
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	in, err := helperDir(op, dir)
	if err != nil {
		return err
	}
	if !fileName.MatchString(file) {
		return fault.Invalidf(op, "file %q is not one path segment of letters, digits, '.', '_' and '-'", file)
	}
	if err := r.needVolume(ctx, op, ref); err != nil {
		return err
	}
	// A tar entry states its size first, so the copy is spooled to learn it.
	spool, err := os.CreateTemp("", "funcd-dev-addon-put-*")
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "spool the copy")
	}
	defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
	size, err := io.Copy(spool, src)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "spool the copy")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "spool the copy")
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: file, Typeflag: tar.TypeReg, Mode: fileMode, Uid: uid, Gid: gid, Size: size,
			ModTime: time.Now()})
		if err == nil {
			_, err = io.Copy(tw, spool)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	err = r.eng.WriteTar(ctx, r.helper(ref, Mount{Volume: r.name(ref), Target: helperRoot}), in, uid, gid, pr)
	_ = pr.Close()
	<-done
	return err
}

// Clear empties dir of ref's volume through the helper; a missing volume or directory holds nothing to clear.
func (r *Runtime) Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error {
	const op = "docker.Clear"
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	in, err := helperDir(op, dir)
	if err != nil {
		return err
	}
	if ok, err := r.hasVolume(ctx, ref); err != nil || !ok {
		return err
	}
	_, err = r.exec(ctx, r.helper(ref, Mount{Volume: r.name(ref), Target: helperRoot}), "sh", "-c",
		`[ ! -d "$1" ] || find "$1" -mindepth 1 -maxdepth 1 -exec rm -rf {} +`, "sh", in)
	return err
}

// Quarantine keeps each object volume a reclaim moves aside as a volume of its own (Decision 3, ADR-0288): labelled with
// the key, the object and its since, holding data/ and its state, filled by a copy move through the helper.
func (r *Runtime) Quarantine() quarantine.Store { return quarantineStore{r: r} }

type quarantineStore struct{ r *Runtime }

// entry is one quarantine volume, read through the helper.
type entry struct {
	quarantine.Entry
	volume         string
	state, hasData bool
	ref            provider.ProviderRef
}

// Move copies ref's volume into an entry dated now, or resumes ref's Moving entry, then removes the volume (ADR-0288
// Decision 3): the entry's state is moving before the copy and kept once the volume is gone. Conflict while ref's
// engine runs; NotFound without a volume.
func (q quarantineStore) Move(ctx context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	const op = "docker.Quarantine"
	r := q.r
	if ref.Kind != v1.KindAddonObject {
		return quarantine.Entry{}, fault.Invalidf(op, "%s: this quarantine keeps %s volumes", ref, v1.KindAddonObject)
	}
	pref := provider.ProviderRef{Namespace: ref.Namespace, Name: ref.Name}
	if err := pref.Namespace.Validate(); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Invalid, op, "namespace")
	}
	if err := pref.Name.Validate(); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Invalid, op, "name")
	}
	src := r.name(pref)
	if _, exists, err := r.inspect(ctx, src); err != nil {
		return quarantine.Entry{}, err
	} else if exists {
		return quarantine.Entry{}, fault.Conflictf(op, "the engine of %s still runs; tear it down first", ref)
	}
	entries, err := q.scan(ctx)
	if err != nil {
		return quarantine.Entry{}, err
	}
	e, resumed := entry{}, false
	for _, c := range entries {
		if c.Ref == ref && c.State == quarantine.Moving {
			e, resumed = c, true
		}
	}
	has, err := r.hasVolume(ctx, pref)
	switch {
	case err != nil:
		return quarantine.Entry{}, err
	case !has && !resumed:
		return quarantine.Entry{}, fault.NotFoundf(op, "%s has no volume", ref)
	case !has:
		// A move cut after the volume went: what it copied is the entry.
		return q.kept(ctx, pref, e)
	}
	if !resumed {
		since := now.UTC().Truncate(time.Millisecond)
		e = entry{Entry: quarantine.Entry{Ref: ref, Since: since}, volume: src + "-" + quarantine.Stamp(since)}
		labels := r.labels(roleQuarantine, pref)
		labels[sinceLabel] = quarantine.Stamp(since)
		cctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		if err := r.eng.CreateVolume(cctx, e.volume, labels); err != nil {
			return quarantine.Entry{}, err
		}
	}
	if _, err := r.exec(ctx, r.helper(pref, Mount{Volume: src, Target: helperSource}, Mount{Volume: e.volume, Target: helperRoot}),
		"sh", "-c", setState(quarantine.Moving)+` && mkdir -p /d/data && cp -a /s/. /d/data/`); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), op, "copy the volume of %s", ref)
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := r.eng.RemoveVolume(rctx, src); err != nil {
		return quarantine.Entry{}, err
	}
	return q.kept(ctx, pref, e)
}

// kept writes e's state kept, the last step of a move.
func (q quarantineStore) kept(ctx context.Context, ref provider.ProviderRef, e entry) (quarantine.Entry, error) {
	if _, err := q.r.exec(ctx, q.r.helper(ref, Mount{Volume: e.volume, Target: helperRoot}), "sh", "-c", setState(quarantine.Kept)); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), "docker.Quarantine", "mark the entry of %s kept", e.Ref)
	}
	return quarantine.Entry{Ref: e.Ref, Since: e.Since, State: quarantine.Kept}, nil
}

// setState is the helper's command that replaces /d/state with s in one rename.
func setState(s quarantine.State) string {
	return `printf '` + string(s) + `' > /d/.state && mv /d/.state /d/state`
}

// scan reads every quarantine volume of the key, its state and whether data/ holds anything, sorted.
func (q quarantineStore) scan(ctx context.Context) ([]entry, error) {
	r := q.r
	lctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	vols, err := r.eng.Volumes(lctx, map[string]string{KeyLabel: r.key, roleLabel: roleQuarantine})
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, v := range vols {
		since, err := quarantine.ParseStamp(v.Labels[sinceLabel])
		if err != nil {
			continue
		}
		ref := provider.ProviderRef{Namespace: v1.NamespaceName(v.Labels[namespaceLabel]), Name: v1.ObjectName(v.Labels[nameLabel])}
		e := entry{Entry: quarantine.Entry{Ref: quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindAddonObject,
			Namespace: ref.Namespace, Name: ref.Name}}, Since: since, State: quarantine.Kept}, volume: v.Name, ref: ref}
		got, err := r.exec(ctx, r.helper(ref, Mount{Volume: v.Name, Target: helperRoot}), "sh", "-c",
			`if [ -f /d/state ]; then printf 'state '; cat /d/state; echo; fi; if [ -n "$(ls -A /d/data 2>/dev/null)" ]; then echo data; fi`)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(got), "\n") {
			switch s, isState := strings.CutPrefix(line, "state "); {
			case isState:
				if e.State, err = quarantine.ParseState(s); err != nil {
					return nil, err
				}
				e.state = true
			case line == "data":
				e.hasData = true
			}
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b entry) int {
		return cmp.Or(strings.Compare(a.String(), b.String()), a.Since.Compare(b.Since))
	})
	return out, nil
}

// List returns the entries; one with neither data nor state is none.
func (q quarantineStore) List(ctx context.Context) ([]quarantine.Entry, error) {
	es, err := q.scan(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range es {
		if e.hasData || e.state {
			out = append(out, e.Entry)
		}
	}
	return out, nil
}

// Expire removes each entry Expired at cutoff and each with neither data nor state, and returns the first.
func (q quarantineStore) Expire(ctx context.Context, cutoff time.Time) ([]quarantine.Entry, error) {
	es, err := q.scan(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range es {
		expired := e.Expired(cutoff)
		if !expired && (e.hasData || e.state) {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, requestTimeout)
		err := q.r.eng.RemoveVolume(rctx, e.volume)
		cancel()
		if err != nil {
			return out, err
		}
		if expired {
			out = append(out, e.Entry)
		}
	}
	return out, nil
}

// engineAPI is the Engine of Docker's Engine API (Decision 1). It sends no pull credentials (Decision 2).
type engineAPI struct {
	http *http.Client
	base string // http://docker/v<version>
}

var _ Engine = (*engineAPI)(nil)

func (e *engineAPI) do(ctx context.Context, method, p string, q url.Values, body io.Reader, ctype string) (*http.Response, error) {
	u := e.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "docker", "%s %s", method, p)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "docker", "%s %s", method, p)
	}
	return resp, nil
}

// send sends a request and reads its answer whole, bounded. A status of 300 or more, but 304, is an error of its kind:
// 404 NotFound, 409 Conflict, else Internal, holding the engine's message.
func (e *engineAPI) send(ctx context.Context, method, p string, q url.Values, body []byte) (int, []byte, error) {
	var rd io.Reader
	ctype := ""
	if body != nil {
		rd, ctype = bytes.NewReader(body), "application/json"
	}
	resp, err := e.do(ctx, method, p, q, rd, ctype)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return resp.StatusCode, nil, fault.Wrapf(err, fault.Unavailable, "docker", "read the answer to %s %s", method, p)
	}
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode != http.StatusNotModified {
		return resp.StatusCode, b, engineError(method+" "+p, resp.StatusCode, b)
	}
	return resp.StatusCode, b, nil
}

func engineError(call string, code int, body []byte) error {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	msg := cmp.Or(m.Message, strings.TrimSpace(string(body)), http.StatusText(code))
	switch code {
	case http.StatusNotFound:
		return fault.NotFoundf("docker", "%s: %s", call, msg)
	case http.StatusConflict:
		return fault.Conflictf("docker", "%s: %s", call, msg)
	}
	return fault.Internalf("docker", "%s: %d %s", call, code, msg)
}

// labelFilter is the filters query of every label.
func labelFilter(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		pairs = append(pairs, k+"="+labels[k])
	}
	b, _ := json.Marshal(map[string][]string{"label": pairs})
	return string(b)
}

func (e *engineAPI) Containers(ctx context.Context, labels map[string]string) ([]string, error) {
	_, b, err := e.send(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"true"}, "filters": {labelFilter(labels)}}, nil)
	if err != nil {
		return nil, err
	}
	var list []struct {
		Names []string `json:"Names"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "docker", "decode the container list")
	}
	var out []string
	for _, c := range list {
		if len(c.Names) > 0 {
			out = append(out, strings.TrimPrefix(c.Names[0], "/"))
		}
	}
	return out, nil
}

func (e *engineAPI) Inspect(ctx context.Context, name string) (Container, bool, error) {
	code, b, err := e.send(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil)
	switch {
	case code == http.StatusNotFound:
		return Container{}, false, nil
	case err != nil:
		return Container{}, false, err
	}
	var in struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Ports map[string][]portBinding `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return Container{}, false, fault.Wrapf(err, fault.Internal, "docker", "decode the container %s", name)
	}
	c := Container{Running: in.State.Running, Labels: in.Config.Labels, Published: map[string]string{}}
	for port, binds := range in.NetworkSettings.Ports {
		for _, bd := range binds {
			if bd.HostPort != "" && (bd.HostIP == "" || bd.HostIP == "127.0.0.1") {
				c.Published[port] = net.JoinHostPort("127.0.0.1", bd.HostPort)
				break
			}
		}
	}
	return c, true, nil
}

func (e *engineAPI) Pull(ctx context.Context, image string) error {
	const op = "docker.Pull"
	code, _, err := e.send(ctx, http.MethodGet, "/images/"+image+"/json", nil, nil)
	switch {
	case code == http.StatusOK:
		return nil
	case code != http.StatusNotFound:
		return err
	}
	resp, err := e.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {image}}, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	pullErr := func(msg string) error {
		return fault.Unavailablef(op, "pull %s: %s: the Docker driver sends no credentials, so pull a private image first "+
			"with docker pull %s", image, msg, image)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
		return pullErr(engineError("POST /images/create", resp.StatusCode, b).Error())
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fault.Wrapf(err, fault.Unavailable, op, "read the pull of %s", image)
		}
		if m.Error != "" {
			return pullErr(m.Error)
		}
	}
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type createBody struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
	StopTimeout  *int                `json:"StopTimeout,omitempty"`
	HostConfig   createHostConfig    `json:"HostConfig"`
}

type createHostConfig struct {
	PortBindings  map[string][]portBinding `json:"PortBindings,omitempty"`
	RestartPolicy struct {
		Name string `json:"Name"`
	} `json:"RestartPolicy"`
	Mounts []mountBody `json:"Mounts,omitempty"`
}

type mountBody struct {
	Type          string `json:"Type"`
	Source        string `json:"Source"`
	Target        string `json:"Target"`
	VolumeOptions struct {
		NoCopy bool `json:"NoCopy"`
	} `json:"VolumeOptions"`
}

func volumeMount(volume, target string) mountBody {
	m := mountBody{Type: "volume", Source: volume, Target: target}
	m.VolumeOptions.NoCopy = true
	return m
}

// create creates a container of body named name and returns its id.
func (e *engineAPI) create(ctx context.Context, name string, body createBody) (string, error) {
	body.HostConfig.RestartPolicy.Name = "no"
	b, err := json.Marshal(body)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, "docker", "encode the container %s", name)
	}
	_, out, err := e.send(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, b)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.ID == "" {
		return "", fault.Internalf("docker", "the engine named no id for the container %s", name)
	}
	return created.ID, nil
}

func (e *engineAPI) Run(ctx context.Context, spec RunSpec) error {
	grace := int(spec.StopGrace / time.Second)
	port := portKey(spec.Port)
	body := createBody{Image: spec.Image, Labels: spec.Labels, ExposedPorts: map[string]struct{}{port: {}},
		StopSignal: "SIGTERM", StopTimeout: &grace}
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		body.Env = append(body.Env, k+"="+spec.Env[k])
	}
	body.HostConfig.PortBindings = map[string][]portBinding{port: {{HostIP: "127.0.0.1"}}}
	if spec.Volume != "" {
		body.HostConfig.Mounts = []mountBody{volumeMount(spec.Volume, spec.Target)}
	}
	id, err := e.create(ctx, spec.Name, body)
	if err != nil {
		return err
	}
	if _, _, err := e.send(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil); err != nil {
		e.removeQuietly(ctx, id)
		return err
	}
	return nil
}

func (e *engineAPI) Stop(ctx context.Context, name string, grace time.Duration) error {
	code, _, err := e.send(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/stop",
		url.Values{"t": {strconv.Itoa(int(grace / time.Second))}}, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}

func (e *engineAPI) Remove(ctx context.Context, name string) error {
	code, _, err := e.send(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name), url.Values{"force": {"true"}}, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}

// removeQuietly removes a container this driver made for one call, even when ctx is done.
func (e *engineAPI) removeQuietly(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	_ = e.Remove(ctx, id)
}

func (e *engineAPI) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	b, err := json.Marshal(struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}{name, labels})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "docker", "encode the volume %s", name)
	}
	_, _, err = e.send(ctx, http.MethodPost, "/volumes/create", nil, b)
	return err
}

func (e *engineAPI) Volumes(ctx context.Context, labels map[string]string) ([]Volume, error) {
	_, b, err := e.send(ctx, http.MethodGet, "/volumes", url.Values{"filters": {labelFilter(labels)}}, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Volumes []Volume `json:"Volumes"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "docker", "decode the volume list")
	}
	return list.Volumes, nil
}

func (e *engineAPI) RemoveVolume(ctx context.Context, name string) error {
	code, _, err := e.send(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}

// helperContainer creates h's container, never started unless argv is set; the caller removes it.
func (e *engineAPI) helperContainer(ctx context.Context, h Helper, argv []string) (string, error) {
	if err := e.Pull(ctx, h.Image); err != nil {
		return "", err
	}
	body := createBody{Image: h.Image, Cmd: argv, Labels: h.Labels}
	for _, m := range h.Mounts {
		body.HostConfig.Mounts = append(body.HostConfig.Mounts, volumeMount(m.Volume, m.Target))
	}
	return e.create(ctx, h.Name, body)
}

func (e *engineAPI) Exec(ctx context.Context, h Helper, argv ...string) ([]byte, error) {
	id, err := e.helperContainer(ctx, h, argv)
	if err != nil {
		return nil, err
	}
	defer e.removeQuietly(ctx, id)
	if _, _, err := e.send(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil); err != nil {
		return nil, err
	}
	_, b, err := e.send(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, nil)
	if err != nil {
		return nil, err
	}
	var waited struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := json.Unmarshal(b, &waited); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "docker", "decode the end of helper %s", h.Name)
	}
	_, logs, err := e.send(ctx, http.MethodGet, "/containers/"+id+"/logs", url.Values{"stdout": {"true"}, "stderr": {"true"}}, nil)
	if err != nil {
		return nil, err
	}
	stdout, stderr := demux(logs)
	if waited.StatusCode != 0 {
		return nil, fault.Internalf("docker", "helper %s exited %d: %s", h.Name, waited.StatusCode, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

// demux splits a container's log stream, framed by 8-byte headers when it has no terminal, into its two streams.
func demux(b []byte) (stdout, stderr []byte) {
	const header = 8
	for len(b) >= header {
		n := int(binary.BigEndian.Uint32(b[4:header]))
		end := min(header+n, len(b))
		switch b[0] {
		case 1:
			stdout = append(stdout, b[header:end]...)
		case 2:
			stderr = append(stderr, b[header:end]...)
		}
		b = b[end:]
	}
	return stdout, stderr
}

func (e *engineAPI) ReadTar(ctx context.Context, h Helper, p string) (io.ReadCloser, error) {
	id, err := e.helperContainer(ctx, h, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/archive", url.Values{"path": {p}}, nil, "")
	if err != nil {
		e.removeQuietly(ctx, id)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
		_ = resp.Body.Close()
		e.removeQuietly(ctx, id)
		return nil, engineError("GET archive "+p, resp.StatusCode, b)
	}
	return readCloser{Reader: resp.Body, close: func() error {
		err := resp.Body.Close()
		e.removeQuietly(ctx, id)
		return err
	}}, nil
}

func (e *engineAPI) WriteTar(ctx context.Context, h Helper, dir string, uid, gid int, archive io.Reader) error {
	id, err := e.helperContainer(ctx, h, nil)
	if err != nil {
		return err
	}
	defer e.removeQuietly(ctx, id)
	if err := e.makeDirs(ctx, id, dir, uid, gid); err != nil {
		return err
	}
	return e.putArchive(ctx, id, dir, archive)
}

// makeDirs makes dir's missing directories, from the deepest one that exists, with mode 0700, owned by uid and gid:
// the archive API extracts only into a directory that exists.
func (e *engineAPI) makeDirs(ctx context.Context, id, dir string, uid, gid int) error {
	segs := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	have := 0
	for have < len(segs) {
		resp, err := e.do(ctx, http.MethodHead, "/containers/"+id+"/archive", url.Values{"path": {"/" + path.Join(segs[:have+1]...)}}, nil, "")
		if err != nil {
			return err
		}
		httpx.CloseBody(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			break
		}
		if resp.StatusCode != http.StatusOK {
			return engineError("HEAD archive "+dir, resp.StatusCode, nil)
		}
		have++
	}
	if have == len(segs) {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := have; i < len(segs); i++ {
		hdr := &tar.Header{Name: path.Join(segs[have:i+1]...) + "/", Typeflag: tar.TypeDir, Mode: dirMode, Uid: uid, Gid: gid,
			ModTime: time.Now()}
		if err := tw.WriteHeader(hdr); err != nil {
			return fault.Wrapf(err, fault.Internal, "docker", "build the directories of %s", dir)
		}
	}
	if err := tw.Close(); err != nil {
		return fault.Wrapf(err, fault.Internal, "docker", "build the directories of %s", dir)
	}
	return e.putArchive(ctx, id, "/"+path.Join(segs[:have]...), &buf)
}

// putArchive extracts archive into dir of the container id, each entry keeping its owner.
func (e *engineAPI) putArchive(ctx context.Context, id, dir string, archive io.Reader) error {
	resp, err := e.do(ctx, http.MethodPut, "/containers/"+id+"/archive", url.Values{"path": {dir}, "copyUIDGID": {"true"}},
		archive, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
		return engineError("PUT archive "+dir, resp.StatusCode, b)
	}
	return nil
}
