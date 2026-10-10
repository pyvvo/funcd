//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/restore"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/workflow"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const restoreToken = "restore-admin-token-0123456789abcdef"

// restorePacing puts many held requeues and blob polls inside a test's still window (pitfall 5).
const restorePacing = "controller:\n  referentPollInterval: 100ms\neventing:\n  blobPollInterval: 100ms\n"

// gsource is a platform's three stores on disk, written directly and cut into generations on a file:// target with
// ADR-0203's Target.Write (ADR-0205's runner, which schedules it in the daemon, is built separately).
type gsource struct {
	dir    string
	meta   store.Store
	runs   runstate.Store
	events *eventstore.Store
	target string
	opts   backup.WriteOptions
}

func newGSource(t *testing.T, key []byte) *gsource {
	t.Helper()
	s := &gsource{dir: t.TempDir(), target: gocloud.FileURL(t.TempDir())}
	var opts []store.Option
	if key != nil {
		enc, err := aesgcm.NewAESEncryptor(key)
		require.NoError(t, err)
		opts = append(opts, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
		s.opts.Keys.SecretsKey = envelope.Fingerprint(key)
	}
	eng, err := badgerstore.Open(filepath.Join(s.dir, "store"))
	require.NoError(t, err)
	s.meta = store.New(eng, opts...)
	s.runs, err = runbadger.New(runbadger.Config{Dir: filepath.Join(s.dir, "workflow")})
	require.NoError(t, err)
	s.events, err = eventstore.Open(eventstore.Config{Dir: filepath.Join(s.dir, "deadletter")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.meta.Close()
		_ = s.runs.Close()
		_ = s.events.Close()
	})
	s.create(t, &v1.Namespace{TypeMeta: tmeta(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "team"}},
		&v1.ResourceGroup{TypeMeta: tmeta(v1.KindResourceGroup), ObjectMeta: om("rg")})
	return s
}

func (s *gsource) create(t *testing.T, objs ...v1.Object) {
	t.Helper()
	for _, o := range objs {
		_, err := s.meta.Create(context.Background(), o)
		require.NoError(t, err, "create %s %s", o.GroupVersionKind().Kind, o.GetName())
	}
}

// cut writes one generation of the three stores.
func (s *gsource) cut(t *testing.T) backup.Manifest {
	t.Helper()
	ctx := context.Background()
	tg, err := backup.Open(ctx, backup.Config{Target: s.target, DataDir: s.dir, Retention: backup.Retention{Hourly: 1},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	defer func() { _ = tg.Close() }()
	m, err := tg.Write(ctx, s.events, s.meta, s.runs, s.opts)
	require.NoError(t, err)
	return m
}

func tmeta(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

func om(name string) v1.ObjectMeta {
	return v1.ObjectMeta{Namespace: "team", ResourceGroup: "rg", Name: v1.ObjectName(name)}
}

func genSecret(name, value string) *v1.Secret {
	return &v1.Secret{TypeMeta: tmeta(v1.KindSecret), ObjectMeta: om(name),
		Spec: v1.SecretSpec{Data: map[string][]byte{"token": []byte(value)}}}
}

// lockedBuffer is a log sink the daemon's goroutines share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// node is a funcd node a restore loads into: a config file on a short data directory.
type node struct {
	dataDir, cfgPath, control string
	logs                      lockedBuffer
}

func newNode(t *testing.T, key []byte, extra string) *node {
	t.Helper()
	n := &node{dataDir: shortDataDir(t), control: freeAddr(t)}
	cfgDir := t.TempDir()
	if key != nil {
		keyFile := filepath.Join(cfgDir, "secrets.key")
		require.NoError(t, os.WriteFile(keyFile, key, 0o600))
		extra += fmt.Sprintf("secrets:\n  encryptionKeyFile: %q\n", keyFile)
	}
	tokenFile := filepath.Join(cfgDir, "admin.token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(restoreToken), 0o600))
	n.cfgPath = filepath.Join(cfgDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(n.cfgPath, fmt.Appendf(nil,
		"server:\n  listenAddr: %q\n  dataPlaneAddr: %q\nstorage:\n  mode: file\n  dataDir: %q\n"+
			"auth:\n  credentials:\n    - tokenFile: %q\n      role: admin\nlog:\n  level: warn\n%s",
		n.control, freeAddr(t), n.dataDir, tokenFile, extra), 0o600))
	return n
}

// restore runs `funcd restore <args> --config <node config>`.
func (n *node) restore(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs(append(append([]string{"restore"}, args...), "--config", n.cfgPath))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// start serves the node as the daemon does and returns a client once the API answers, and the stop.
func (n *node) start(t *testing.T) (*sdk.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = serve(ctx, n.cfgPath, nil, &n.logs)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Error("funcd did not stop")
			}
		})
	}
	t.Cleanup(stop)
	c, err := sdk.New("http://"+n.control, sdk.WithToken(restoreToken))
	require.NoError(t, err)
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("funcd stopped: %v\n%s", serveErr, n.logs.String())
		default:
		}
		_, err := c.HoldStatus(ctx)
		if err == nil {
			return c, stop
		}
		if time.Now().After(deadline) {
			t.Fatalf("funcd did not answer: %v\n%s", err, n.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func requireEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Empty(t, names, "%s holds what a failed restore left", dir)
}

func reportOf(t *testing.T, ev hold.Evidence) restore.Report {
	t.Helper()
	var r restore.Report
	require.NoError(t, json.Unmarshal(ev.Report, &r))
	return r
}

// scenario: restore-refuses-non-empty — a metastore directory holding data refuses the restore with a Conflict
// naming it, before the target is read (it is unreadable here) and with nothing written.
func TestScenarioRestoreRefusesNonEmpty(t *testing.T) {
	t.Parallel()
	s := newGSource(t, nil)
	s.cut(t)
	n := newNode(t, nil, "")
	storeDir := filepath.Join(n.dataDir, "store")
	require.NoError(t, os.MkdirAll(storeDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, "MANIFEST"), []byte("x"), 0o600))
	targetDir := strings.TrimPrefix(s.target, "file://")
	require.NoError(t, os.Chmod(targetDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(targetDir, 0o700) })

	_, err := n.restore("run", "latest", "--from", s.target)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, storeDir)
	for _, f := range []string{hold.MarkerFile, hold.BusyFile, hold.ReportFile} {
		require.NoFileExists(t, filepath.Join(n.dataDir, f))
	}
	entries, err := os.ReadDir(storeDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// scenario: restore-crash-refuses-start — what a restore run killed after its metastore Load leaves (restore.inprogress,
// the marker, a partial metastore) refuses the start with a Conflict naming it; once emptied, a new run boots held.
func TestScenarioRestoreCrashRefusesStart(t *testing.T) {
	t.Parallel()
	s := newGSource(t, nil)
	s.cut(t)
	n := newNode(t, nil, "")
	require.NoError(t, hold.Begin(n.dataDir, "run"))
	require.NoError(t, hold.Write(n.dataDir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	eng, err := badgerstore.Open(filepath.Join(n.dataDir, "store"))
	require.NoError(t, err)
	require.NoError(t, eng.Close())

	err = serve(context.Background(), n.cfgPath, nil, io.Discard)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "restore run")
	require.ErrorContains(t, err, "interrupted")

	_, err = n.restore("run", "latest", "--from", s.target)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "the partial metastore is still there")
	require.NoError(t, os.RemoveAll(filepath.Join(n.dataDir, "store")))
	_, err = n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	c, _ := n.start(t)
	ev, err := c.HoldStatus(context.Background())
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, "restore", ev.Marker.Reason)
}

// scenario: restore-keeps-data-owner — every entry a restore creates gets the data directory's owner (ADR-0026 §4).
// As root on Linux a second uid owns the data; otherwise the data directory gets another of this user's groups,
// which a process may set, and the case holds for the group.
func TestScenarioRestoreKeepsDataOwner(t *testing.T) {
	t.Parallel()
	s := newGSource(t, nil)
	s.cut(t)
	n := newNode(t, nil, "")
	uid, gid := -1, -1
	if os.Geteuid() == 0 && runtime.GOOS == "linux" {
		uid, gid = 1000, 1000
	} else {
		groups, err := os.Getgroups()
		require.NoError(t, err)
		for _, g := range groups {
			if g != os.Getgid() {
				gid = g
				break
			}
		}
		if gid < 0 {
			t.Skip("needs root on Linux, or a user with a second group")
		}
	}
	require.NoError(t, os.Lchown(n.dataDir, uid, gid))
	want := ownerOf(t, n.dataDir)

	_, err := n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	var created int
	require.NoError(t, filepath.WalkDir(n.dataDir, func(p string, _ fs.DirEntry, err error) error {
		require.NoError(t, err)
		got := ownerOf(t, p)
		require.Equal(t, want, got, "owner of %s", p)
		created++
		return nil
	}))
	require.Greater(t, created, 4)
	c, _ := n.start(t)
	ev, err := c.HoldStatus(context.Background())
	require.NoError(t, err)
	require.True(t, ev.Held)
}

func ownerOf(t *testing.T, p string) [2]uint32 {
	t.Helper()
	info, err := os.Lstat(p)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	return [2]uint32{st.Uid, st.Gid}
}

// scenario: restore-boots-held — G with a timer, a blob source, a Sensor and a run is restored; funcd lists G's
// objects and reports held, and across a restart nothing fires or dispatches: no Invocation, no new run, no dead
// letter, the run untouched, the blob key pending. The ADR's 1 s timer and 5 s run here as a 100 ms timer, 100 ms
// requeues and blob polls, and 500 ms on each side of the restart.
func TestScenarioRestoreBootsHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newGSource(t, nil)
	s.create(t,
		&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: om("inbox")},
		&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: om("tick"), Spec: v1.EventSourceSpec{
			Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}}},
		&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: om("files"), Spec: v1.EventSourceSpec{
			Blob: &v1.BlobSource{Bucket: "inbox", Events: []v1.BlobEvent{{Name: "new"}}}}},
		&v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: om("s"), Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}, {Name: "file", Source: "files", Event: "new"}},
			Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}, {Name: "b", On: "file", Workflow: "wf"}}}},
		&v1.WorkflowRun{TypeMeta: tmeta(v1.KindWorkflowRun), ObjectMeta: om("r1"), Spec: v1.WorkflowRunSpec{Workflow: "wf"}},
	)
	obj, err := s.meta.Get(ctx, v1.KindWorkflowRun.GVK(), "team", "r1")
	require.NoError(t, err)
	run := obj.(*v1.WorkflowRun)
	run.Status.Phase = v1.RunRunning
	_, err = s.meta.Update(ctx, run)
	require.NoError(t, err)
	s.cut(t)

	n := newNode(t, nil, restorePacing)
	_, err = n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(n.dataDir, "blob"), 0o700))
	b, err := gocloud.Open(ctx, gocloud.FileURL(filepath.Join(n.dataDir, "blob")))
	require.NoError(t, err)
	require.NoError(t, b.Put(ctx, "s3/team/inbox/a.txt", []byte("a"), blob.PutOptions{}))
	require.NoError(t, b.Close())

	c, stop := n.start(t)
	ev, err := c.HoldStatus(ctx)
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, []string{"team/r1"}, reportOf(t, ev).PausedByRestore)
	sources, err := c.List(ctx, v1.KindEventSource, "team")
	require.NoError(t, err)
	require.Len(t, sources, 2)
	before, err := c.Get(ctx, v1.KindWorkflowRun, "team", "r1")
	require.NoError(t, err)
	require.True(t, before.(*v1.WorkflowRun).Spec.Paused)

	still := func() {
		t.Helper()
		time.Sleep(500 * time.Millisecond)
		invs, err := c.List(ctx, v1.KindInvocation, "team")
		require.NoError(t, err)
		require.Empty(t, invs, "a held timer or blob source fired")
		runs, err := c.List(ctx, v1.KindWorkflowRun, "team")
		require.NoError(t, err)
		require.Len(t, runs, 1, "a held Sensor dispatched")
		require.Equal(t, before.GetObjectMeta().ResourceVersion, runs[0].GetObjectMeta().ResourceVersion, "a held run moved")
		dls, err := c.DeadLetters(ctx, "team")
		require.NoError(t, err)
		require.Empty(t, dls)
		ev, err := c.HoldStatus(ctx)
		require.NoError(t, err)
		require.True(t, ev.Held)
		require.Equal(t, map[string]int{"new": 1}, ev.Pending["team/files"])
	}
	still()
	stop()
	c, _ = n.start(t)
	still()
}

