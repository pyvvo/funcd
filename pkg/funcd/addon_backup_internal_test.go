package funcd

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/testkit/fakeaddon"
)

// The fake add-on's folders (ADR-0333 Decision 1): its engines write their snapshots under backups and load a copy
// from restore at a start with fakeaddon.RestoreEnv.
const (
	fakeSnapshots = "/app/data/backups"
	fakeRestore   = "/app/data/restore"
	loginHost     = "login.example.com"
)

// loginEngine is the engine of orders/login.
func loginEngine() provider.ProviderRef {
	return provider.ProviderRef{Namespace: "orders", Name: "login"}
}

// fakeAddon registers kind with an engine that keeps its items in its storage, snapshots them and restores them, as
// rauthy's hiqlite does; timeout 0 is restore.timeout unset.
func fakeAddon(name v1.ObjectName, kind string, timeout v1.Duration) *v1.Addon {
	a := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: name}}
	a.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: v1.RuntimeName(strings.ToLower(kind)), Image: "ghcr.io/pyvvo/funcd-fake" + addonDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: kind, Port: 8080, Readiness: v1.AddonReadiness{Path: "/ping"}, Storage: &v1.AddonStorage{Path: "/app/data", UID: os.Geteuid(), GID: os.Getegid()}, Snapshot: &v1.AddonSnapshot{Path: fakeSnapshots}, Restore: &v1.AddonRestore{Path: fakeRestore, Timeout: timeout,
			Env: map[string]string{fakeaddon.RestoreEnv: `${{ "file:" + restore.file }}`}}}}}}
	return a
}

// addonObject is the object name of kind in orders' group rg1, exposed at name.example.com, as funcd stores it.
func addonObject(name v1.ObjectName, kind string) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: tmeta(v1.KindAddonObject), ObjectMeta: ordersMeta(string(name))}
	o.Spec.Kind = kind
	o.Spec.Ingress = &v1.Ingress{Host: string(name) + ".example.com"}
	return o
}

// clientObject is addonObject as a client writes it, under its add-on's kind.
func clientObject(name v1.ObjectName, kind string) *v1.AddonObject {
	o := addonObject(name, kind)
	o.TypeMeta = v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.Kind(kind)}
	return o
}

// addonRig is the workload backup rig over the fake add-on runtime, with the Addon fake and the IdentityProvider login,
// Ready.
func addonRig(t *testing.T, timeout v1.Duration) (*wbRig, *fakeaddon.Runtime) {
	t.Helper()
	rt := fakeaddon.New(t)
	r := newWBRig(t, wbOptions{opts: []Option{WithAddonRuntime(rt)}, setup: func(r *wbRig) {
		r.seed(fakeAddon("fake", "IdentityProvider", timeout), addonObject("login", "IdentityProvider"))
	}})
	r.waitAddon("login", func(o *v1.AddonObject) bool { return o.Status.Phase == v1.PhaseReady })
	return r, rt
}

func (r *wbRig) addon(name v1.ObjectName) (*v1.AddonObject, bool) {
	obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "orders", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(r.t, err)
	return obj.(*v1.AddonObject), true
}

// waitAddon waits until the stored object name satisfies cond.
func (r *wbRig) waitAddon(name v1.ObjectName, cond func(*v1.AddonObject) bool) *v1.AddonObject {
	r.t.Helper()
	var o *v1.AddonObject
	require.Eventually(r.t, func() bool {
		var ok bool
		o, ok = r.addon(name)
		return ok && cond(o)
	}, 15*time.Second, 10*time.Millisecond, "add-on object %s\n%s", name, r.logs.String())
	return o
}

