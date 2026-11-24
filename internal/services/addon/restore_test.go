package addon_test

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/fakeaddon"
)

// restoreRig is a reconciler over the fake engines, with an Addon whose engines snapshot under backups and load a copy
// from restore through fakeaddon.RestoreEnv, and the IdentityProvider default/login.
type restoreRig struct {
	ctx  context.Context
	st   store.Store
	rt   *fakeaddon.Runtime
	r    *addon.Reconciler
	held *heldGate
}

// heldGate is a platform hold a test turns on.
type heldGate struct {
	hold.Gate
	on bool
}

func (g *heldGate) Held() bool { return g.on }

// loginRef is the engine of default/login.
func loginRef() provider.ProviderRef {
	return provider.ProviderRef{Namespace: "default", Name: "login"}
}

func newRestoreRig(t *testing.T) *restoreRig {
	t.Helper()
	ctx := context.Background()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "fake"},
		Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/fake@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809", Workloads: []v1.Workload{v1.WorkloadEngine},
			Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, Readiness: v1.AddonReadiness{Path: "/ping"}, Env: map[string]string{"MODE": "serve"}, Storage: &v1.AddonStorage{Path: "/app/data", UID: os.Geteuid(), GID: os.Getegid()}, Snapshot: &v1.AddonSnapshot{Path: "/app/data/backups"}, Restore: &v1.AddonRestore{Path: "/app/data/restore",
				Env: map[string]string{fakeaddon.RestoreEnv: `${{ "file:" + restore.file }}`}}}}}}}
	register(t, st, a)
	rt := fakeaddon.New(t)
	g := &heldGate{Gate: hold.Never}
	r, err := addon.NewReconciler(addon.Deps{Store: st, Provider: rt, RunsImages: true,
		Files: rt, Volumes: rt, Hold: g, QuarantinePeriod: time.Hour})
	require.NoError(t, err)
	rig := &restoreRig{ctx: ctx, st: st, rt: rt, r: r, held: g}
	rig.create(t, "login", v1.DeletionRetain)
	return rig
}

func (r *restoreRig) create(t *testing.T, name v1.ObjectName, deletion v1.DeletionPolicy) {
	t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.AddonObjectSpec{Kind: "IdentityProvider", Deletion: deletion}}
	_, err := r.st.Create(r.ctx, o)
	require.NoError(t, err)
}

func (r *restoreRig) pass(t *testing.T, name v1.ObjectName) controller.Result {
	t.Helper()
	res, err := r.r.Reconcile(r.ctx, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: name})
	require.NoError(t, err)
	return res
}

func (r *restoreRig) object(t *testing.T) *v1.AddonObject {
	t.Helper()
	obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "default", "login")
	require.NoError(t, err)
	return obj.(*v1.AddonObject)
}

// running brings login's engine up and Ready without restore.env.
func (r *restoreRig) running(t *testing.T) *fakeaddon.Engine {
	t.Helper()
	r.pass(t, "login")
	r.pass(t, "login")
	e := r.rt.Engine(loginRef())
	_, ready, _ := e.State()
	require.True(t, ready)
	return e
}

// restore stands for an in-place Restore's load: the Restore rewind, Running, its copy of items in restore and the
// object marked Loading.
func (r *restoreRig) restore(t *testing.T, items string) {
	t.Helper()
	rs := &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: "rewind", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "key"},
			Members: []v1.RestoreMember{{Kind: "IdentityProvider", Name: "login", InPlace: true}}}}
	created, err := r.st.Create(r.ctx, rs)
	require.NoError(t, err)
	created.(*v1.Restore).Status.Phase = v1.RunRunning
	_, err = r.st.Update(r.ctx, created)
	require.NoError(t, err)
	require.NoError(t, r.rt.Put(r.ctx, "default", "login", "restore", "backup_1.json", strings.NewReader(items), os.Geteuid(), os.Getegid()))
	o := r.object(t)
	o.Status.Restore = &v1.AddonRestoreState{Restore: "rewind", File: "backup_1.json", State: v1.AddonRestoreLoading}
	_, err = r.st.Update(r.ctx, o)
	require.NoError(t, err)
}

// data writes an engine's data into its storage, which a quarantine entry then holds.
func (r *restoreRig) data(t *testing.T, ref provider.ProviderRef) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(r.rt.Dir(ref), "data.json"), []byte(`{"client-a":"old"}`), 0o600))
}

