package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow"
)

const conflictType = "urn:funcd:problem:conflict"

// newConcurrencyServer is the API over a store on eng with the real RBAC, the link and ResourceGroup admissions
// (so a Function delete runs a Delete admission and a ConfigMap delete does not) and the owner collector.
func newConcurrencyServer(t *testing.T, eng store.Engine) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(eng)
	return concurrencyServerOn(t, st), st
}

// concurrencyServerOn is newConcurrencyServer over st.
func concurrencyServerOn(t *testing.T, st store.Store) http.Handler {
	t.Helper()
	r := raceReader{st}
	col, err := gc.New(gc.Deps{Store: st, Purger: noPurge{}})
	require.NoError(t, err)
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(), Collector: col,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{groupNS}},
		}),
		Admissions: []admission.Admission{
			admission.NewLinkValidityAdmission(r),
			admission.NewLinkDeletionProtectionAdmission(r),
			admission.NewResourceGroupDeletionProtectionAdmission(r),
		},
	})
	require.NoError(t, err)
	return h
}

// send is one request with the dev token and the given If-Match lines.
func send(t *testing.T, srv http.Handler, method, path string, body []byte, ifMatch ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+devToken)
	for _, v := range ifMatch {
		r.Header.Add("If-Match", v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func fnPath(name string) string { return kindPath(groupNS, "functions") + "/" + name }

// fnBody is Function name in the team group with handler and, when rv is set, metadata.resourceVersion.
func fnBody(t *testing.T, name, handler, rv string) []byte {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup, fn.ResourceVersion = v1.ObjectName(name), groupNS, "team", rv
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", handler, "oci://example/app:v1"
	b, err := json.Marshal(fn)
	require.NoError(t, err)
	return b
}

// stored returns the stored Function's resourceVersion and handler.
func stored(t *testing.T, st store.Store, name string) (string, string) {
	t.Helper()
	o, err := st.Get(context.Background(), v1.KindFunction.GVK(), groupNS, v1.ObjectName(name))
	require.NoError(t, err)
	return o.GetObjectMeta().ResourceVersion, o.(*v1.Function).Spec.Handler
}

// twoVersions creates group team and Function f, then replaces f once: it returns f's first and current version.
func twoVersions(t *testing.T, srv http.Handler, st store.Store) (v1RV, v2RV string) {
	t.Helper()
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), fnBody(t, "f", "h1", "")})
	v1RV, _ = stored(t, st, "f")
	mustDo(t, srv, call{http.MethodPut, fnPath("f"), fnBody(t, "f", "h2", "")})
	v2RV, _ = stored(t, st, "f")
	require.NotEqual(t, v1RV, v2RV)
	return v1RV, v2RV
}

// appTwoVersions creates App todo through the generic CRUD route, then replaces it once: it returns the server,
// the App's first and its current version.
func appTwoVersions(t *testing.T) (srv http.Handler, v1RV, v2RV string) {
	t.Helper()
	srv = newServer(t)
	rvOf := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var app v1.App
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &app))
		return app.ResourceVersion
	}
	v1RV = rvOf(send(t, srv, http.MethodPost, appBase, appBody(t, "todo", nil)))
	v2RV = rvOf(send(t, srv, http.MethodPut, appBase+"/todo", appBody(t, "todo", func(s *v1.AppSpec) { s.Version = "2" })))
	require.NotEqual(t, v1RV, v2RV)
	return srv, v1RV, v2RV
}

// appBodyAt is App todo with spec.version version and metadata.resourceVersion rv.
func appBodyAt(t *testing.T, version, rv string) []byte {
	t.Helper()
	var app v1.App
	require.NoError(t, json.Unmarshal(appBody(t, "todo", func(s *v1.AppSpec) { s.Version = version }), &app))
	app.ResourceVersion = rv
	b, err := json.Marshal(app)
	require.NoError(t, err)
	return b
}

// requireAppAt asserts the stored App todo still has version rv and spec.version version.
func requireAppAt(t *testing.T, srv http.Handler, rv, version string) {
	t.Helper()
	rec := send(t, srv, http.MethodGet, appBase+"/todo", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var app v1.App
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &app))
	require.Equal(t, rv, app.ResourceVersion)
	require.Equal(t, version, app.Spec.Version)
}

func requireConflict(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	typ, detail := problemOf(t, rec)
	require.Equal(t, conflictType, typ)
	return detail
}