// scenario: restore-integrity — a metastore part of another sha256, or a Secret that does not open with the key the
// manifest names, fails the restore naming the part or the Secret, and leaves the data directory empty.
func TestScenarioRestoreIntegrity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := bytes.Repeat([]byte{1}, 32)
	s := newGSource(t, key)
	s.create(t, genSecret("s", "hunter2"))
	m := s.cut(t)
	b, err := gocloud.Open(ctx, s.target)
	require.NoError(t, err)
	part := fmt.Sprintf("gen/hourly/%010d-%s/metastore/part-00000", m.Generation, m.Timeline)
	data, err := b.Get(ctx, part)
	require.NoError(t, err)
	data[len(data)/2] ^= 0xff
	require.NoError(t, b.Put(ctx, part, data, blob.PutOptions{}))
	require.NoError(t, b.Close())
	n := newNode(t, key, "")
	_, err = n.restore("run", "latest", "--from", s.target)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "store metastore")
	require.ErrorContains(t, err, fmt.Sprintf("%s/%d", m.Timeline, m.Generation))
	requireEmptyDir(t, n.dataDir)

	other := newGSource(t, bytes.Repeat([]byte{2}, 32))
	other.opts.Keys.SecretsKey = envelope.Fingerprint(key)
	other.create(t, genSecret("s", "hunter2"))
	other.cut(t)
	n = newNode(t, key, "")
	_, err = n.restore("run", "latest", "--from", other.target)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "Secret team/s")
	requireEmptyDir(t, n.dataDir)
}