func (r *restoreRig) endRestore(t *testing.T, phase v1.RunPhase) {
	t.Helper()
	obj, err := r.st.Get(r.ctx, v1.KindRestore.GVK(), "default", "rewind")
	require.NoError(t, err)
	obj.(*v1.Restore).Status.Phase = phase
	_, err = r.st.Update(r.ctx, obj)
	require.NoError(t, err)
}

func (r *restoreRig) copies(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(r.rt.Dir(loginRef()), "restore"))
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

const copyEnv = "file:/app/data/restore/backup_1.json"

// ADR-0333 Decision 4: while Loading, the engine starts with restore.env; the copy goes only once that engine is
// Ready, Loaded only once the copy is gone; then the engine starts without restore.env, and the mark goes once that
// one is Ready.
func TestRestoreMovesTheMarkOnAfterEachReadyEngine(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	e := r.running(t)
	r.restore(t, `{"client-a":"before"}`)

	r.pass(t, "login")
	running, ready, env := e.State()
	require.True(t, running)
	require.False(t, ready, "the engine that loads the copy is starting")
	require.Equal(t, copyEnv, env[fakeaddon.RestoreEnv])
	require.Equal(t, []string{"backup_1.json"}, r.copies(t), "the copy stays until that engine is Ready")
	require.Equal(t, v1.AddonRestoreLoading, r.object(t).Status.Restore.State)

	res := r.pass(t, "login")
	require.True(t, res.Requeue, "Loaded starts the engine without restore.env at once")
	require.Empty(t, r.copies(t), "the copy goes once the engine with restore.env is Ready")
	require.Equal(t, v1.AddonRestoreLoaded, r.object(t).Status.Restore.State)
	require.Equal(t, map[string]string{"client-a": "before"}, e.Items(), "the engine loaded the copy")

	r.pass(t, "login")
	_, ready, env = e.State()
	require.False(t, ready)
	require.NotContains(t, env, fakeaddon.RestoreEnv, "Loaded starts the engine without restore.env")
	require.NotNil(t, r.object(t).Status.Restore, "the mark stays until that engine is Ready")

	r.pass(t, "login")
	require.Nil(t, r.object(t).Status.Restore, "the mark goes once the engine without restore.env is Ready")
	require.Len(t, e.Starts(), 3, "two restarts: with restore.env, then without")
}

// "Loaded only once the copy is gone, whatever the engine does then": a Loading mark whose copy went turns Loaded, here
// after the write of Loaded was lost and the engine with restore.env crashed, which no longer starts without its copy.
func TestRestoreTurnsLoadedOnceTheCopyIsGone(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	e := r.running(t)
	r.restore(t, `{"client-a":"before"}`)
	r.pass(t, "login")
	r.pass(t, "login")
	require.Empty(t, r.copies(t))
	o := r.object(t)
	o.Status.Restore.State = v1.AddonRestoreLoading
	_, err := r.st.Update(r.ctx, o)
	require.NoError(t, err)
	e.Crash()

	r.pass(t, "login")
	require.Equal(t, v1.AddonRestoreLoaded, r.object(t).Status.Restore.State)
	running, _, env := e.State()
	require.True(t, running)
	require.NotContains(t, env, fakeaddon.RestoreEnv, "the next engine starts without restore.env")
	require.Equal(t, map[string]string{"client-a": "before"}, e.Items())
}

// ADR-0333 Decision 4: once the Restore that the mark names has failed, or is gone, the copy goes, then the mark, and the
// engine starts without restore.env.
func TestRestoreThatEndedIsCleared(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]func(*testing.T, *restoreRig){
		"failed": func(t *testing.T, r *restoreRig) { r.endRestore(t, v1.RunFailed) },
		"gone": func(t *testing.T, r *restoreRig) {
			require.NoError(t, r.st.Delete(r.ctx, v1.KindRestore.GVK(), "default", "rewind", ""))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRestoreRig(t)
			e := r.running(t)
			r.restore(t, `{}`)
			r.pass(t, "login")
			_, _, env := e.State()
			require.Equal(t, copyEnv, env[fakeaddon.RestoreEnv])

			end(t, r)
			r.pass(t, "login")
			require.Empty(t, r.copies(t))
			require.Nil(t, r.object(t).Status.Restore)
			_, _, env = e.State()
			require.NotContains(t, env, fakeaddon.RestoreEnv, "the engine starts without restore.env")
		})
	}
}