// scenario: stale-replace-conflicts
func TestScenarioStaleReplaceConflicts(t *testing.T) {
	srv, st := newConcurrencyServer(t, memory.New())
	old, cur := twoVersions(t, srv, st)

	detail := requireConflict(t, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", old)))
	require.Contains(t, detail, fmt.Sprintf("Function %s/f", groupNS))
	require.Contains(t, detail, "re-read")
	requireConflict(t, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", ""), `"`+old+`"`))

	rv, handler := stored(t, st, "f")
	require.Equal(t, cur, rv)
	require.Equal(t, "h2", handler)

	app, oldApp, curApp := appTwoVersions(t)
	requireConflict(t, send(t, app, http.MethodPut, appBase+"/todo", appBodyAt(t, "3", oldApp)))
	requireConflict(t, send(t, app, http.MethodPut, appBase+"/todo", appBodyAt(t, "3", ""), `"`+oldApp+`"`))
	requireAppAt(t, app, curApp, "2")
}

// scenario: current-replace-succeeds
func TestScenarioCurrentReplaceSucceeds(t *testing.T) {
	srv, st := newConcurrencyServer(t, memory.New())
	_, cur := twoVersions(t, srv, st)

	for i, form := range []struct {
		body   bool
		header bool
	}{{body: true}, {header: true}, {body: true, header: true}} {
		handler := fmt.Sprintf("h%d", i+3)
		var bodyRV string
		var lines []string
		if form.body {
			bodyRV = cur
		}
		if form.header {
			lines = append(lines, `"`+cur+`"`)
		}
		rec := send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", handler, bodyRV), lines...)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got v1.Function
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, handler, got.Spec.Handler)
		require.NotEqual(t, cur, got.ResourceVersion)
		rv, h := stored(t, st, "f")
		require.Equal(t, got.ResourceVersion, rv)
		require.Equal(t, handler, h)
		cur = rv
	}
}

// scenario: unversioned-writes-stay-unconditional
func TestScenarioUnversionedWritesStayUnconditional(t *testing.T) {
	srv, st := newConcurrencyServer(t, memory.New())
	twoVersions(t, srv, st)

	require.Equal(t, http.StatusOK, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", "")).Code)
	require.Equal(t, http.StatusOK, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h4", ""), "*").Code)
	_, handler := stored(t, st, "f")
	require.Equal(t, "h4", handler)

	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), fnBody(t, "g", "h1", "")})
	require.Equal(t, http.StatusNoContent, send(t, srv, http.MethodDelete, fnPath("f"), nil, "*").Code)
	require.Equal(t, http.StatusNoContent, send(t, srv, http.MethodDelete, fnPath("g"), nil).Code)
	require.False(t, present(t, st, v1.KindFunction, groupNS, "f"))
	require.False(t, present(t, st, v1.KindFunction, groupNS, "g"))
}

// scenario: bad-precondition-rejected
func TestScenarioBadPreconditionRejected(t *testing.T) {
	srv, st := newConcurrencyServer(t, memory.New())
	old, cur := twoVersions(t, srv, st)
	quoted := func(v string) string { return `"` + v + `"` }

	rec := send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", cur), quoted(old))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, "differ")

	for name, lines := range map[string][]string{
		"weak":      {"W/" + quoted(cur)},
		"list":      {quoted(old) + ", " + quoted(cur)},
		"unquoted":  {cur},
		"empty":     {""},
		"two lines": {quoted(cur), quoted(old)},
	} {
		put := send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", ""), lines...)
		require.Equal(t, http.StatusBadRequest, put.Code, "PUT %s: %s", name, put.Body.String())
		del := send(t, srv, http.MethodDelete, fnPath("f"), nil, lines...)
		require.Equal(t, http.StatusBadRequest, del.Code, "DELETE %s: %s", name, del.Body.String())
	}
	rv, handler := stored(t, st, "f")
	require.Equal(t, cur, rv)
	require.Equal(t, "h2", handler)

	app, oldApp, curApp := appTwoVersions(t)
	rec = send(t, app, http.MethodPut, appBase+"/todo", appBodyAt(t, "3", curApp), quoted(oldApp))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = send(t, app, http.MethodPut, appBase+"/todo", appBodyAt(t, "3", ""), "W/"+quoted(curApp))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	requireAppAt(t, app, curApp, "2")
}