// item calls the engine of login through the edge: PUT writes key, GET reads it. It returns the status and the body.
func (r *wbRig) item(method, key, value string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequestWithContext(r.ctx, method, "http://"+r.p.DataPlaneAddr()+"/items/"+key, strings.NewReader(value))
	require.NoError(r.t, err)
	req.Host = loginHost
	res, err := httpx.NodeClient(5 * time.Second).Do(req)
	require.NoError(r.t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	require.NoError(r.t, err)
	return res.StatusCode, string(b)
}

// put writes key through the edge once the edge serves login, and requires its acknowledgment.
func (r *wbRig) putItem(key, value string) {
	r.t.Helper()
	require.Eventually(r.t, func() bool {
		status, _ := r.item(http.MethodPut, key, value)
		return status == http.StatusNoContent
	}, 10*time.Second, 10*time.Millisecond, "the engine acknowledges %s", key)
}

// loginSchedule copies the IdentityProvider login.
func (r *wbRig) loginSchedule() *v1.BackupSchedule {
	return r.schedule("login-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Addons: []v1.ObjectName{"login"}}
	})
}

// backedUp writes client-a, snapshots it, and takes Backup b1 of login.
func (r *wbRig) backedUp(rt *fakeaddon.Runtime) *v1.Backup {
	r.t.Helper()
	r.putItem("client-a", "before")
	rt.Engine(loginEngine()).Snapshot(fakeSnapshots)
	r.create(r.dev, r.loginSchedule())
	r.create(r.dev, r.backupObj("b1", "login-nightly"))
	return r.waitBackup("b1", v1.RunSucceeded)
}

// copies are the files of login's restore.path.
func copies(t *testing.T, rt *fakeaddon.Runtime) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(rt.Dir(loginEngine()), "restore"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// loginInPlace restores login in place.
func loginInPlace() v1.RestoreMember {
	return v1.RestoreMember{Kind: "IdentityProvider", Name: "login", InPlace: true}
}

// withoutRestoreEnv reports an engine that runs and answers, last started without restore.env.
func withoutRestoreEnv(e *fakeaddon.Engine) bool {
	running, ready, env := e.State()
	_, restoring := env[fakeaddon.RestoreEnv]
	return running && ready && !restoring
}

// scenario: addon-backup-member — a BackupSchedule whose scope.addons names login: its Backup lists IdentityProvider/login
// with its bytes, and the copy is the newest snapshot as it is.
func TestScenarioAddonBackupMember(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	r.putItem("client-a", "before")
	e := rt.Engine(loginEngine())
	e.Snapshot(fakeSnapshots)
	r.putItem("client-b", "after the snapshot")
	newest := e.Snapshot(fakeSnapshots)
	snap, err := os.ReadFile(filepath.Join(rt.Dir(loginEngine()), "backups", newest))
	require.NoError(t, err)

	r.create(r.dev, r.loginSchedule())
	r.create(r.dev, r.backupObj("b1", "login-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Len(t, b.Status.Members, 1)
	require.Equal(t, v1.Kind("IdentityProvider"), b.Status.Members[0].Kind)
	require.Equal(t, v1.ObjectName("login"), b.Status.Members[0].Name)
	require.Positive(t, b.Status.Members[0].Bytes)
	require.Equal(t, map[string]string{"000001": string(snap)}, r.records(b.Status.Path, "IdentityProvider", "login", r.recovery),
		"the newest snapshot, copied as it is")
}

// scenario: addon-backup-no-snapshot — an engine that has written no snapshot fails the Backup NoSnapshot.
func TestScenarioAddonBackupNoSnapshot(t *testing.T) {
	t.Parallel()
	r, _ := addonRig(t, 0)
	r.create(r.dev, r.loginSchedule())
	r.create(r.dev, r.backupObj("b1", "login-nightly"))
	b := r.waitBackup("b1", v1.RunFailed)
	require.Equal(t, "NoSnapshot", reason(b.Status.Status))
}

// scenario: addon-backup-group-scope — a group's Backup copies login, and not echo, whose Addon declares no snapshot; a
// schedule naming echo fails NoSnapshot, saying its add-on declares no snapshot.path.
func TestScenarioAddonBackupGroupScope(t *testing.T) {
	t.Parallel()
	rt := fakeaddon.New(t)
	whoami := &v1.Addon{TypeMeta: tmeta(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "whoami"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "echoservice", Image: "ghcr.io/pyvvo/whoami" + addonDigest, Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "EchoService", Port: 80, Readiness: v1.AddonReadiness{Path: "/ping"}}}}}}
	r := newWBRig(t, wbOptions{opts: []Option{WithAddonRuntime(rt)}, setup: func(r *wbRig) {
		r.seed(fakeAddon("fake", "IdentityProvider", 0), whoami, addonObject("login", "IdentityProvider"), addonObject("echo", "EchoService"))
	}})
	for _, name := range []v1.ObjectName{"login", "echo"} {
		r.waitAddon(name, func(o *v1.AddonObject) bool { return o.Status.Phase == v1.PhaseReady })
	}
	r.putItem("client-a", "before")
	rt.Engine(loginEngine()).Snapshot(fakeSnapshots)

	r.create(r.dev, r.schedule("group-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{ResourceGroup: "rg1"}
	}))
	r.create(r.dev, r.backupObj("b1", "group-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	var kinds []string
	for _, m := range b.Status.Members {
		kinds = append(kinds, string(m.Kind)+"/"+string(m.Name))
	}
	require.Contains(t, kinds, "IdentityProvider/login")
	require.NotContains(t, kinds, "EchoService/echo", "echo's Addon declares no snapshot")

	r.create(r.dev, r.schedule("echo-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Addons: []v1.ObjectName{"echo"}}
	}))
	r.create(r.dev, r.backupObj("b2", "echo-nightly"))
	b = r.waitBackup("b2", v1.RunFailed)
	require.Equal(t, "NoSnapshot", reason(b.Status.Status))
	c, _ := b.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "the add-on whoami declares no snapshot.path")
}