// scenario: version-rule — a generation of a newer minor is Invalid naming both versions; one of an older minor
// restores and its first start runs the start steps (the ADR-0178 KVStore migration records its completion). It
// sets the process-wide version.Version, so it stays serial.
func TestScenarioVersionRule(t *testing.T) {
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "v0.9.0"
	newer := newGSource(t, nil)
	newer.cut(t)
	version.Version = "v0.7.2"
	older := newGSource(t, nil)
	older.cut(t)
	version.Version = "v0.8.1"

	n := newNode(t, nil, "")
	_, err := n.restore("run", "latest", "--from", newer.target)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "v0.9.0")
	require.ErrorContains(t, err, "v0.8.1")
	requireEmptyDir(t, n.dataDir)

	_, err = n.restore("run", "latest", "--from", older.target)
	require.NoError(t, err)
	c, _ := n.start(t)
	_, err = c.Get(context.Background(), v1.KindConfigMap, workflow.KVMigrationNamespace, workflow.KVMigrationRecord)
	require.NoError(t, err, "the start steps ran at the first start")
}

// scenario: restore-points — T2 restored from T1/40: list shows T2 under T1/40 and T1/41 abandoned; latest is T2/43;
// the resourceVersion of 41's revision and that of 43's both resolve to T1/40.
func TestScenarioRestorePoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const tl1, tl2 = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	dir := t.TempDir()
	b, err := gocloud.Open(ctx, gocloud.FileURL(dir))
	require.NoError(t, err)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, m := range []backup.Manifest{
		{Generation: 40, Timeline: tl1, Revision: 100, At: v1.NewTimestamp(t0)},
		{Generation: 41, Timeline: tl1, Revision: 150, At: v1.NewTimestamp(t0.Add(time.Hour))},
		{Generation: 43, Timeline: tl2, Revision: 20, At: v1.NewTimestamp(t0.Add(2 * time.Hour)),
			Parent: &backup.GenRef{Timeline: tl1, Generation: 40}},
	} {
		m.Format, m.Funcd = backup.Format, "v0.8.0"
		data, err := yaml.Marshal(m)
		require.NoError(t, err)
		key := fmt.Sprintf("gen/hourly/%010d-%s/manifest.yaml", m.Generation, m.Timeline)
		require.NoError(t, b.Put(ctx, key, data, blob.PutOptions{}))
	}
	n := newNode(t, nil, "")
	out, err := n.restore("list", "--from", gocloud.FileURL(dir))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 4, out)
	require.True(t, strings.HasPrefix(lines[1], tl1+"/40 "), out)
	require.True(t, strings.HasPrefix(lines[2], "  "+tl2+"/43 "), "T2 is under T1/40:\n%s", out)
	require.True(t, strings.HasPrefix(lines[3], tl1+"/41 "), out)
	require.True(t, strings.HasSuffix(lines[3], "abandoned"), out)

	gens, err := restore.List(ctx, b)
	require.NoError(t, err)
	for point, want := range map[string]string{"latest": tl2 + "/43", tl1 + "-150": tl1 + "/40", tl2 + "-20": tl1 + "/40"} {
		p, err := restore.ParsePoint(point)
		require.NoError(t, err)
		g, err := restore.Resolve(gens, p)
		require.NoError(t, err, point)
		require.Equal(t, want, fmt.Sprintf("%s/%d", g.Manifest.Timeline, g.Manifest.Generation), point)
	}
	require.NoError(t, b.Close())
}