// scenario: stale-delete-conflicts — a Function (compared at the Delete admission's Get) and a ConfigMap (compared
// by store.Delete) answer the same 409.
func TestScenarioStaleDeleteConflicts(t *testing.T) {
	srv, st := newConcurrencyServer(t, memory.New())
	old, cur := twoVersions(t, srv, st)

	requireConflict(t, send(t, srv, http.MethodDelete, fnPath("f"), nil, `"`+old+`"`))
	require.True(t, present(t, st, v1.KindFunction, groupNS, "f"))
	require.Equal(t, http.StatusNoContent, send(t, srv, http.MethodDelete, fnPath("f"), nil, `"`+cur+`"`).Code)
	require.False(t, present(t, st, v1.KindFunction, groupNS, "f"))

	cmPath := kindPath(groupNS, "configmaps") + "/c"
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, "c", "team")})
	o, err := st.Get(context.Background(), v1.KindConfigMap.GVK(), groupNS, "c")
	require.NoError(t, err)
	cmRV := o.GetObjectMeta().ResourceVersion
	requireConflict(t, send(t, srv, http.MethodDelete, cmPath, nil, `"`+cmRV+`0"`))
	require.True(t, present(t, st, v1.KindConfigMap, groupNS, "c"))
	require.Equal(t, http.StatusNoContent, send(t, srv, http.MethodDelete, cmPath, nil, `"`+cmRV+`"`).Code)
	require.False(t, present(t, st, v1.KindConfigMap, groupNS, "c"))
}

// scenario: stale-kvstore-replace-conflicts — the version is compared before the live-Workflow guard.
func TestScenarioStaleKVStoreReplaceConflicts(t *testing.T) {
	ctx := context.Background()
	srv, st := newKVServer(t)
	wf := kvWorkflow(t, st, "w-kv", v1.DeletionRetain)
	kept := keptStore(t, st, "w-kv", markerOf("w", wf.UID))
	body, err := json.Marshal(kept)
	require.NoError(t, err)

	newer := *kept
	newer.Status.Phase = v1.PhaseReady
	_, err = st.Update(ctx, &newer)
	require.NoError(t, err)
	cur := storeRV(t, st, "w-kv")

	path := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/kvstores/w-kv", kvNS)
	rec := do(t, srv, http.MethodPut, path, "operator-token", body)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	typ, detail := problemOf(t, rec)
	require.Equal(t, conflictType, typ)
	require.Contains(t, detail, "re-read")
	require.NotContains(t, detail, "managed by")
	require.Equal(t, cur, storeRV(t, st, "w-kv"))
}

// scenario: forced-group-delete-honors-version
func TestScenarioForcedGroupDeleteHonorsVersion(t *testing.T) {
	ctx := context.Background()
	srv, st := newConcurrencyServer(t, memory.New())
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), fnBody(t, "a", "h1", "")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, "c", "team")})
	g, err := st.Get(ctx, v1.KindResourceGroup.GVK(), groupNS, "team")
	require.NoError(t, err)
	old := g.GetObjectMeta().ResourceVersion
	g.(*v1.ResourceGroup).Status.Phase = v1.PhaseReady
	g, err = st.Update(ctx, g)
	require.NoError(t, err)
	cur := g.GetObjectMeta().ResourceVersion

	requireConflict(t, send(t, srv, http.MethodDelete, groupPath(groupNS, "team", true), nil, `"`+old+`"`))
	require.True(t, present(t, st, v1.KindResourceGroup, groupNS, "team"))
	require.True(t, present(t, st, v1.KindFunction, groupNS, "a"))
	require.True(t, present(t, st, v1.KindConfigMap, groupNS, "c"))

	rec := send(t, srv, http.MethodDelete, groupPath(groupNS, "team", true), nil, `"`+cur+`"`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.False(t, present(t, st, v1.KindResourceGroup, groupNS, "team"))
	require.False(t, present(t, st, v1.KindFunction, groupNS, "a"))
}

// backupEngine is a memory engine that records the buckets written, so a test can copy every key at one moment:
// the backup a restore loads (ADR-0206), taken without ADR-0202's Snapshot.
type backupEngine struct {
	store.Engine
	mu      sync.Mutex
	buckets map[string]bool
}

type bucketTxn struct {
	store.Txn
	e *backupEngine
}

func (tx bucketTxn) Put(bucket, key string, val []byte) error {
	tx.e.mu.Lock()
	tx.e.buckets[bucket] = true
	tx.e.mu.Unlock()
	return tx.Txn.Put(bucket, key, val)
}

