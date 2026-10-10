package restore_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/restore"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

const (
	t1 = "aaaaaaaaaaaaaaaa"
	t2 = "bbbbbbbbbbbbbbbb"
	t3 = "cccccccccccccccc"
)

func TestParsePoint(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want restore.Point
	}{
		{"latest", restore.Point{Latest: true}},
		{"pre-upgrade", restore.Point{Pin: backup.PreUpgrade}},
		{"verified", restore.Point{Pin: backup.Verified}},
		{"2026-10-08T12:00:00Z", restore.Point{At: at}},
		{t1 + "/40", restore.Point{Gen: &backup.GenRef{Timeline: t1, Generation: 40}}},
		{t1 + "-150", restore.Point{Version: store.Version{Timeline: t1, N: 150}}},
	} {
		got, err := restore.ParsePoint(c.in)
		require.NoError(t, err, c.in)
		require.Equal(t, c.want, got, c.in)
	}
	for _, bad := range []string{"", "newest", "150", t1 + "/x", "XYZ/4", "hourly"} {
		_, err := restore.ParsePoint(bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
	}
}

func gen(tl string, n, rev uint64, at time.Time, parent *backup.GenRef) restore.Generation {
	return restore.Generation{Class: backup.Hourly, State: restore.StateComplete, Manifest: backup.Manifest{
		Format: backup.Format, Generation: n, Timeline: tl, Revision: rev, At: v1.NewTimestamp(at), Parent: parent}}
}

// Two timelines that both qualify and do not descend from each other are a Conflict until --timeline names one;
// an abandoned or incomplete generation never resolves latest.
func TestResolve(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	gens := []restore.Generation{
		gen(t1, 40, 100, t0, nil),
		gen(t2, 43, 20, t0.Add(2*time.Hour), &backup.GenRef{Timeline: t1, Generation: 40}),
		gen(t3, 44, 7, t0.Add(3*time.Hour), nil),
	}
	_, err := restore.Resolve(gens, restore.Point{Latest: true})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, t3)

	g, err := restore.Resolve(gens, restore.Point{Latest: true, Timeline: t2})
	require.NoError(t, err)
	require.Equal(t, backup.GenRef{Timeline: t2, Generation: 43}, g.Ref())
	g, err = restore.Resolve(gens, restore.Point{At: t0.Add(time.Hour), Timeline: t2})
	require.NoError(t, err)
	require.Equal(t, backup.GenRef{Timeline: t1, Generation: 40}, g.Ref(), "a timeline keeps the ones it descends from")

	gens[2].State = restore.StateAbandoned
	g, err = restore.Resolve(gens, restore.Point{Latest: true})
	require.NoError(t, err)
	require.Equal(t, uint64(43), g.Manifest.Generation)
	_, err = restore.Resolve(gens, restore.Point{Pin: backup.PreUpgrade})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, err = restore.Resolve(gens, restore.Point{Gen: &backup.GenRef{Timeline: t1, Generation: 41}})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

func TestCheckVersion(t *testing.T) {
	for _, c := range []struct {
		writer, binary string
		invalid        bool
	}{
		{"v0.8.0", "v0.8.3", false},
		{"v0.7.9", "v0.8.0", false},
		{"v0.9.0", "v0.8.3", true},
		{"v0.9.0-9-gabc1234-dirty", "v0.8.3", true},
		{"v0.8.4-2-gabc1234", "v0.8.0", false},
		{"dev", "v0.8.0", false},
		{"v0.9.0", "dev", false},
		{"abc1234", "v0.8.0", false},
		{"v0.9.0", "abc1234-dirty", false},
	} {
		err := restore.CheckVersion(c.writer, c.binary)
		if !c.invalid {
			require.NoError(t, err, "%s under %s", c.writer, c.binary)
			continue
		}
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, c.writer)
		require.ErrorContains(t, err, c.binary)
	}
}

func TestExportRedacts(t *testing.T) {
	sec := &v1.Secret{ObjectMeta: v1.ObjectMeta{Namespace: "a", Name: "s", UID: "u1", ResourceVersion: t1 + "-4",
		CreationTime: v1.NewTimestamp(time.Now())}, Spec: v1.SecretSpec{Data: map[string][]byte{"token": []byte("hunter2")}}}
	out, err := restore.Export(sec, false)
	require.NoError(t, err)
	s := out.(*v1.Secret)
	require.Equal(t, []byte("REDACTED"), s.Spec.Data["token"])
	require.Empty(t, s.UID)
	require.Empty(t, s.ResourceVersion)
	require.True(t, s.CreationTime.IsZero())
	require.Equal(t, v1.KindSecret, s.Kind)
	require.Equal(t, []byte("hunter2"), sec.Spec.Data["token"], "the source is not changed")
	out, err = restore.Export(sec, true)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), out.(*v1.Secret).Spec.Data["token"])

	fn := &v1.Function{ObjectMeta: v1.ObjectMeta{Namespace: "a", Name: "f", Generation: 3}}
	fn.Status.Phase = v1.PhaseReady
	out, err = restore.Export(fn, false)
	require.NoError(t, err)
	require.Empty(t, out.(*v1.Function).Status.Phase)
}

// source is a metastore, run store and event store in memory, cut into generations on a file:// target.
type source struct {
	meta   store.Store
	runs   runstate.Store
	events *eventstore.Store
	target backup.Target
	src    blob.Bucket
	keys   backup.Keys
}

