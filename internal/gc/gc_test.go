package gc_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow"
)

const ns v1.NamespaceName = "default"

// purges records each Purge as "<ns>/<bucket>".
type purges struct {
	mu    sync.Mutex
	calls []string
}

func (p *purges) Purge(_ context.Context, ns v1.NamespaceName, bucket v1.ObjectName) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, string(ns)+"/"+string(bucket))
	return nil
}

func (p *purges) got() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func newCollector(t testing.TB, st store.Store, interval time.Duration) *gc.Collector {
	t.Helper()
	c, err := gc.New(gc.Deps{Store: st, Purger: &purges{}, Interval: interval})
	require.NoError(t, err)
	return c
}

func object(t testing.TB, kind v1.Kind, name v1.ObjectName) v1.Object {
	t.Helper()
	obj, ok := v1.NewObject(kind)
	require.True(t, ok)
	m := obj.GetObjectMeta()
	m.Namespace, m.Name, m.ResourceGroup = ns, name, "rg1"
	switch o := obj.(type) {
	case *v1.Workflow:
		o.Spec.Steps = []v1.WorkflowStep{{Name: "s1", Function: &v1.FunctionStep{Ref: "x"}}}
	case *v1.Function:
		o.Spec.Runtime, o.Spec.Handler, o.Spec.Image = "nodejs22", "handle", "file:///tmp/x.mjs"
	case *v1.Revision:
		o.Spec.Function = v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "fn"}
		o.Spec.Number, o.Spec.Runtime, o.Spec.Handler, o.Spec.Image = 1, "nodejs22", "handle", "file:///tmp/x.mjs"
	case *v1.Site:
		o.Spec.Image, o.Spec.Prefix, o.Spec.Bucket.Name = "oci-layout://x:web", "web", "assets"
	case *v1.Identity:
		o.Spec.Type = v1.IdentityTypeExternal
	case *v1.Secret:
		o.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	case *v1.Route:
		o.Spec.Host = string(name) + ".example.com"
		o.Spec.Rules = []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: "fn"}}}
	case *v1.EventSource:
		o.Spec.Timer = &v1.TimerSource{Events: []v1.TimerEvent{{Name: "hourly", Interval: v1.Duration(time.Hour)}}}
	case *v1.Sensor:
		o.Spec.On = []v1.Dependency{{Name: "tick", Source: "tick", Event: "hourly"}}
		o.Spec.Do = []v1.Action{{Name: "plan", On: "tick", Workflow: "wf"}}
	case *v1.CatalogService:
		o.Spec.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "lake", Prefix: "lake"}}
		o.Spec.Catalog = v1.CatalogRef{Bucket: "lake", Prefix: "lake"}
	case *v1.AppRevision:
		o.Spec.App, o.Spec.Number = v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "app"}, 1
	}
	return obj
}

func create(t testing.TB, st store.Store, kind v1.Kind, name v1.ObjectName, owner v1.Object) v1.Object {
	t.Helper()
	obj := object(t, kind, name)
	if owner != nil {
		om := owner.GetObjectMeta()
		ref := v1.OwnerReference{
			ObjectRef: v1.ObjectRef{Kind: owner.GroupVersionKind().Kind, Namespace: om.Namespace, Name: om.Name},
			UID:       om.UID, Controller: true,
		}
		obj.GetObjectMeta().OwnerReferences = []v1.OwnerReference{ref}
		if kind == v1.KindKVStore || kind == v1.KindBucket { // a store's maker marks it (ADR-0178, ADR-0199)
			ref.Controller = false
			obj.GetObjectMeta().OwnerReferences = append(obj.GetObjectMeta().OwnerReferences, ref)
		}
	}
	out, err := st.Create(context.Background(), obj)
	require.NoError(t, err, "create %s/%s", kind, name)
	return out
}