func (e *backupEngine) Update(ctx context.Context, fn func(store.Txn) error) error {
	return e.Engine.Update(ctx, func(tx store.Txn) error { return fn(bucketTxn{tx, e}) })
}

func (e *backupEngine) backup(t *testing.T) store.Engine {
	t.Helper()
	ctx := context.Background()
	e.mu.Lock()
	defer e.mu.Unlock()
	dst := memory.New()
	require.NoError(t, e.View(ctx, func(src store.Txn) error {
		return dst.Update(ctx, func(out store.Txn) error {
			for bucket := range e.buckets {
				if err := src.Scan(bucket, func(key string, val []byte) error {
					return out.Put(bucket, key, bytes.Clone(val))
				}); err != nil {
					return err
				}
			}
			return nil
		})
	}))
	return dst
}

// scenario: pre-restore-version-conflicts — B is restored from A's backup; a client holding the version A wrote
// after that backup gets 409 on B, by plain string inequality.
func TestScenarioPreRestoreVersionConflicts(t *testing.T) {
	a := &backupEngine{Engine: memory.New(), buckets: map[string]bool{}}
	srvA, stA := newConcurrencyServer(t, a)
	_, atBackup := twoVersions(t, srvA, stA)
	backup := a.backup(t)

	mustDo(t, srvA, call{http.MethodPut, fnPath("f"), fnBody(t, "f", "afterBackup", "")})
	held, _ := stored(t, stA, "f")

	srvB, stB := newConcurrencyServer(t, backup)
	rv, handler := stored(t, stB, "f")
	require.Equal(t, atBackup, rv)
	require.Equal(t, "h2", handler)

	requireConflict(t, send(t, srvB, http.MethodPut, fnPath("f"), fnBody(t, "f", "fromA", held)))
	requireConflict(t, send(t, srvB, http.MethodDelete, fnPath("f"), nil, `"`+held+`"`))
	rv, handler = stored(t, stB, "f")
	require.Equal(t, atBackup, rv)
	require.Equal(t, "h2", handler, "f keeps B's content")
}

// raceStore runs write on the stored copy of the next races objects of kind it reads and updates it, right after
// each read: a concurrent write (a controller's status write, a replace) landing between a handler's read and its
// store write. gets counts the reads of kind.
type raceStore struct {
	store.Store
	kind  v1.Kind
	write func(v1.Object)
	races atomic.Int32
	gets  atomic.Int32
}

func (s *raceStore) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	read, err := s.Store.Get(ctx, gvk, ns, name)
	if err != nil || gvk.Kind != s.kind {
		return read, err
	}
	s.gets.Add(1)
	if s.races.Add(-1) < 0 {
		return read, nil
	}
	o, err := s.Store.Get(ctx, gvk, ns, name)
	if err != nil {
		return nil, err
	}
	s.write(o)
	if _, err := s.Update(ctx, o); err != nil {
		return nil, err
	}
	return read, nil
}

// flipPhase is a controller's status write.
func flipPhase(o v1.Object) {
	status := o.(v1.StatusObject).GetStatus()
	if status.Phase == v1.PhaseReady {
		status.Phase = v1.PhaseDeploying
	} else {
		status.Phase = v1.PhaseReady
	}
}