// scenario: single-secret-restore — inspect --object prints a deleted Secret by key with no value; with
// --secrets-key and --reveal-secrets its values, and `funcdctl apply -f` of that output brings it back.
func TestScenarioSingleSecretRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := bytes.Repeat([]byte{3}, 32)
	s := newGSource(t, key)
	s.create(t, genSecret("s", "hunter2"))
	s.cut(t)
	orig, err := s.meta.Get(ctx, v1.KindSecret.GVK(), "team", "s")
	require.NoError(t, err)
	keyFile := filepath.Join(t.TempDir(), "escrowed.key")
	require.NoError(t, os.WriteFile(keyFile, key, 0o600))
	value := base64.StdEncoding.EncodeToString([]byte("hunter2"))

	n := newNode(t, key, "")
	out, err := n.restore("inspect", "latest", "--from", s.target, "--object", "Secret/team/s")
	require.NoError(t, err)
	require.Contains(t, out, "name: s")
	require.Contains(t, out, "namespace: team")
	require.Contains(t, out, "do not decode without --secrets-key")
	require.NotContains(t, out, value)
	out, err = n.restore("inspect", "latest", "--from", s.target, "--object", "Secret/team/s", "--secrets-key", keyFile)
	require.NoError(t, err)
	require.Contains(t, out, base64.StdEncoding.EncodeToString([]byte("REDACTED")))
	require.NotContains(t, out, value)
	out, err = n.restore("inspect", "latest", "--from", s.target, "--object", "Secret/team/s", "--secrets-key", keyFile,
		"--reveal-secrets")
	require.NoError(t, err)
	require.Contains(t, out, value)
	requireEmptyDir(t, n.dataDir)

	_, err = n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	c, _ := n.start(t)
	require.NoError(t, c.Delete(ctx, v1.KindSecret, "team", "s"))
	obj, err := sdk.DecodeManifest([]byte(out))
	require.NoError(t, err)
	_, err = c.Apply(ctx, obj)
	require.NoError(t, err)
	got, err := c.Get(ctx, v1.KindSecret, "team", "s")
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), got.(*v1.Secret).Spec.Data["token"])
	require.NotEqual(t, orig.GetObjectMeta().UID, got.GetObjectMeta().UID)
}