func exists(t testing.TB, st store.Store, kind v1.Kind, name v1.ObjectName) bool {
	t.Helper()
	_, err := st.Get(context.Background(), kind.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

func del(t testing.TB, st store.Store, kind v1.Kind, name v1.ObjectName) {
	t.Helper()
	require.NoError(t, st.Delete(context.Background(), kind.GVK(), ns, name, ""))
}

// fleet creates one owner and child of every pair, and returns them by pair.
func fleet(t testing.TB, st store.Store) map[gc.Pair][2]v1.Object {
	t.Helper()
	wf := create(t, st, v1.KindWorkflow, "wf", nil)
	id := create(t, st, v1.KindIdentity, "id", nil)
	site := create(t, st, v1.KindSite, "web", nil)
	fn := create(t, st, v1.KindFunction, "fn", nil)
	app := create(t, st, v1.KindApp, "app", nil)
	f := map[gc.Pair][2]v1.Object{
		{Owner: v1.KindWorkflow, Child: v1.KindFunction}: {wf, create(t, st, v1.KindFunction, "wf-s1", wf)},
		{Owner: v1.KindWorkflow, Child: v1.KindKVStore}:  {wf, create(t, st, v1.KindKVStore, "wf-state", wf)},
		{Owner: v1.KindIdentity, Child: v1.KindSecret}:   {id, create(t, st, v1.KindSecret, "id", id)},
		{Owner: v1.KindSite, Child: v1.KindRoute}:        {site, create(t, st, v1.KindRoute, "web", site)},
		{Owner: v1.KindFunction, Child: v1.KindRevision}: {fn, create(t, st, v1.KindRevision, "fn-1", fn)},
	}
	for _, p := range gc.Pairs() {
		if p.Owner == v1.KindApp {
			name := v1.ObjectName("app-" + strings.ToLower(string(p.Child)))
			if p.Child == v1.KindAppRevision {
				name = v1.AppRevisionName("app", 1)
			}
			f[p] = [2]v1.Object{app, create(t, st, p.Child, name, app)}
		}
	}
	rev := f[gc.Pair{Owner: v1.KindApp, Child: v1.KindAppRevision}][1]
	f[gc.Pair{Owner: v1.KindAppRevision, Child: v1.KindInvocation}] = [2]v1.Object{rev, create(t, st, v1.KindInvocation, "inv-app", rev)}
	return f
}

func deleteOwners(t testing.TB, st store.Store) {
	t.Helper()
	del(t, st, v1.KindWorkflow, "wf")
	del(t, st, v1.KindIdentity, "id")
	del(t, st, v1.KindSite, "web")
	del(t, st, v1.KindFunction, "fn")
	del(t, st, v1.KindApp, "app")
}

// scenario: live-owner-children-never-collected — sweeps delete nothing while every owner lives.
func TestScenarioLiveOwnerChildrenNeverCollected(t *testing.T) {
	st := store.New(memory.New())
	f := fleet(t, st)
	c := newCollector(t, st, 0)
	for range 3 {
		require.NoError(t, c.CollectNamespace(context.Background(), ""))
	}
	for p, oc := range f {
		require.True(t, exists(t, st, p.Child, oc[1].GetObjectMeta().Name), "the child of a live owner stays: %v", p)
	}
}

func TestCollectNamespaceDeletesChildrenOfDeletedOrReplacedOwners(t *testing.T) {
	st := store.New(memory.New())
	f := fleet(t, st)
	unowned := create(t, st, v1.KindSecret, "plain", nil)
	weak := object(t, v1.KindSecret, "weak")
	weak.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindIdentity, Name: "gone"}, UID: "u"}}
	_, err := st.Create(context.Background(), weak)
	require.NoError(t, err)

	deleteOwners(t, st)
	create(t, st, v1.KindIdentity, "id", nil) // a namesake with another UID

	require.NoError(t, newCollector(t, st, 0).CollectNamespace(context.Background(), ns))
	for p, oc := range f {
		require.False(t, exists(t, st, p.Child, oc[1].GetObjectMeta().Name), "the child of a dead owner is collected: %v", p)
	}
	require.True(t, exists(t, st, v1.KindSecret, unowned.GetObjectMeta().Name), "an unowned object stays")
	require.True(t, exists(t, st, v1.KindSecret, "weak"), "a non-controller ownerRef is ignored")
}

func TestCollectNamespaceIsScopedToItsNamespace(t *testing.T) {
	st := store.New(memory.New())
	wf := create(t, st, v1.KindWorkflow, "wf", nil)
	other := object(t, v1.KindKVStore, "wf-state")
	om := other.GetObjectMeta()
	om.Namespace = "other"
	ref := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Name: "wf"}, UID: wf.GetObjectMeta().UID}
	marker := ref
	ref.Controller = true
	om.OwnerReferences = []v1.OwnerReference{ref, marker}
	_, err := st.Create(context.Background(), other)
	require.NoError(t, err)
	del(t, st, v1.KindWorkflow, "wf")

	c := newCollector(t, st, 0)
	require.NoError(t, c.CollectNamespace(context.Background(), ns))
	_, err = st.Get(context.Background(), v1.KindKVStore.GVK(), "other", "wf-state")
	require.NoError(t, err, "a pass over default leaves other alone")
	require.NoError(t, c.CollectNamespace(context.Background(), "other"))
	_, err = st.Get(context.Background(), v1.KindKVStore.GVK(), "other", "wf-state")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an empty owner namespace is the child's: wf is dead there")
}

// conflictStore changes a child just before the collector's first delete of it, so the delete hits a Conflict.
type conflictStore struct {
	store.Store
	once   sync.Once
	mutate func(v1.Object) v1.Object
}

func (s *conflictStore) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	s.once.Do(func() {
		cur, err := s.Get(ctx, gvk, ns, name)
		if err == nil {
			_, _ = s.Update(ctx, s.mutate(cur))
		}
	})
	return s.Store.Delete(ctx, gvk, ns, name, rv)
}

