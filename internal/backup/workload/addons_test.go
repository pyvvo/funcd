package workload_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// addonRig is a store with rauthy's Addon, whose engines write their copies under state_machine/backups and load one
// from restore, the IdentityProvider login of orders in group app, and its engine's files under a storage root.
type addonRig struct {
	ctx   context.Context
	st    store.Store
	files provider.EngineFiles
	dir   string // login's storage directory
}

func newAddonRig(t *testing.T, mutate func(*v1.AddonSpec)) *addonRig {
	t.Helper()
	ctx := context.Background()
	st := store.New(storemem.New())
	a := &v1.Addon{TypeMeta: tm(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "rauthy"}, Spec: v1.AddonSpec{
		Kind: "IdentityProvider", Image: testImage, Port: 8080, Readiness: v1.AddonReadiness{Path: "/ping"},
		Storage:  &v1.AddonStorage{Path: "/app/data", UID: os.Geteuid(), GID: os.Getegid()},
		Snapshot: &v1.AddonSnapshot{Path: "/app/data/state_machine/backups"},
		Restore:  &v1.AddonRestore{Path: "/app/data/restore", Timeout: v1.MinAddonRestoreTimeout},
	}}
	if mutate != nil {
		mutate(&a.Spec)
	}
	for _, o := range []v1.Object{a, idp("login", "app")} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "addonobject", "orders", "login")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state_machine", "backups"), 0o700))
	return &addonRig{ctx: ctx, st: st, files: provider.NewEngineFiles(root, v1.KindAddonObject), dir: dir}
}

// testImage is an engine image pinned by digest.
const testImage = "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809"

func idp(name, group string) *v1.AddonObject {
	return &v1.AddonObject{TypeMeta: tm(v1.KindAddonObject), ObjectMeta: meta(name, group), Spec: v1.AddonObjectSpec{Kind: "IdentityProvider"}}
}

// snapshot writes a snapshot of login's engine, last changed age ago.
func (r *addonRig) snapshot(t *testing.T, name string, data []byte, age time.Duration) {
	t.Helper()
	p := filepath.Join(r.dir, "state_machine", "backups", name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	at := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(p, at, at))
}

func (r *addonRig) object(t *testing.T) *v1.AddonObject {
	t.Helper()
	obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "orders", "login")
	require.NoError(t, err)
	return obj.(*v1.AddonObject)
}