// A Restore's end reaches the objects whose mark names it.
func TestMapRestore(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	r.create(t, "other", "")
	r.running(t)
	r.restore(t, `{}`)
	rs := &v1.Restore{ObjectMeta: v1.ObjectMeta{Name: "rewind", Namespace: "default"}}
	require.Equal(t, []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: "default", Name: "login"}}, r.r.MapRestore(r.ctx, rs))
	require.Empty(t, r.r.MapRestore(r.ctx, &v1.Restore{ObjectMeta: v1.ObjectMeta{Name: "rewind", Namespace: "elsewhere"}}))
}

// ADR-0333 Decision 5: once the engine is gone, deletion delete removes the storage and retain moves it into the
// quarantine; neither while the platform is held.
func TestDeletionRemovesOrQuarantinesTheStorage(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		deletion    v1.DeletionPolicy
		held        bool
		kept, moved bool
	}{
		{deletion: v1.DeletionDelete},
		{deletion: v1.DeletionRetain, moved: true},
		{deletion: "", moved: true},
		{deletion: v1.DeletionDelete, held: true, kept: true},
		{deletion: v1.DeletionRetain, held: true, kept: true},
	} {
		r := newRestoreRig(t)
		obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "default", "login")
		require.NoError(t, err)
		obj.(*v1.AddonObject).Spec.Deletion = c.deletion
		_, err = r.st.Update(r.ctx, obj)
		require.NoError(t, err)
		r.running(t)
		r.data(t, loginRef())
		r.held.on = c.held
		require.NoError(t, r.st.Delete(r.ctx, v1.KindAddonObject.GVK(), "default", "login", ""))
		r.pass(t, "login")

		running, _, _ := r.rt.Engine(loginRef()).State()
		require.False(t, running, "%+v: the engine stops first", c)
		_, err = os.Stat(r.rt.Dir(loginRef()))
		require.Equal(t, c.kept, err == nil, "%+v: the storage stays", c)
		entries, err := r.r.Quarantine().List(r.ctx)
		require.NoError(t, err)
		require.Equal(t, c.moved, len(entries) == 1, "%+v: a quarantine entry", c)
	}
}

// ADR-0333 Decision 5: a start not held quarantines the storage of every add-on object no longer stored, whatever its
// deletion, and keeps a stored object's; held, it moves nothing.
func TestReclaimOrphansQuarantinesTheUnstored(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	r.create(t, "gone", v1.DeletionDelete)
	r.running(t)
	r.pass(t, "gone")
	gone := provider.ProviderRef{Namespace: "default", Name: "gone"}
	r.data(t, gone)
	require.NoError(t, r.st.Delete(r.ctx, v1.KindAddonObject.GVK(), "default", "gone", ""))

	r.held.on = true
	require.NoError(t, r.r.ReclaimOrphans(r.ctx, time.Now()))
	_, err := os.Stat(r.rt.Dir(gone))
	require.NoError(t, err, "held, nothing moves")

	r.held.on = false
	require.NoError(t, r.r.ReclaimOrphans(r.ctx, time.Now()))
	_, err = os.Stat(r.rt.Dir(gone))
	require.True(t, os.IsNotExist(err), "the unstored object's storage moved")
	_, err = os.Stat(r.rt.Dir(loginRef()))
	require.NoError(t, err, "a stored object's stays")
	entries, err := r.r.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, v1.ObjectRef{Kind: v1.KindAddonObject, Namespace: "default", Name: "gone"}, entries[0].ObjectRef,
		"quarantined whatever its deletion was")
}