func TestConflictRejudgesWithAFreshOwnerRead(t *testing.T) {
	t.Run("still dead", func(t *testing.T) {
		st := &conflictStore{Store: store.New(memory.New()), mutate: func(o v1.Object) v1.Object {
			o.GetObjectMeta().Tags = v1.Tags{"touched": "yes"}
			return o
		}}
		create(t, st, v1.KindSecret, "id", create(t, st, v1.KindIdentity, "id", nil))
		del(t, st, v1.KindIdentity, "id")
		require.NoError(t, newCollector(t, st, 0).CollectNamespace(context.Background(), ns))
		require.False(t, exists(t, st, v1.KindSecret, "id"))
	})
	t.Run("re-owned by a live owner", func(t *testing.T) {
		base := store.New(memory.New())
		create(t, base, v1.KindSecret, "id", create(t, base, v1.KindIdentity, "id", nil))
		del(t, base, v1.KindIdentity, "id")
		live := create(t, base, v1.KindIdentity, "id", nil)
		st := &conflictStore{Store: base, mutate: func(o v1.Object) v1.Object {
			o.GetObjectMeta().OwnerReferences[0].UID = live.GetObjectMeta().UID
			return o
		}}
		require.NoError(t, newCollector(t, st, 0).CollectNamespace(context.Background(), ns))
		require.True(t, exists(t, st, v1.KindSecret, "id"), "the re-judgment sees the live owner")
	})
}

func TestNewValidatesDeps(t *testing.T) {
	_, err := gc.New(gc.Deps{Purger: &purges{}})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = gc.New(gc.Deps{Store: store.New(memory.New())})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a purger is required")
	_, err = gc.New(gc.Deps{Store: store.New(memory.New()), Purger: &purges{}, Interval: -time.Second})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func runCollector(t *testing.T, c *gc.Collector) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

// The owners are deleted only after the start sweep has returned, and the next sweep is an hour away: only the
// watch can collect their children.
func TestRunCollectsADeletedOwnersChildrenFromItsWatch(t *testing.T) {
	st := store.New(memory.New())
	f := fleet(t, st)
	c := newCollector(t, st, time.Hour)
	swept := make(chan struct{})
	gc.OnStartSwept(c, func() { close(swept) })
	runCollector(t, c)
	select {
	case <-swept:
	case <-time.After(5 * time.Second):
		t.Fatal("the start sweep did not return")
	}
	for p, oc := range f {
		require.True(t, exists(t, st, p.Child, oc[1].GetObjectMeta().Name), "the start sweep keeps the child of a live owner: %v", p)
	}
	deleteOwners(t, st)
	require.Eventually(t, func() bool {
		for p, oc := range f {
			if exists(t, st, p.Child, oc[1].GetObjectMeta().Name) {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
}

func TestRunSweepsOnStartAndEveryInterval(t *testing.T) {
	st := store.New(memory.New())
	create(t, st, v1.KindSecret, "early", create(t, st, v1.KindIdentity, "early", nil))
	del(t, st, v1.KindIdentity, "early")
	runCollector(t, newCollector(t, st, 50*time.Millisecond))
	require.Eventually(t, func() bool { return !exists(t, st, v1.KindSecret, "early") }, 5*time.Second, 10*time.Millisecond, "the start sweep collects a leftover")

	// A child naming an owner UID that never existed has no delete event: only the periodic sweep finds it.
	late := object(t, v1.KindSecret, "late")
	late.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindIdentity, Name: "never"}, UID: "nope", Controller: true}}
	_, err := st.Create(context.Background(), late)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exists(t, st, v1.KindSecret, "late") }, 5*time.Second, 10*time.Millisecond)
}