// write copies the members with the Addons kind to an S3 stub, unsealed, and returns the manifest and the keys put.
func (r *addonRig) write(t *testing.T, ms ...workload.Member) (workload.Manifest, []string, *s3stub.Stub, error) {
	t.Helper()
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(r.ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	const path = "orders/b1-uid/"
	m, err := workload.Write(r.ctx, "orders", ms, []workload.MemberKind{workload.Addons(r.st, r.files)},
		workload.Options{Target: target, Path: path})
	return m, s.PutKeys(path), s, err
}

// login is the member IdentityProvider/login.
func login() workload.Member { return workload.Member{Kind: "IdentityProvider", Name: "login"} }

// scenario: addon-backup-member — a Backup copies the newest quiet snapshot, unopened, as member IdentityProvider/login
// at addon/IdentityProvider/login/<file>/, in records of 8 MiB.
func TestAddons_WriteCopiesTheNewestQuietSnapshotUnopened(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	newest := bytes.Repeat([]byte("s"), 8<<20+3)
	r.snapshot(t, "backup_1.sqlite", []byte("older"), time.Minute)
	r.snapshot(t, "backup_2.sqlite", newest, 30*time.Second)
	r.snapshot(t, "backup_3.sqlite", []byte("just named"), 0)
	m, keys, s, err := r.write(t, login())
	require.NoError(t, err)
	require.Len(t, m.Members, 1)
	require.Equal(t, v1.Kind("IdentityProvider"), m.Members[0].Kind)
	require.Equal(t, "addon/IdentityProvider/login/backup_2.sqlite", m.Members[0].Files[0].Name)
	require.Contains(t, keys, "orders/b1-uid/addon/IdentityProvider/login/backup_2.sqlite/part-00000")

	target, err := gocloud.OpenWith(r.ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	next := workload.Records(r.ctx, target, "orders/b1-uid/", m.Members[0].Files[0], nil)
	var got []byte
	var recs []string
	for {
		rec, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		recs = append(recs, string(rec.Key))
		got = append(got, rec.Value...)
	}
	require.Equal(t, []string{"000001", "000002"}, recs, "a record holds 8 MiB")
	require.True(t, bytes.Equal(newest, got), "the snapshot is copied as it is")
}

// scenario: addon-backup-no-snapshot — no quiet snapshot, an Addon that declares no snapshot, or no Addon of the kind
// fails the member NoSnapshot, before anything is written.
func TestAddons_WriteFailsNoSnapshot(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	r.snapshot(t, "backup_1.sqlite", []byte("just named"), 0)
	_, keys, _, err := r.write(t, login())
	require.ErrorIs(t, err, workload.ErrNoSnapshot)
	require.ErrorContains(t, err, "has written no snapshot under /app/data/state_machine/backups yet")
	require.Empty(t, keys)

	r = newAddonRig(t, func(s *v1.AddonSpec) { s.Snapshot = nil })
	_, _, _, err = r.write(t, login())
	require.ErrorIs(t, err, workload.ErrNoSnapshot)
	require.ErrorContains(t, err, "the add-on rauthy declares no snapshot.path")

	require.NoError(t, r.st.Delete(r.ctx, v1.KindAddon.GVK(), "", "rauthy", ""))
	_, _, _, err = r.write(t, login())
	require.ErrorIs(t, err, workload.ErrNoSnapshot)
	require.ErrorContains(t, err, "no Addon registers the kind IdentityProvider")

	_, _, _, err = r.write(t, workload.Member{Kind: "EchoService", Name: "login"})
	require.Equal(t, fault.NotFound, fault.KindOf(err), "login is no EchoService")
}

// One member kind serves every add-on's kind, and only the kinds funcd does not define (ADR-0333 Decision 2).
func TestServes(t *testing.T) {
	t.Parallel()
	addons := workload.Addons(nil, nil)
	require.Equal(t, v1.KindAddonObject, addons.Kind())
	require.True(t, workload.Serves(addons, "IdentityProvider"))
	require.True(t, workload.Serves(addons, v1.KindAddonObject))
	require.False(t, workload.Serves(addons, v1.KindKVStore), "a kind funcd defines")
	dbs := workload.Databases(nil, nil)
	require.True(t, workload.Serves(dbs, v1.KindSQLDatabase))
	require.False(t, workload.Serves(dbs, "IdentityProvider"))
}

// scenario: addon-backup-group-scope — a ResourceGroup takes its add-on objects whose Addon declares a snapshot, under
// their kinds and in name order; a named object resolves whatever its Addon declares, and a missing one is NotFound.
func TestResolve_AddonMembers(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	echo := &v1.Addon{TypeMeta: tm(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "whoami"}, Spec: v1.AddonSpec{Kind: "EchoService",
		Image: testImage, Port: 80, Readiness: v1.AddonReadiness{Path: "/"}}}
	for _, o := range []v1.Object{echo, idp("alpha", "app"), idp("other", "elsewhere"),
		&v1.AddonObject{TypeMeta: tm(v1.KindAddonObject), ObjectMeta: meta("echo", "app"), Spec: v1.AddonObjectSpec{Kind: "EchoService"}},
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("orders-state", "app")}} {
		_, err := r.st.Create(r.ctx, o)
		require.NoError(t, err)
	}
	ms, err := workload.Resolve(r.ctx, r.st, "orders", v1.BackupScope{ResourceGroup: "app"}, workload.Limits{}.OrDefaults())
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}, {Kind: "IdentityProvider", Name: "alpha"},
		{Kind: "IdentityProvider", Name: "login"}}, ms, "echo's Addon declares no snapshot")

	ms, err = workload.Resolve(r.ctx, r.st, "orders", v1.BackupScope{Addons: []v1.ObjectName{"echo", "login"}}, workload.Limits{}.OrDefaults())
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: "EchoService", Name: "echo"}, login()}, ms)

	_, err = workload.Resolve(r.ctx, r.st, "orders", v1.BackupScope{Addons: []v1.ObjectName{"gone"}}, workload.Limits{}.OrDefaults())
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.ErrorContains(t, err, "member add-on object gone")
}

// scenario: addon-app-backup — scope.app copies the App's add-on parts beside its stores, those whose Addon declares a
// snapshot (ADR-0337 Decision 7).
func TestScenarioAddonAppBackup(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	echo := &v1.Addon{TypeMeta: tm(v1.KindAddon), ObjectMeta: v1.ObjectMeta{Name: "whoami"}, Spec: v1.AddonSpec{Kind: "EchoService",
		Image: testImage, Port: 80, Readiness: v1.AddonReadiness{Path: "/"}}}
	app := &v1.App{TypeMeta: tm(v1.KindApp), ObjectMeta: meta("web-auth", "web-auth")}
	app.Spec.KV = []v1.KVStoreEntry{{Name: "web-auth-state"}}
	app.Spec.Addons = []v1.AppAddon{{Ref: "login"}, {Name: "web-auth-echo", AddonObjectSpec: v1.AddonObjectSpec{Kind: "EchoService"}}}
	for _, o := range []v1.Object{echo, app,
		&v1.AddonObject{TypeMeta: tm(v1.KindAddonObject), ObjectMeta: meta("web-auth-echo", "web-auth"), Spec: v1.AddonObjectSpec{Kind: "EchoService"}},
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("web-auth-state", "web-auth")}} {
		_, err := r.st.Create(r.ctx, o)
		require.NoError(t, err)
	}
	ms, err := workload.Resolve(r.ctx, r.st, "orders", v1.BackupScope{App: "web-auth"}, workload.Limits{}.OrDefaults())
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: v1.KindKVStore, Name: "web-auth-state"}, login()}, ms,
		"the App's IdentityProvider is copied; its EchoService, whose Addon declares no snapshot, is not")
}