// scenario: addon-restore-in-place — a client written before the Backup and one after it: the in-place Restore of login
// succeeds once the engine answers again without restore.env, the first client is there, the second is not, and
// restore.path is empty. The engine started twice, with restore.env and then without, under the hold.
//
// oracle: docs/design/addon/restore-start.fizz#TheCopyIsLoaded
// oracle: docs/design/addon/restore-start.fizz#TheRestoreEnds
func TestScenarioAddonRestoreInPlace(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	r.backedUp(rt)
	r.putItem("client-b", "after")
	e := rt.Engine(loginEngine())
	before := len(e.Starts())

	r.create(r.admin, r.restoreObj("rewind", "b1", loginInPlace()))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.True(t, withoutRestoreEnv(e), "the Restore ends once the engine answers again without restore.env")
	starts := e.Starts()[before:]
	require.Len(t, starts, 2, "two starts: %v", starts)
	require.Equal(t, "file:"+fakeRestore+"/"+r.restoredFile(t), starts[0][fakeaddon.RestoreEnv], "the first loads the copy")
	require.NotContains(t, starts[1], fakeaddon.RestoreEnv)
	status, body := r.item(http.MethodGet, "client-a", "")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "before", body)
	status, _ = r.item(http.MethodGet, "client-b", "")
	require.Equal(t, http.StatusNotFound, status, "a write after the Backup is gone")
	require.Empty(t, copies(t, rt), "restore.path is empty")
	o, _ := r.addon("login")
	require.Nil(t, o.Status.Restore)
	require.False(t, r.hold.HeldIn("orders"), "a Restore that succeeded lifts its hold")
}

