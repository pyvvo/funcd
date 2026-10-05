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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const ns v1.NamespaceName = "default"

func newCollector(t testing.TB, st store.Store, interval time.Duration) *gc.Collector {
	t.Helper()
	c, err := gc.New(gc.Deps{Store: st, Interval: interval})
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
	}
	return obj
}

func create(t testing.TB, st store.Store, kind v1.Kind, name v1.ObjectName, owner v1.Object) v1.Object {
	t.Helper()
	obj := object(t, kind, name)
	if owner != nil {
		om := owner.GetObjectMeta()
		obj.GetObjectMeta().OwnerReferences = []v1.OwnerReference{{
			ObjectRef: v1.ObjectRef{Kind: owner.GroupVersionKind().Kind, Namespace: om.Namespace, Name: om.Name},
			UID:       om.UID, Controller: true,
		}}
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

// fleet creates one owner and child of every pair, and returns them by child kind.
func fleet(t testing.TB, st store.Store) map[v1.Kind][2]v1.Object {
	t.Helper()
	wf := create(t, st, v1.KindWorkflow, "wf", nil)
	id := create(t, st, v1.KindIdentity, "id", nil)
	site := create(t, st, v1.KindSite, "web", nil)
	fn := create(t, st, v1.KindFunction, "fn", nil)
	return map[v1.Kind][2]v1.Object{
		v1.KindFunction: {wf, create(t, st, v1.KindFunction, "wf-s1", wf)},
		v1.KindKVStore:  {wf, create(t, st, v1.KindKVStore, "wf-state", wf)},
		v1.KindSecret:   {id, create(t, st, v1.KindSecret, "id", id)},
		v1.KindRoute:    {site, create(t, st, v1.KindRoute, "web", site)},
		v1.KindRevision: {fn, create(t, st, v1.KindRevision, "fn-1", fn)},
	}
}

// scenario: live-owner-children-never-collected — sweeps delete nothing while every owner lives.
func TestScenarioLiveOwnerChildrenNeverCollected(t *testing.T) {
	st := store.New(memory.New())
	f := fleet(t, st)
	c := newCollector(t, st, 0)
	for range 3 {
		require.NoError(t, c.CollectNamespace(context.Background(), ""))
	}
	for kind, pair := range f {
		require.True(t, exists(t, st, kind, pair[1].GetObjectMeta().Name), "%s of a live owner stays", kind)
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

	del(t, st, v1.KindWorkflow, "wf")
	del(t, st, v1.KindIdentity, "id")
	create(t, st, v1.KindIdentity, "id", nil) // a namesake with another UID
	del(t, st, v1.KindSite, "web")
	del(t, st, v1.KindFunction, "fn")

	require.NoError(t, newCollector(t, st, 0).CollectNamespace(context.Background(), ns))
	for kind, pair := range f {
		require.False(t, exists(t, st, kind, pair[1].GetObjectMeta().Name), "%s of a dead owner is collected", kind)
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
	om.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Name: "wf"}, UID: wf.GetObjectMeta().UID, Controller: true}}
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
	_, err := gc.New(gc.Deps{})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = gc.New(gc.Deps{Store: store.New(memory.New()), Interval: -time.Second})
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
	for kind, pair := range f {
		require.True(t, exists(t, st, kind, pair[1].GetObjectMeta().Name), "the start sweep keeps the %s of a live owner", kind)
	}
	del(t, st, v1.KindWorkflow, "wf")
	del(t, st, v1.KindIdentity, "id")
	del(t, st, v1.KindSite, "web")
	del(t, st, v1.KindFunction, "fn")
	require.Eventually(t, func() bool {
		for kind, pair := range f {
			if exists(t, st, kind, pair[1].GetObjectMeta().Name) {
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
	}
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
	var want []gc.Pair
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