func newSource(t *testing.T, key []byte) *source {
	t.Helper()
	ctx := context.Background()
	s := &source{}
	var opts []store.Option
	if key != nil {
		enc, err := aesgcm.NewAESEncryptor(key)
		require.NoError(t, err)
		opts = append(opts, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
		s.keys.SecretsKey = envelope.Fingerprint(key)
	}
	s.meta = store.New(memory.New(), opts...)
	var err error
	s.runs, err = runbadger.New(runbadger.Config{InMemory: true})
	require.NoError(t, err)
	s.events, err = eventstore.Open(eventstore.Config{InMemory: true})
	require.NoError(t, err)
	dir := t.TempDir()
	s.target, err = backup.Open(ctx, backup.Config{Target: gocloud.FileURL(dir), Retention: backup.Retention{Hourly: 1},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	s.src, err = gocloud.Open(ctx, gocloud.FileURL(dir))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.meta.Close()
		_ = s.runs.Close()
		_ = s.events.Close()
		_ = s.target.Close()
		_ = s.src.Close()
	})
	return s
}

func (s *source) cut(t *testing.T) backup.Manifest {
	t.Helper()
	m, err := s.target.Write(context.Background(), s.events, s.meta, s.runs, backup.WriteOptions{Keys: s.keys})
	require.NoError(t, err)
	return m
}

func (s *source) put(t *testing.T, obj v1.Object) {
	t.Helper()
	ctx := context.Background()
	cur, err := s.meta.Get(ctx, obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
	if fault.KindOf(err) == fault.NotFound {
		_, err = s.meta.Create(ctx, obj)
		require.NoError(t, err)
		return
	}
	require.NoError(t, err)
	obj.GetObjectMeta().ResourceVersion = cur.GetObjectMeta().ResourceVersion
	_, err = s.meta.Update(ctx, obj)
	require.NoError(t, err)
}

func tm(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

func configMap(name, value string) *v1.ConfigMap {
	return &v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap), ObjectMeta: v1.ObjectMeta{Namespace: "a", ResourceGroup: "rg", Name: v1.ObjectName(name)},
		Spec: v1.ConfigMapSpec{Data: map[string]string{"v": value}}}
}

func secret(name, value string) *v1.Secret {
	return &v1.Secret{TypeMeta: tm(v1.KindSecret), ObjectMeta: v1.ObjectMeta{Namespace: "a", ResourceGroup: "rg", Name: v1.ObjectName(name)},
		Spec: v1.SecretSpec{Data: map[string][]byte{"v": []byte(value)}}}
}

func refs(names ...string) []v1.ObjectRef {
	var out []v1.ObjectRef
	for _, n := range names {
		kind, name, _ := strings.Cut(n, "/")
		out = append(out, v1.ObjectRef{Kind: v1.Kind(kind), Namespace: "a", Name: v1.ObjectName(name)})
	}
	return out
}

// Inspect loads a generation into memory engines; without the secrets key Secrets list by key and diff by key only,
// with it a changed value shows; a key of another fingerprint is refused.
func TestInspectDiff(t *testing.T) {
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	s := newSource(t, key)
	s.put(t, &v1.Namespace{TypeMeta: tm(v1.KindNamespace), ObjectMeta: v1.ObjectMeta{Name: "a"}})
	s.put(t, configMap("keep", "1"))
	s.put(t, configMap("drop", "1"))
	s.put(t, secret("s", "one"))
	first := s.cut(t)
	s.put(t, configMap("keep", "2"))
	require.NoError(t, s.meta.Delete(ctx, v1.KindConfigMap.GVK(), "a", "drop", ""))
	s.put(t, configMap("new", "1"))
	s.put(t, secret("s", "two"))
	s.put(t, secret("u", "one"))
	second := s.cut(t)

	gens, err := restore.List(ctx, s.src)
	require.NoError(t, err)
	inspect := func(m backup.Manifest, k []byte) *restore.View {
		g, err := restore.Resolve(gens, restore.Point{Gen: &backup.GenRef{Timeline: m.Timeline, Generation: m.Generation}})
		require.NoError(t, err)
		v, err := restore.Inspect(ctx, g, restore.Options{Source: s.src, SecretsKey: k})
		require.NoError(t, err)
		t.Cleanup(func() { _ = v.Close() })
		return v
	}
	from, to := inspect(first, nil), inspect(second, nil)
	require.Equal(t, refs("Secret/s", "Secret/u"), to.Secrets)
	_, err = to.Meta.List(ctx, v1.KindSecret.GVK(), store.ListOptions{})
	require.Error(t, err, "Secrets do not decode without the key")
	added, removed, changed, err := restore.Diff(from, to)
	require.NoError(t, err)
	require.Equal(t, refs("ConfigMap/new"), added[v1.KindConfigMap])
	require.Equal(t, refs("Secret/u"), added[v1.KindSecret])
	require.Equal(t, refs("ConfigMap/drop"), removed[v1.KindConfigMap])
	require.Equal(t, refs("ConfigMap/keep"), changed[v1.KindConfigMap])
	require.Empty(t, changed[v1.KindSecret])

	_, _, changed, err = restore.Diff(inspect(first, key), inspect(second, key))
	require.NoError(t, err)
	require.Equal(t, refs("Secret/s"), changed[v1.KindSecret])

	g, err := restore.Resolve(gens, restore.Point{Latest: true})
	require.NoError(t, err)
	_, err = restore.Inspect(ctx, g, restore.Options{Source: s.src, SecretsKey: bytes.Repeat([]byte{8}, 32)})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