// file is a member file of the copy of login whose name is the copy's.
func memberFile(name string) workload.MemberFile {
	return workload.MemberFile{Kind: "IdentityProvider", Name: "login",
		Files: []backup.StoreFile{{Name: "addon/IdentityProvider/login/" + name}}}
}

// opener reads data as one record, whatever the file.
func opener(data string) workload.RecordOpener {
	return func(context.Context, backup.StoreFile) func() (snapshot.Record, error) {
		done := false
		return func() (snapshot.Record, error) {
			if done {
				return snapshot.Record{}, io.EOF
			}
			done = true
			return snapshot.Record{Key: []byte("000001"), Value: []byte(data)}, nil
		}
	}
}

func (r *addonRig) destination(t *testing.T, rm v1.RestoreMember) workload.Destination {
	t.Helper()
	d, err := workload.Addons(r.st, r.files).Destination(workload.WithRestore(r.ctx, "rewind"), "orders", rm)
	require.NoError(t, err)
	return d
}

// inPlace restores login in place.
func inPlace() v1.RestoreMember {
	return v1.RestoreMember{Kind: "IdentityProvider", Name: "login", InPlace: true}
}

// ADR-0333 Decision 3: the load empties restore.path, writes the copy there, marks the object Loading with the
// Restore and the copy's file, and waits until the mark is gone, which the reconciler does once the engine is back.
func TestAddons_LoadPlacesTheCopyAndWaitsForTheMarkToGo(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, func(s *v1.AddonSpec) { s.Restore.Timeout = v1.MaxAddonRestoreTimeout })
	require.NoError(t, os.MkdirAll(filepath.Join(r.dir, "restore"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "restore", "stale"), []byte("x"), 0o600))
	d := r.destination(t, inPlace())
	done := make(chan error, 1)
	go func() { done <- d.Load(r.ctx, memberFile("backup_2.sqlite"), opener("the copy")) }()

	require.Eventually(t, func() bool { return r.object(t).Status.Restore != nil }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, &v1.AddonRestoreState{Restore: "rewind", File: "backup_2.sqlite", State: v1.AddonRestoreLoading}, r.object(t).Status.Restore)
	ents, err := os.ReadDir(filepath.Join(r.dir, "restore"))
	require.NoError(t, err)
	require.Len(t, ents, 1, "the load emptied restore.path first")
	b, err := os.ReadFile(filepath.Join(r.dir, "restore", "backup_2.sqlite"))
	require.NoError(t, err)
	require.Equal(t, "the copy", string(b))
	select {
	case err := <-done:
		t.Fatalf("the load ended while the mark stands: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	o := r.object(t)
	o.Status.Restore.State = v1.AddonRestoreLoaded
	_, err = r.st.Update(r.ctx, o)
	require.NoError(t, err)
	select {
	case err := <-done:
		t.Fatalf("the load ended at Loaded, before the engine without restore.env is Ready: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	o = r.object(t)
	o.Status.Restore = nil
	_, err = r.st.Update(r.ctx, o)
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the load did not end once the mark went")
	}
}

// scenario: addon-restore-timeout — past restore.timeout the load fails ErrRestoreTimedOut, and its clear removes the
// copy, then the mark.
func TestAddons_LoadTimesOutAndItsClearRemovesTheCopyThenTheMark(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	d := r.destination(t, inPlace())
	start := time.Now()
	err := d.Load(r.ctx, memberFile("backup_2.sqlite"), opener("the copy"))
	require.ErrorIs(t, err, workload.ErrRestoreTimedOut)
	require.GreaterOrEqual(t, time.Since(start), time.Duration(v1.MinAddonRestoreTimeout))
	require.NotNil(t, r.object(t).Status.Restore)

	require.NoError(t, d.Clear(r.ctx))
	require.Nil(t, r.object(t).Status.Restore)
	ents, err := os.ReadDir(filepath.Join(r.dir, "restore"))
	require.NoError(t, err)
	require.Empty(t, ents)
}

// A load whose object is deleted fails, and a Restore's own clear leaves another Restore's mark.
func TestAddons_LoadFailsOnADeletedObject(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	d := r.destination(t, inPlace())
	done := make(chan error, 1)
	go func() { done <- d.Load(r.ctx, memberFile("backup_2.sqlite"), opener("the copy")) }()
	require.Eventually(t, func() bool { return r.object(t).Status.Restore != nil }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, r.st.Delete(r.ctx, v1.KindAddonObject.GVK(), "orders", "login", ""))
	err := <-done
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "was deleted during the restore")
	require.NoError(t, d.Clear(r.ctx), "no mark of the Restore stands on a deleted object")

	r = newAddonRig(t, nil)
	o := r.object(t)
	o.Status.Restore = &v1.AddonRestoreState{Restore: "other", File: "x", State: v1.AddonRestoreLoading}
	_, err = r.st.Update(r.ctx, o)
	require.NoError(t, err)
	require.NoError(t, r.destination(t, inPlace()).Clear(r.ctx))
	require.Equal(t, v1.ObjectName("other"), r.object(t).Status.Restore.Restore)
}

// scenario: addon-restore-in-place-only — an add-on object's destination is in place only; a name of another kind is
// MemberNotFound's NotFound, and an Addon that declares no restore refuses it.
func TestAddons_DestinationRules(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, nil)
	kind := workload.Addons(r.st, r.files)
	_, err := kind.Destination(r.ctx, "orders", v1.RestoreMember{Kind: "IdentityProvider", Name: "login", As: "other"})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "restores in place only")
	_, err = kind.Destination(r.ctx, "orders", v1.RestoreMember{Kind: "EchoService", Name: "login", InPlace: true})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, err = kind.Destination(r.ctx, "orders", v1.RestoreMember{Kind: "IdentityProvider", Name: "gone", InPlace: true})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, err = workload.Addons(r.st, nil).Destination(r.ctx, "orders", inPlace())
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a runtime that reaches no engine's files")
	_, err = kind.Destination(r.ctx, "orders", inPlace())
	require.Equal(t, fault.Internal, fault.KindOf(err), "a load that no Restore names would leave a mark no Restore ends")

	r = newAddonRig(t, func(s *v1.AddonSpec) { s.Restore = nil })
	_, err = workload.Addons(r.st, r.files).Destination(r.ctx, "orders", inPlace())
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "declares no restore.path")

	d := newAddonRig(t, nil).destination(t, inPlace())
	err = d.Load(r.ctx, workload.MemberFile{Kind: "IdentityProvider", Name: "login"}, opener(""))
	require.ErrorIs(t, err, workload.ErrFileCount)
	err = d.Load(r.ctx, memberFile(".."), opener(""))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.True(t, strings.Contains(err.Error(), "not one path segment"), "%v", err)
}