// restoredFile is the snapshot file the copy of Backup b1 holds.
func (r *wbRig) restoredFile(t *testing.T) string {
	t.Helper()
	b, _ := r.getBackup("b1")
	src := r.target()
	keys, _, err := src.ListAfter(r.ctx, b.Status.Path+"addon/IdentityProvider/login/", "", 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	return strings.Split(strings.TrimPrefix(keys[0].Key, b.Status.Path+"addon/IdentityProvider/login/"), "/")[0]
}

// scenario: addon-restore-no-rollback — while the engine runs with restore.env, the namespace refuses writes, and a
// crash then reloads the copy; once the Restore has succeeded, a write the engine acknowledged survives its crash.
//
// oracle: docs/design/addon/restore-start.fizz#NoAckedWriteRolledBack
func TestScenarioAddonRestoreNoRollback(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	r.backedUp(rt)
	e := rt.Engine(loginEngine())
	rt.Stall(true)
	r.create(r.admin, r.restoreObj("rewind", "b1", loginInPlace()))
	require.Eventually(t, func() bool {
		running, _, env := e.State()
		_, restoring := env[fakeaddon.RestoreEnv]
		return running && restoring
	}, 15*time.Second, 10*time.Millisecond, "the engine runs with restore.env")
	require.True(t, r.hold.HeldIn("orders"))
	status, _ := r.item(http.MethodPut, "client-c", "during")
	require.Contains(t, []int{http.StatusNotFound, http.StatusServiceUnavailable}, status,
		"no write reaches an engine running with restore.env: no entry until it is Ready, and the hold refuses calls")
	e.Crash()
	require.Eventually(t, func() bool {
		running, _, env := e.State()
		_, restoring := env[fakeaddon.RestoreEnv]
		return running && restoring
	}, 15*time.Second, 10*time.Millisecond, "the next engine loads the copy again")

	rt.Stall(false)
	r.waitRestore("rewind", v1.RunSucceeded)
	r.putItem("client-c", "after the restore")
	e.Crash()
	require.Eventually(t, func() bool { return withoutRestoreEnv(e) }, 15*time.Second, 10*time.Millisecond,
		"the engine starts again without restore.env")
	require.Equal(t, map[string]string{"client-a": "before", "client-c": "after the restore"}, e.Items(),
		"no acknowledged write is rolled back")
}

// scenario: addon-restore-timeout — restore.timeout 2s and an engine that never turns Ready with restore.env: the
// Restore fails RestoreTimedOut, restore.path is empty, status.restore is gone, the engine runs without restore.env, and
// the namespace stays held.
func TestScenarioAddonRestoreTimeout(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, v1.Duration(2*time.Second))
	r.backedUp(rt)
	rt.Stall(true)
	r.create(r.admin, r.restoreObj("rewind", "b1", loginInPlace()))
	rs := r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "RestoreTimedOut", reason(rs.Status.Status))
	r.waitAddon("login", func(o *v1.AddonObject) bool { return o.Status.Restore == nil })
	require.Empty(t, copies(t, rt))
	require.Eventually(t, func() bool { return withoutRestoreEnv(rt.Engine(loginEngine())) }, 15*time.Second, 10*time.Millisecond,
		"the engine runs without restore.env")
	require.True(t, r.hold.HeldIn("orders"), "the namespace stays held")
}

// scenario: addon-restore-interrupted — funcd killed while the engine runs with restore.env: at the next start the
// Restore is Failed (Interrupted), restore.path is empty, status.restore is gone, the engine runs without restore.env
// over the crash marker the boot sweep's kill left, and the namespace stays held.
//
// oracle: docs/design/addon/restore-start.fizz#TheEngineRunsWithoutRestoreEnv
func TestScenarioAddonRestoreInterrupted(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	r.backedUp(rt)
	e := rt.Engine(loginEngine())
	rt.Stall(true)
	r.create(r.admin, r.restoreObj("rewind", "b1", loginInPlace()))
	r.waitAddon("login", func(o *v1.AddonObject) bool {
		running, _, env := e.State()
		_, restoring := env[fakeaddon.RestoreEnv]
		return o.Status.Restore != nil && running && restoring
	})

	r.stop()
	e.Kill()
	rt.Stall(false)
	r.restart()
	rs := r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "Interrupted", reason(rs.Status.Status))
	r.waitAddon("login", func(o *v1.AddonObject) bool { return o.Status.Restore == nil })
	require.Empty(t, copies(t, rt))
	require.Eventually(t, func() bool { return withoutRestoreEnv(e) }, 15*time.Second, 10*time.Millisecond,
		"the engine runs without restore.env")
	require.True(t, r.hold.HeldIn("orders"), "the namespace stays held")
	require.Equal(t, v1.ObjectName("rewind"), r.hold.NamespaceMarkers()["orders"][hold.ReasonRestore].Restore)
}

// scenario: addon-restore-in-place-only — a Restore member of kind IdentityProvider that sets as is refused.
func TestScenarioAddonRestoreInPlaceOnly(t *testing.T) {
	t.Parallel()
	r, _ := addonRig(t, 0)
	_, err := r.admin.Create(r.ctx, r.restoreObj("elsewhere", "b1", v1.RestoreMember{Kind: "IdentityProvider", Name: "login", As: "other"}))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "restores in place only")
}