func TestCollectNamespaceBesideRun(t *testing.T) {
	st := store.New(memory.New())
	c := newCollector(t, st, 5*time.Millisecond)
	runCollector(t, c)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				name := v1.ObjectName(fmt.Sprintf("o-%d-%d", w, i))
				owner := create(t, st, v1.KindIdentity, name, nil)
				create(t, st, v1.KindSecret, name, owner)
				del(t, st, v1.KindIdentity, name)
				if err := c.CollectNamespace(context.Background(), ns); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.Eventually(t, func() bool {
		res, err := st.List(context.Background(), v1.KindSecret.GVK(), store.ListOptions{})
		require.NoError(t, err)
		return len(res.Items) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

// ownerRefWriter matches a non-test write of a controller OwnerReference list.
var ownerRefWriter = regexp.MustCompile(`OwnerReferences\s*(=|:)\s*\[\]v1\.OwnerReference\{`)

// writers maps each file that stamps OwnerReferences to the pairs it stamps.
func writers() map[string][]gc.Pair {
	return map[string][]gc.Pair{
		"internal/workflow/reconcile_workflow.go": {{Owner: v1.KindWorkflow, Child: v1.KindFunction}, {Owner: v1.KindWorkflow, Child: v1.KindKVStore}},
		"internal/services/identity/reconcile.go": {{Owner: v1.KindIdentity, Child: v1.KindSecret}},
		"internal/site/reconcile.go":              {{Owner: v1.KindSite, Child: v1.KindRoute}},
		"internal/function/function.go":           {{Owner: v1.KindFunction, Child: v1.KindRevision}},
		"internal/controlplane/kvhandover.go":     nil, // writes only the non-controller KVStore marker (ADR-0178)
		"internal/app/revision.go":                {{Owner: v1.KindApp, Child: v1.KindAppRevision}},
		"internal/app/hooks.go":                   {{Owner: v1.KindAppRevision, Child: v1.KindInvocation}},
	}
}

// appPairs is the pairs the App reconciler stamps on its parts (ADR-0199 Decision 7).
func appPairs() []gc.Pair {
	var out []gc.Pair
	for _, k := range []v1.Kind{
		v1.KindFunction, v1.KindWorkflow, v1.KindEventSource, v1.KindSensor, v1.KindRoute, v1.KindSite,
		v1.KindCatalogService, v1.KindKVStore, v1.KindBucket, v1.KindConfigMap,
	} {
		out = append(out, gc.Pair{Owner: v1.KindApp, Child: k})
	}
	return out
}

func TestPairsCoverEveryControllerRef(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == ".modcopy" || n == ".cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ownerRefWriter.Match(b) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	}))
	var known []string
	want := appPairs()
	for f, pairs := range writers() {
		known = append(known, f)
		want = append(want, pairs...)
	}
	sort.Strings(known)
	sort.Strings(found)
	require.Equal(t, known, found, "every OwnerReferences writer is listed with the pairs it stamps")

	kinds := map[v1.Kind]bool{}
	for _, k := range v1.AllKinds() {
		kinds[k] = true
	}
	for _, p := range gc.Pairs() {
		require.True(t, kinds[p.Owner] && kinds[p.Child], "pair %v names real kinds", p)
		require.True(t, p.Owner.Namespaced() && p.Child.Namespaced(), "pair %v is namespaced", p)
	}
	require.ElementsMatch(t, want, gc.Pairs())
	require.Equal(t, v1.KindRevision, gc.Pairs()[len(gc.Pairs())-1].Child, "Revision is collected last")
}

// ADR-0200 Contracts: (App, AppRevision) follows ADR-0199's App pairs and precedes (Function, Revision).
func TestPairsOrderAppRevisionAfterTheAppSections(t *testing.T) {
	pairs := gc.Pairs()
	at := slices.Index(pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindAppRevision})
	require.GreaterOrEqual(t, at, 0, "(App, AppRevision) is a pair")
	for i, p := range pairs {
		if p.Owner == v1.KindApp && p.Child != v1.KindAppRevision {
			require.Less(t, i, at, "the App section pair %v precedes (App, AppRevision)", p)
		}
	}
	require.Less(t, at, slices.Index(pairs, gc.Pair{Owner: v1.KindFunction, Child: v1.KindRevision}))
}

// ADR-0214 Contracts: (AppRevision, Invocation) comes right after (App, AppRevision), so a hook call's record goes with
// its revision in one sweep.
func TestPairsOrderHookInvocationAfterAppRevision(t *testing.T) {
	pairs := gc.Pairs()
	at := slices.Index(pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindAppRevision})
	require.Equal(t, at+1, slices.Index(pairs, gc.Pair{Owner: v1.KindAppRevision, Child: v1.KindInvocation}))
}

// aead is an AES-GCM Encryptor so the benchmark pays Secret decryption like a durable store.
type aead struct{ c cipher.AEAD }

func newAEAD(b *testing.B) aead {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(b, err)
	blk, err := aes.NewCipher(key)
	require.NoError(b, err)
	g, err := cipher.NewGCM(blk)
	require.NoError(b, err)
	return aead{c: g}
}

func (a aead) Encrypt(_ context.Context, p []byte) ([]byte, error) {
	nonce := make([]byte, a.c.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.c.Seal(nonce, nonce, p, nil), nil
}

func (a aead) Decrypt(_ context.Context, c []byte) ([]byte, error) {
	n := a.c.NonceSize()
	return a.c.Open(nil, c[:n], c[n:], nil)
}

// BenchmarkSweep is one sweep over 10 000 live-owned children, 1 000 of them encrypted Secrets.
func BenchmarkSweep(b *testing.B) {
	st := store.New(memory.New(), store.WithEncryptor([]v1.Kind{v1.KindSecret}, newAEAD(b)))
	for i := range 100 {
		wf := create(b, st, v1.KindWorkflow, v1.ObjectName(fmt.Sprintf("wf%d", i)), nil)
		for j := range 45 {
			create(b, st, v1.KindFunction, v1.ObjectName(fmt.Sprintf("wf%d-s%d", i, j)), wf)
		}
		for j := range 45 {
			create(b, st, v1.KindKVStore, v1.ObjectName(fmt.Sprintf("wf%d-kv%d", i, j)), wf)
		}
	}
	for i := range 1000 {
		create(b, st, v1.KindSecret, v1.ObjectName(fmt.Sprintf("id%d", i)), create(b, st, v1.KindIdentity, v1.ObjectName(fmt.Sprintf("id%d", i)), nil))
	}
	c := newCollector(b, st, 0)
	b.ResetTimer()
	for b.Loop() {
		require.NoError(b, c.CollectNamespace(context.Background(), ""))
	}
}

type fixedRuntime struct{}

func (fixedRuntime) Runtime(context.Context, string) (v1.RuntimeName, error) { return "nodejs22", nil }

// scenario: upgrade-refuses-older-store-and-collector-keeps-it — a user's store older than w, which the previous
// materializer gave w's controller ref, stays unmarked at upgrade, w is refused, and the collector keeps it.
func TestScenarioUpgradeRefusesOlderStoreAndCollectorKeepsIt(t *testing.T) {
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	user := object(t, v1.KindKVStore, "shared").(*v1.KVStore)
	user.Spec.Tables = []v1.KVTable{{Name: "t"}}
	_, err := st.Create(ctx, user)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	wf := object(t, v1.KindWorkflow, "w").(*v1.Workflow)
	wf.Spec.Steps = []v1.WorkflowStep{{Name: "s1", Function: &v1.FunctionStep{Image: "oci:s1"}}}
	wf.Spec.KV = []v1.WorkflowKVStore{{Name: "shared", Deletion: v1.DeletionDelete, Tables: []v1.KVTable{{Name: "t"}}}}
	obj, err := st.Create(ctx, wf)
	require.NoError(t, err)
	wf = obj.(*v1.Workflow)
	cur, err := st.Get(ctx, v1.KindKVStore.GVK(), ns, "shared")
	require.NoError(t, err)
	cur.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{
		ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: ns, Name: "w"}, UID: wf.UID, Controller: true, BlockOwnerDeletion: true,
	}}
	prev, err := st.Update(ctx, cur)
	require.NoError(t, err)

	require.NoError(t, workflow.MarkKVStoresOnce(ctx, st, nil))
	r := workflow.NewWorkflowReconciler(st, workflow.NewMaterializer(st, fixedRuntime{}, nil, 0), nil, nil, 0)
	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: ns, Name: "w"})
	require.NoError(t, err)
	got, err := st.Get(ctx, v1.KindWorkflow.GVK(), ns, "w")
	require.NoError(t, err)
	ready, ok := got.(*v1.Workflow).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "KVStoreNotOwned", ready.Reason)
	after, err := st.Get(ctx, v1.KindKVStore.GVK(), ns, "shared")
	require.NoError(t, err)
	require.Equal(t, prev.GetObjectMeta().ResourceVersion, after.GetObjectMeta().ResourceVersion, "no write")

	del(t, st, v1.KindWorkflow, "w")
	c := newCollector(t, st, 0)
	for range 2 {
		require.NoError(t, c.CollectNamespace(ctx, ns))
	}
	require.True(t, exists(t, st, v1.KindKVStore, "shared"), "an unmarked store is never collected")
}