// ADR-0332 Decision 1: an object's Addon is the one of its kind's plural, so an Addon that registers the kind again in
// another case keeps the object a member of a group Backup and in place for a Restore.
func TestAddons_AnAddonOfTheSamePluralKeepsItsObjects(t *testing.T) {
	t.Parallel()
	r := newAddonRig(t, func(s *v1.AddonSpec) { s.Kind = "Identityprovider" })
	ms, err := workload.Resolve(r.ctx, r.st, "orders", v1.BackupScope{ResourceGroup: "app"}, workload.Limits{}.OrDefaults())
	require.NoError(t, err)
	require.Contains(t, ms, workload.Member{Kind: "IdentityProvider", Name: "login"}, "the group scope takes the object")
	_, err = workload.Addons(r.st, r.files).Destination(workload.WithRestore(r.ctx, "rewind"), "orders",
		v1.RestoreMember{Kind: "IdentityProvider", Name: "login", InPlace: true})
	require.NoError(t, err, "an in-place Restore finds its Addon")

	// A write under the new Addon stores its kind's case; a Backup taken before still names the old one.
	obj, err := r.st.Get(r.ctx, v1.KindAddonObject.GVK(), "orders", "login")
	require.NoError(t, err)
	stored := obj.(*v1.AddonObject)
	stored.Spec.Kind = "Identityprovider"
	_, err = r.st.Update(r.ctx, stored)
	require.NoError(t, err)
	_, err = workload.Addons(r.st, r.files).Destination(workload.WithRestore(r.ctx, "rewind"), "orders",
		v1.RestoreMember{Kind: "IdentityProvider", Name: "login", InPlace: true})
	require.NoError(t, err, "a Backup taken before the object was written again restores in place")
}