// scenario: dead-letters-held — a restored dead letter lists by its id; its replay is Unavailable while held, nothing
// redelivers it after the release, and then its replay works.
func TestScenarioDeadLettersHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newGSource(t, nil)
	s.create(t, &v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: om("s"), Spec: v1.SensorSpec{
		On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}},
		Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}}}})
	ce, err := eventing.NewNamedEvent("team", "tick", "t")
	require.NoError(t, err)
	payload, err := json.Marshal(ce)
	require.NoError(t, err)
	const id = "01JA0000000000000000000001"
	require.NoError(t, s.events.DeadLetters().Put(ctx, deadletter.DeadLetter{ID: id, Namespace: "team", Sensor: "s",
		Source: "tick", Event: "t", Action: "a", Payload: payload, Attempts: 3, Reason: "boom",
		FailedAt: v1.NewTimestamp(time.Now())}))
	s.cut(t)

	n := newNode(t, nil, restorePacing)
	_, err = n.restore("run", "latest", "--from", s.target)
	require.NoError(t, err)
	c, _ := n.start(t)
	dls, err := c.DeadLetters(ctx, "team")
	require.NoError(t, err)
	require.Len(t, dls, 1)
	require.Equal(t, id, dls[0].ID)
	require.Equal(t, fault.Unavailable, fault.KindOf(c.ReplayDeadLetter(ctx, "team", id)))

	require.NoError(t, c.ReleaseHold(ctx, nil))
	time.Sleep(500 * time.Millisecond)
	dls, err = c.DeadLetters(ctx, "team")
	require.NoError(t, err)
	require.Len(t, dls, 1, "nothing redelivers a dead letter")
	runs, err := c.List(ctx, v1.KindWorkflowRun, "team")
	require.NoError(t, err)
	require.Empty(t, runs)
	require.NoError(t, c.ReplayDeadLetter(ctx, "team", id))
	dls, err = c.DeadLetters(ctx, "team")
	require.NoError(t, err)
	require.Empty(t, dls)
	runs, err = c.List(ctx, v1.KindWorkflowRun, "team")
	require.NoError(t, err)
	require.Len(t, runs, 1)
}