// holdPins stores an open WorkflowRun in ns whose status pins revision rev of fn (ADR-0190).
func holdPins(t *testing.T, st store.Store, fn v1.Object, rev v1.ObjectName) v1.Object {
	t.Helper()
	run := object(t, v1.KindWorkflowRun, "r1").(*v1.WorkflowRun)
	run.Spec.Workflow = "wf"
	created, err := st.Create(context.Background(), run)
	require.NoError(t, err)
	run = created.(*v1.WorkflowRun)
	run.Status.Phase = v1.RunRunning
	run.Status.Pins = []v1.RevisionPin{{Function: fn.GetObjectMeta().Name, FunctionUID: fn.GetObjectMeta().UID, Revision: rev}}
	updated, err := st.Update(context.Background(), run)
	require.NoError(t, err)
	return updated
}

// A dead-owned Function or Revision an open workflow run pins waits for a sweep after the run ends (ADR-0190
// Decision 7).
func TestHeldChildrenWaitForTheirRun(t *testing.T) {
	st := store.New(memory.New())
	f := fleet(t, st)
	revPair := gc.Pair{Owner: v1.KindFunction, Child: v1.KindRevision}
	stepFn, fnRev := f[gc.Pair{Owner: v1.KindWorkflow, Child: v1.KindFunction}][1], f[revPair][1]
	run := holdPins(t, st, stepFn, "wf-s1-1")
	runPin := run.(*v1.WorkflowRun)
	runPin.Status.Pins = append(runPin.Status.Pins, v1.RevisionPin{Function: "fn", FunctionUID: f[revPair][0].GetObjectMeta().UID, Revision: fnRev.GetObjectMeta().Name})
	run, err := st.Update(context.Background(), runPin)
	require.NoError(t, err)
	del(t, st, v1.KindWorkflow, "wf")
	del(t, st, v1.KindFunction, "fn")

	c := newCollector(t, st, 0)
	require.NoError(t, c.CollectNamespace(context.Background(), ns))
	require.True(t, exists(t, st, v1.KindFunction, "wf-s1"), "a held step Function waits")
	require.True(t, exists(t, st, v1.KindRevision, "fn-1"), "a held Revision waits")
	require.False(t, exists(t, st, v1.KindKVStore, "wf-state"), "an unheld child of the same owner is collected")

	ended := run.(*v1.WorkflowRun)
	ended.Status.Phase = v1.RunCancelled
	_, err = st.Update(context.Background(), ended)
	require.NoError(t, err)
	require.NoError(t, c.CollectNamespace(context.Background(), ns))
	require.False(t, exists(t, st, v1.KindFunction, "wf-s1"), "the next sweep after the run ends collects it")
	require.False(t, exists(t, st, v1.KindRevision, "fn-1"))
}