// A write without a client version is unconditional (ADR-0210 Decision 3): a controller's status write between the
// server's read and its write must not turn it into a 409. A PUT that carries a version keeps its 409.
func TestIssue900_UnversionedWriteSurvivesStatusWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		st := &raceStore{Store: store.New(memory.New()), kind: v1.KindFunction, write: flipPhase}
		srv := concurrencyServerOn(t, st)
		twoVersions(t, srv, st)

		st.races.Store(1)
		rec := send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h3", ""))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		o, err := st.Store.Get(ctx, v1.KindFunction.GVK(), groupNS, "f")
		require.NoError(t, err)
		require.Equal(t, "h3", o.(*v1.Function).Spec.Handler)
		require.Equal(t, v1.PhaseReady, o.(*v1.Function).Status.Phase, "the controller's status write is kept")

		cur := o.GetObjectMeta().ResourceVersion
		st.races.Store(1)
		requireConflict(t, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h4", cur)))

		st.races.Store(1000)
		requireConflict(t, send(t, srv, http.MethodPut, fnPath("f"), fnBody(t, "f", "h4", "")))
		o, err = st.Store.Get(ctx, v1.KindFunction.GVK(), groupNS, "f")
		require.NoError(t, err)
		require.Equal(t, "h3", o.(*v1.Function).Spec.Handler, "a status that changes on every read still ends in 409")
	})

	t.Run("kvstore handover", func(t *testing.T) {
		t.Parallel()
		st := &raceStore{Store: store.New(memory.New()), kind: v1.KindKVStore, write: flipPhase}
		srv := kvServerOn(t, st)
		wf := kvWorkflow(t, st, "w-keep", v1.DeletionRetain)
		keptStore(t, st, "w-keep", markerOf("w", "u1"))

		st.races.Store(1)
		require.Equal(t, http.StatusOK, handover(t, srv, "operator-token", "w-keep", "w"))
		o, err := st.Store.Get(ctx, v1.KindKVStore.GVK(), kvNS, "w-keep")
		require.NoError(t, err)
		require.Equal(t, []v1.OwnerReference{workflow.KVMarker(wf)}, o.GetObjectMeta().OwnerReferences)
		require.Equal(t, v1.PhaseReady, o.(*v1.KVStore).Status.Phase, "the controller's status write is kept")
	})
}

// fullPrefixes is a BlobProber whose listed prefixes hold objects.
type fullPrefixes map[string]bool

func (p fullPrefixes) HasAny(_ context.Context, _ v1.NamespaceName, _ v1.ObjectName, prefix string) (bool, error) {
	return p[prefix], nil
}

// An unversioned delete is judged by bucket-deletion-protection (ADR-0080). It reads other objects (the namespace's
// Functions), but ADR-0147 Decision 2 leaves it unmarked, so no namespace lock orders the delete with a replace of
// the Bucket. A replace that adds a prefix holding objects after the delete's read must still refuse the delete; one
// that changes nothing protected must not turn it into a 409; a write on every attempt ends in 409 after
// ownReadAttempts reads. A client version is not retried: its race is one read and a 409.
func TestDeleteRechecksProtectionWrittenConcurrently(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		write   func(*v1.Bucket)
		races   int32
		ifMatch bool
		code    int
		gets    int32
	}{
		"protection added": {
			write: func(b *v1.Bucket) { b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: "gold"}) },
			races: 1, code: http.StatusConflict, gets: 2,
		},
		"unprotected change": {
			write: func(b *v1.Bucket) { b.Spec.MaxObjectBytes = 1 << 20 },
			races: 1, code: http.StatusNoContent, gets: 2,
		},
		"write on every attempt": {
			write: func(b *v1.Bucket) { b.Spec.MaxObjectBytes++ },
			races: 1000, code: http.StatusConflict, gets: controlplane.OwnReadAttempts,
		},
		"client version": {
			write: func(b *v1.Bucket) { b.Spec.MaxObjectBytes = 1 << 20 },
			races: 1, ifMatch: true, code: http.StatusConflict, gets: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := &raceStore{Store: store.New(memory.New()), kind: v1.KindBucket, write: func(o v1.Object) { tc.write(o.(*v1.Bucket)) }}
			srv, err := controlplane.NewServer(controlplane.Deps{
				Store: st, Authorizer: rbac.New(),
				Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
					devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{groupNS}},
				}),
				Admissions: []admission.Admission{
					admission.NewBucketDeletionProtectionAdmission(raceReader{st}, fullPrefixes{"gold": true}),
				},
			})
			require.NoError(t, err)
			obj, _ := v1.NewObject(v1.KindBucket)
			b := obj.(*v1.Bucket)
			b.Name, b.Namespace, b.ResourceGroup = "b", groupNS, "team"
			b.Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}}
			created, err := st.Create(ctx, b)
			require.NoError(t, err)
			var ifMatch []string
			if tc.ifMatch {
				ifMatch = append(ifMatch, `"`+created.GetObjectMeta().ResourceVersion+`"`)
			}

			st.races.Store(tc.races)
			rec := send(t, srv, http.MethodDelete, kindPath(groupNS, "buckets")+"/b", nil, ifMatch...)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			require.Equal(t, tc.gets, st.gets.Load(), "reads of the Bucket")
			require.Equal(t, tc.code == http.StatusConflict, present(t, st, v1.KindBucket, groupNS, "b"))
			if name == "protection added" {
				_, detail := problemOf(t, rec)
				require.Contains(t, detail, `prefix "gold" still holds objects`)
			}
		})
	}
}