// scenario: first-drill — a seeded platform, its generation sealed to two age recipients with its keys escrowed, is
// restored into a scratch directory: the integrity checks pass, the counts per kind equal the source's, a read of
// each object works on the node booted held (never released), and the time is logged.
func TestScenarioFirstDrill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start := time.Now()
	key, master := bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 32)
	keys := t.TempDir()
	var recipients []string
	var identity string
	for i := range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		p := filepath.Join(keys, fmt.Sprintf("recipient-%d.txt", i))
		require.NoError(t, os.WriteFile(p, []byte(id.Recipient().String()+"\n"), 0o600))
		recipients = append(recipients, p)
		if i == 1 {
			identity = filepath.Join(keys, "recovery.key")
			require.NoError(t, os.WriteFile(identity, []byte(id.String()+"\n"), 0o600))
		}
	}
	sealer, err := envelope.New(envelope.Config{Recipients: recipients, SecretsKey: key, Master: master,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	escrowDir := t.TempDir()
	for sub, data := range map[string][]byte{"secrets": key, "master": master} {
		require.NoError(t, os.MkdirAll(filepath.Join(escrowDir, sub), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(escrowDir, sub, "node.key"), data, 0o600))
	}

	s := newGSource(t, key)
	s.opts = backup.WriteOptions{Seal: sealer.Seal(), Keys: sealer.Keys()}
	seeded := []v1.Object{
		&v1.ConfigMap{TypeMeta: tmeta(v1.KindConfigMap), ObjectMeta: om("settings"), Spec: v1.ConfigMapSpec{Data: map[string]string{"k": "v"}}},
		genSecret("api", "hunter2"),
		&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: om("inbox")},
		&v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: om("cache")},
		&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: om("tick"), Spec: v1.EventSourceSpec{
			Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(time.Second)}}}}},
		&v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: om("s"), Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}},
			Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}}}},
		&v1.WorkflowRun{TypeMeta: tmeta(v1.KindWorkflowRun), ObjectMeta: om("r1"), Spec: v1.WorkflowRunSpec{Workflow: "wf"}},
	}
	s.create(t, seeded...)
	want, err := restore.Counts(ctx, s.meta)
	require.NoError(t, err)
	s.cut(t)

	n := newNode(t, key, "")
	_, err = n.restore("run", "latest", "--from", s.target, "--identity", identity, "--escrow", escrowDir)
	require.NoError(t, err)
	got, err := os.ReadFile(s3gateway.MasterPath("", n.dataDir))
	require.NoError(t, err, "the escrowed master is installed")
	require.Equal(t, master, got)
	c, _ := n.start(t)
	ev, err := c.HoldStatus(ctx)
	require.NoError(t, err)
	require.True(t, ev.Held)
	require.Equal(t, want, reportOf(t, ev).Counts)
	for _, o := range append(seeded, &v1.Namespace{ObjectMeta: v1.ObjectMeta{Name: "team"}}) {
		kind := o.GroupVersionKind().Kind
		_, err := c.Get(ctx, kind, o.GetNamespace(), o.GetName())
		require.NoError(t, err, "read %s %s", kind, o.GetName())
	}
	t.Logf("first drill: restore and held boot took %s", time.Since(start).Round(time.Millisecond))
}