// ownedBy returns obj with owner's controller reference and, with marker, the non-controller marker too.
func ownedBy(obj, owner v1.Object, controller, marker bool) v1.Object {
	om := owner.GetObjectMeta()
	ref := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: owner.GroupVersionKind().Kind, Namespace: om.Namespace, Name: om.Name}, UID: om.UID}
	var refs []v1.OwnerReference
	if controller {
		c := ref
		c.Controller = true
		refs = append(refs, c)
	}
	if marker {
		refs = append(refs, ref)
	}
	obj.GetObjectMeta().OwnerReferences = refs
	return obj
}

func put(t testing.TB, st store.Store, obj v1.Object) v1.Object {
	t.Helper()
	out, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
	return out
}

// deleteHook runs after once a Delete has returned.
type deleteHook struct {
	store.Store
	after func()
}

func (s deleteHook) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	err := s.Store.Delete(ctx, gvk, ns, name, rv)
	s.after()
	return err
}

// ADR-0199 Decision 7: purge, delete at the resourceVersion, then purge again unless a namesake exists.
func TestDeleteBucket(t *testing.T) {
	ctx := context.Background()
	t.Run("purges, deletes and purges again", func(t *testing.T) {
		st := store.New(memory.New())
		b := put(t, st, object(t, v1.KindBucket, "b")).(*v1.Bucket)
		p := &purges{}
		require.NoError(t, gc.DeleteBucket(ctx, st, p, b))
		require.Equal(t, []string{"default/b", "default/b"}, p.got())
		require.False(t, exists(t, st, v1.KindBucket, "b"))
	})
	t.Run("a Bucket of that name exists again", func(t *testing.T) {
		base := store.New(memory.New())
		b := put(t, base, object(t, v1.KindBucket, "b")).(*v1.Bucket)
		var again v1.Object
		st := deleteHook{Store: base, after: func() { again = put(t, base, object(t, v1.KindBucket, "b")) }}
		p := &purges{}
		require.NoError(t, gc.DeleteBucket(ctx, st, p, b))
		require.Equal(t, []string{"default/b"}, p.got(), "the namesake's prefix is its own")
		cur, err := base.Get(ctx, v1.KindBucket.GVK(), ns, "b")
		require.NoError(t, err)
		require.Equal(t, again.GetObjectMeta().UID, cur.GetObjectMeta().UID)
	})
	t.Run("a changed Bucket is a Conflict", func(t *testing.T) {
		st := store.New(memory.New())
		b := put(t, st, object(t, v1.KindBucket, "b")).(*v1.Bucket)
		changed, err := st.Get(ctx, v1.KindBucket.GVK(), ns, "b")
		require.NoError(t, err)
		changed.GetObjectMeta().Tags = v1.Tags{"touched": "yes"}
		_, err = st.Update(ctx, changed)
		require.NoError(t, err)
		p := &purges{}
		err = gc.DeleteBucket(ctx, st, p, b)
		require.Equal(t, fault.Conflict, fault.KindOf(err), "got %v", err)
		require.Equal(t, []string{"default/b"}, p.got(), "no second purge while the Bucket stays")
		require.True(t, exists(t, st, v1.KindBucket, "b"))
	})
	t.Run("a Bucket already gone is purged again", func(t *testing.T) {
		st := store.New(memory.New())
		b := put(t, st, object(t, v1.KindBucket, "b")).(*v1.Bucket)
		del(t, st, v1.KindBucket, "b")
		p := &purges{}
		require.NoError(t, gc.DeleteBucket(ctx, st, p, b))
		require.Equal(t, []string{"default/b", "default/b"}, p.got())
	})
}