// scenario: addon-deletion — deletion delete removes the storage; the default retain moves it into the quarantine, which
// hold status lists, and a new object of that name starts empty.
func TestScenarioAddonDeletion(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	gone := clientObject("gone", "IdentityProvider")
	gone.Spec.Deletion = v1.DeletionDelete
	r.create(r.dev, gone)
	r.waitAddon("gone", func(o *v1.AddonObject) bool { return o.Status.Phase == v1.PhaseReady })
	goneRef := provider.ProviderRef{Namespace: "orders", Name: "gone"}
	require.NoError(t, os.WriteFile(filepath.Join(rt.Dir(goneRef), "data.json"), []byte(`{"x":"y"}`), 0o600))
	require.NoError(t, r.dev.Delete(r.ctx, "IdentityProvider", "orders", "gone"))
	require.Eventually(t, func() bool {
		_, err := os.Stat(rt.Dir(goneRef))
		return os.IsNotExist(err)
	}, 10*time.Second, 10*time.Millisecond, "deletion delete removes the storage")

	r.putItem("client-a", "before")
	require.NoError(t, r.dev.Delete(r.ctx, "IdentityProvider", "orders", "login"))
	require.Eventually(t, func() bool {
		ev, err := r.admin.HoldStatus(r.ctx)
		require.NoError(t, err)
		for _, q := range ev.Quarantine {
			if q.Kind == v1.KindAddonObject && q.Namespace == "orders" && q.Name == "login" {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "hold status lists the retained storage in the quarantine")
	r.create(r.dev, clientObject("login", "IdentityProvider"))
	r.waitAddon("login", func(o *v1.AddonObject) bool { return o.Status.Phase == v1.PhaseReady })
	require.Eventually(t, func() bool {
		status, _ := r.item(http.MethodGet, "client-a", "")
		return status == http.StatusNotFound
	}, 10*time.Second, 10*time.Millisecond, "a new object of that name starts empty")
}

// ADR-0332 Decision 3: a backup target at an add-on engine's address is refused, whatever the schedule's owner names.
func TestAddonBackupTargetIsNeverAnEngine(t *testing.T) {
	t.Parallel()
	r, rt := addonRig(t, 0)
	r.backedUp(rt)
	addr := rt.Engine(loginEngine()).Address()
	r.create(r.dev, r.schedule("at-the-engine", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Addons: []v1.ObjectName{"login"}}
		s.Target.URL = "s3://backups?region=us-east-1&use_path_style=true&endpoint=http://" + addr
	}))
	r.create(r.dev, r.backupObj("b2", "at-the-engine"))
	b := r.waitBackup("b2", v1.RunFailed)
	c, _ := b.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, addr+" is an add-on engine's address, which a backup target never is")
}

// ADR-0332 Decision 3 through the platform's wiring: an engine that the add-on runtime still runs in a namespace with no
// add-on object left counts for the gateway's engine index, which reads the runtime's namespaces, and for a backup
// target.
func TestAStoppingEngineCountsThroughThePlatform(t *testing.T) {
	t.Parallel()
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	r := newWBRig(t, wbOptions{opts: []Option{WithRuntime(rt)}})
	_, err := r.p.addon.engines.Converge(r.ctx, provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "gone", Name: "old"},
		Image: "example.test/engine@sha256:" + strings.Repeat("a", 64), Port: 8080, Replicas: 1})
	require.NoError(t, err)
	rt.mu.Lock()
	for id, in := range rt.insts {
		if in.OwnerKind == v1.KindAddonObject {
			in.IP = "10.63.0.11"
			rt.insts[id] = in
		}
	}
	rt.mu.Unlock()
	require.Equal(t, []v1.NamespaceName{"gone"}, r.p.exec.addonNamespaces())

	r.create(r.dev, r.schedule("at-the-engine", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Target.URL = "s3://backups?region=us-east-1&use_path_style=true&endpoint=http://10.63.0.11:9000"
	}))
	r.create(r.dev, r.backupObj("b3", "at-the-engine"))
	b := r.waitBackup("b3", v1.RunFailed)
	c, _ := b.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, "10.63.0.11:9000 is an add-on engine's address, which a backup target never is")
}