// An object deleted and created again between two passes starts empty: the earlier one's storage goes first.
func TestAnObjectCreatedAgainStartsEmpty(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	e := r.running(t)
	r.data(t, loginRef())
	require.NoError(t, r.st.Delete(r.ctx, v1.KindAddonObject.GVK(), "default", "login", ""))
	r.create(t, "login", "")
	r.pass(t, "login")
	r.pass(t, "login")
	require.Empty(t, e.Items(), "the new object's engine starts on empty storage")
	entries, err := r.r.Quarantine().List(r.ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the earlier object's storage is quarantined")
}

// ADR-0332 Decision 3: an engine's address, its status.address: on loopback, as funcdctl dev publishes one, the address
// and port; elsewhere the whole address.
func TestEnginesTellAnEngineAddress(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	r.create(t, "bridged", "")
	for name, at := range map[v1.ObjectName]string{"login": "127.0.0.1:41000", "bridged": "10.63.0.7:8080"} {
		obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "default", name)
		require.NoError(t, err)
		obj.(*v1.AddonObject).Status.Address = at
		_, err = r.st.Update(r.ctx, obj)
		require.NoError(t, err)
	}
	engines := addon.Engines{Store: r.st}
	for addr, want := range map[string]bool{
		"127.0.0.1:41000": true, "127.0.0.1:9000": false, "10.63.0.7:8080": true, "10.63.0.7:22": true,
		"[::ffff:10.63.0.7]:22": true, "10.63.0.8:8080": false,
	} {
		got, err := engines.Engine(r.ctx, netip.MustParseAddrPort(addr))
		require.NoError(t, err)
		require.Equal(t, want, got, addr)
	}
}

// ADR-0333 Decision 1: restore.env's expressions read config and restore.file, checked when the Addon is applied.
func TestCheckRestoreEnv(t *testing.T) {
	t.Parallel()
	require.NoError(t, addon.CheckRestoreEnv(nil, nil))
	ok := &v1.AddonRestore{Env: map[string]string{"A": `${{ "file:" + restore.file }}`, "B": "${{ config.issuer }}", "C": "x"}}
	require.NoError(t, addon.CheckRestoreEnv(ok, []byte(schema)))
	bad := &v1.AddonRestore{Env: map[string]string{"A": "${{ restore.path }}"}}
	require.Equal(t, fault.Invalid, fault.KindOf(addon.CheckRestoreEnv(bad, []byte(schema))), "restore holds file only")
}

// ADR-0333 Decision 4: the copy counts as gone only once restore.path holds no file, whatever its time says, so a copy
// dated ahead of the clock keeps the mark until an engine loaded it.
func TestRestoreKeepsTheMarkForACopyDatedAhead(t *testing.T) {
	t.Parallel()
	r := newRestoreRig(t)
	r.running(t)
	r.restore(t, `{"client-a":"before"}`)
	ahead := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(r.rt.Dir(loginRef()), "restore", "backup_1.json"), ahead, ahead))
	r.pass(t, "login")
	require.NotNil(t, r.object(t).Status.Restore, "the mark stays while the copy is there")
	require.NotEmpty(t, r.copies(t), "and so does the copy")
}

// TestIssue1511_ADeletionAfterARestartFollowsTheObjectsPolicy: an object deleted after funcd restarts, before the
// restarted reconciler's first pass over it, follows its deletion policy, held at the start or not, since the start
// records every stored object's policy (ADR-0333 Decision 5, scenario addon-deletion).
func TestIssue1511_ADeletionAfterARestartFollowsTheObjectsPolicy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		deletion    v1.DeletionPolicy
		heldAtStart bool
		moved       bool
	}{
		{deletion: v1.DeletionDelete},
		{deletion: v1.DeletionDelete, heldAtStart: true},
		{deletion: v1.DeletionRetain, moved: true},
	} {
		r := newRestoreRig(t)
		obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "default", "login")
		require.NoError(t, err)
		obj.(*v1.AddonObject).Spec.Deletion = c.deletion
		_, err = r.st.Update(r.ctx, obj)
		require.NoError(t, err)
		r.running(t)
		r.data(t, loginRef())

		g := &heldGate{Gate: hold.Never, on: c.heldAtStart}
		restarted, err := addon.NewReconciler(addon.Deps{Store: r.st, Provider: r.rt, RunsImages: true,
			Files: r.rt, Volumes: r.rt, Hold: g, QuarantinePeriod: time.Hour})
		require.NoError(t, err)
		r.r = restarted
		require.NoError(t, r.r.ReclaimOrphans(r.ctx, time.Now()), "%+v: the start", c)
		g.on = false
		require.NoError(t, r.st.Delete(r.ctx, v1.KindAddonObject.GVK(), "default", "login", ""))
		r.pass(t, "login")

		_, err = os.Stat(r.rt.Dir(loginRef()))
		require.Error(t, err, "%+v: the storage left its place", c)
		entries, err := r.r.Quarantine().List(r.ctx)
		require.NoError(t, err)
		require.Equal(t, c.moved, len(entries) == 1, "%+v: a quarantine entry", c)
	}
}