// ADR-0199 Decision 6: one row per field that keeps an object in use.
func TestInUse(t *testing.T) {
	ctx := context.Background()
	fn := func(name v1.ObjectName, mutate func(*v1.FunctionSpec)) v1.Object {
		f := object(t, v1.KindFunction, name).(*v1.Function)
		mutate(&f.Spec)
		return f
	}
	for _, tc := range []struct {
		name   string
		target v1.Object
		user   v1.Object
	}{
		{"a Function's spec.links", object(t, v1.KindFunction, "callee"), fn("caller", func(s *v1.FunctionSpec) {
			s.Links = []v1.FunctionLink{{Alias: "callee", Target: "callee"}}
		})},
		{"a Function's spec.kv", object(t, v1.KindKVStore, "cache"), fn("api", func(s *v1.FunctionSpec) {
			s.KV = []v1.FunctionKV{{Alias: "cache", Store: "cache", Table: "entries"}}
		})},
		{"a Function's spec.blob", object(t, v1.KindBucket, "lake"), fn("api", func(s *v1.FunctionSpec) {
			s.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: "lake", Prefix: "raw"}}
		})},
		{"a CatalogService's spec.blob", object(t, v1.KindBucket, "lake"), object(t, v1.KindCatalogService, "catalog")},
		{"an EventSource's spec.blob.bucket", object(t, v1.KindBucket, "lake"), func() v1.Object {
			es := object(t, v1.KindEventSource, "drops").(*v1.EventSource)
			es.Spec.Timer = nil
			es.Spec.Blob = &v1.BlobSource{Bucket: "lake", Events: []v1.BlobEvent{{Name: "dropped"}}}
			return es
		}()},
		{"a Route's spec.rules[].backend.static.bucket", object(t, v1.KindBucket, "lake"), func() v1.Object {
			r := object(t, v1.KindRoute, "docs").(*v1.Route)
			r.Spec.Rules = append(r.Spec.Rules, v1.RouteRule{Path: "/docs", Backend: v1.RouteBackend{Static: &v1.StaticBackend{Bucket: "lake"}}})
			return r
		}()},
		{"a Site's spec.bucket.name", object(t, v1.KindBucket, "assets"), object(t, v1.KindSite, "web")},
		{"a Function's spec.config", object(t, v1.KindConfigMap, "settings"), fn("api", func(s *v1.FunctionSpec) {
			s.Config = []v1.ObjectName{"settings"}
		})},
		{"a CatalogService's spec.config", object(t, v1.KindConfigMap, "settings"), func() v1.Object {
			c := object(t, v1.KindCatalogService, "catalog").(*v1.CatalogService)
			c.Spec.Config = []v1.ObjectName{"settings"}
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(memory.New())
			target := put(t, st, tc.target)
			user := put(t, st, tc.user)
			um := user.GetObjectMeta()
			want := v1.ObjectRef{Kind: user.GroupVersionKind().Kind, Namespace: um.Namespace, Name: um.Name}

			got, used, err := gc.InUse(ctx, st, target, nil)
			require.NoError(t, err)
			require.True(t, used)
			require.Equal(t, want, got)

			_, used, err = gc.InUse(ctx, st, target, func(o v1.Object) bool { return o.GetObjectMeta().UID == um.UID })
			require.NoError(t, err)
			require.False(t, used, "a skipped user does not count")

			other := put(t, st, func() v1.Object {
				o := object(t, tc.target.GroupVersionKind().Kind, tc.target.GetObjectMeta().Name)
				o.GetObjectMeta().Namespace = "other"
				return o
			}())
			_, used, err = gc.InUse(ctx, st, other, nil)
			require.NoError(t, err)
			require.False(t, used, "a user in another namespace does not count")
		})
	}
	t.Run("a kind nothing uses", func(t *testing.T) {
		st := store.New(memory.New())
		_, used, err := gc.InUse(ctx, st, put(t, st, object(t, v1.KindSecret, "s")), nil)
		require.NoError(t, err)
		require.False(t, used)
	})
}

// ADR-0212 Decision 4: a paused App writes nothing, but the owner GC, not its reconciler, collects its tree once it is
// deleted; a retained store keeps only the marker and stays.
func TestDeletedPausedAppTreeCollected(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	paused := object(t, v1.KindApp, "app").(*v1.App)
	paused.Spec.Paused = true
	app := put(t, st, paused)
	put(t, st, ownedBy(object(t, v1.KindFunction, "todo-api"), app, true, false))
	put(t, st, ownedBy(object(t, v1.KindRoute, "todo-api"), app, true, false))
	put(t, st, ownedBy(object(t, v1.KindKVStore, "todo-cache"), app, true, true))
	put(t, st, ownedBy(object(t, v1.KindKVStore, "todo-store"), app, false, true))
	put(t, st, ownedBy(object(t, v1.KindAppRevision, v1.AppRevisionName("app", 1)), app, true, false))
	del(t, st, v1.KindApp, "app")

	require.NoError(t, newCollector(t, st, 0).CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindFunction, "todo-api"))
	require.False(t, exists(t, st, v1.KindRoute, "todo-api"))
	require.False(t, exists(t, st, v1.KindKVStore, "todo-cache"))
	require.False(t, exists(t, st, v1.KindAppRevision, v1.AppRevisionName("app", 1)))
	require.True(t, exists(t, st, v1.KindKVStore, "todo-store"))
}

// scenario: app-store-in-use-kept (the collector half, ADR-0199 Decision 7) — a store a dead App controls waits
// while something the sweep does not delete uses it; an unused Bucket goes through DeleteBucket.
func TestAppStoreInUseWaitsForALaterSweep(t *testing.T) {
	ctx := context.Background()
	t.Run("a user outside the App", func(t *testing.T) {
		st := store.New(memory.New())
		app := create(t, st, v1.KindApp, "todo", nil)
		put(t, st, ownedBy(object(t, v1.KindKVStore, "todo-cache"), app, true, true))
		put(t, st, ownedBy(object(t, v1.KindBucket, "todo-tmp"), app, true, true))
		put(t, st, ownedBy(object(t, v1.KindBucket, "todo-files"), app, false, true))
		put(t, st, ownedBy(object(t, v1.KindBucket, "unmarked"), app, true, false))
		api := object(t, v1.KindFunction, "todo-api").(*v1.Function)
		api.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "todo-cache", Table: "entries"}}
		api.Spec.Blob = []v1.FunctionBlob{{Alias: "tmp", Bucket: "todo-tmp", Prefix: "tmp"}}
		put(t, st, ownedBy(api, app, true, false))
		audit := object(t, v1.KindFunction, "audit").(*v1.Function)
		audit.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "todo-cache", Table: "entries"}}
		audit = put(t, st, audit).(*v1.Function)
		del(t, st, v1.KindApp, "todo")

		p := &purges{}
		c, err := gc.New(gc.Deps{Store: st, Purger: p})
		require.NoError(t, err)
		require.NoError(t, c.CollectNamespace(ctx, ns))
		require.False(t, exists(t, st, v1.KindFunction, "todo-api"))
		require.False(t, exists(t, st, v1.KindBucket, "todo-tmp"), "a user the App controls does not keep it")
		require.Equal(t, []string{"default/todo-tmp", "default/todo-tmp"}, p.got(), "a collected Bucket goes through DeleteBucket")
		require.True(t, exists(t, st, v1.KindKVStore, "todo-cache"), "audit, outside the App, still binds it")
		require.True(t, exists(t, st, v1.KindBucket, "todo-files"), "a retained Bucket has no controller reference")
		require.True(t, exists(t, st, v1.KindBucket, "unmarked"), "a Bucket without the marker is never collected")

		audit.Spec.KV = nil
		_, err = st.Update(ctx, audit)
		require.NoError(t, err)
		require.NoError(t, c.CollectNamespace(ctx, ns))
		require.False(t, exists(t, st, v1.KindKVStore, "todo-cache"), "the next sweep after the binding goes collects it")
	})
	t.Run("a held Function of the App", func(t *testing.T) {
		st := store.New(memory.New())
		app := create(t, st, v1.KindApp, "todo", nil)
		put(t, st, ownedBy(object(t, v1.KindKVStore, "todo-cache"), app, true, true))
		api := object(t, v1.KindFunction, "todo-api").(*v1.Function)
		api.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "todo-cache", Table: "entries"}}
		run := holdPins(t, st, put(t, st, ownedBy(api, app, true, false)), "todo-api-1").(*v1.WorkflowRun)
		del(t, st, v1.KindApp, "todo")

		c := newCollector(t, st, 0)
		require.NoError(t, c.CollectNamespace(ctx, ns))
		require.True(t, exists(t, st, v1.KindFunction, "todo-api"), "a held Function waits")
		require.True(t, exists(t, st, v1.KindKVStore, "todo-cache"), "a held Function counts as a user")

		run.Status.Phase = v1.RunCancelled
		_, err := st.Update(ctx, run)
		require.NoError(t, err)
		require.NoError(t, c.CollectNamespace(ctx, ns))
		require.False(t, exists(t, st, v1.KindFunction, "todo-api"))
		require.False(t, exists(t, st, v1.KindKVStore, "todo-cache"))
	})
}

// ADR-0213 Decision 5: the GC collects an App's ConfigMaps, and keeps one while a Function the App does not control,
// or a Function of the App an open run holds, names it.
func TestAppConfigMapInUseWaitsForALaterSweep(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	app := create(t, st, v1.KindApp, "todo", nil)
	put(t, st, ownedBy(object(t, v1.KindConfigMap, "todo-settings-0000000001"), app, true, false))
	put(t, st, ownedBy(object(t, v1.KindConfigMap, "todo-settings-0000000002"), app, true, false))
	put(t, st, ownedBy(object(t, v1.KindConfigMap, "todo-unused-0000000003"), app, true, false))
	api := object(t, v1.KindFunction, "todo-api").(*v1.Function)
	api.Spec.Config = []v1.ObjectName{"todo-settings-0000000002"}
	run := holdPins(t, st, put(t, st, ownedBy(api, app, true, false)), "todo-api-1").(*v1.WorkflowRun)
	audit := object(t, v1.KindFunction, "audit").(*v1.Function)
	audit.Spec.Config = []v1.ObjectName{"todo-settings-0000000001"}
	audit = put(t, st, audit).(*v1.Function)
	del(t, st, v1.KindApp, "todo")

	c := newCollector(t, st, 0)
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindConfigMap, "todo-unused-0000000003"), "an unused ConfigMap of the App is collected")
	require.True(t, exists(t, st, v1.KindConfigMap, "todo-settings-0000000001"), "audit, outside the App, still names it")
	require.True(t, exists(t, st, v1.KindConfigMap, "todo-settings-0000000002"), "a held Function counts as a user")

	audit.Spec.Config = nil
	_, err := st.Update(ctx, audit)
	require.NoError(t, err)
	run.Status.Phase = v1.RunCancelled
	_, err = st.Update(ctx, run)
	require.NoError(t, err)
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindFunction, "todo-api"))
	require.False(t, exists(t, st, v1.KindConfigMap, "todo-settings-0000000001"))
	require.False(t, exists(t, st, v1.KindConfigMap, "todo-settings-0000000002"))
}

// ADR-0213 Decision 5: (App, ConfigMap) follows (App, Bucket) and precedes (App, AppRevision).
func TestPairsOrderAppConfigMapAfterBucket(t *testing.T) {
	pairs := gc.Pairs()
	at := slices.Index(pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindConfigMap})
	require.Equal(t, slices.Index(pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindBucket})+1, at)
	require.Equal(t, at+1, slices.Index(pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindAppRevision}))
	require.NotContains(t, pairs, gc.Pair{Owner: v1.KindApp, Child: v1.KindSecret}, "the App never owns a Secret")
}
