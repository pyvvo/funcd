package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func changeLog(t *testing.T, opts ...store.Option) (store.Store, store.ChangeLog) {
	t.Helper()
	s := store.New(memory.New(), append([]store.Option{store.WithChangeLog(0)}, opts...)...)
	cl, ok := s.(store.ChangeLog)
	require.True(t, ok)
	return s, cl
}

func all(store.Change) bool { return true }

func TestChangeLogRecordsEachWriteInOrder(t *testing.T) {
	ctx := context.Background()
	s, cl := changeLog(t)
	put(t, s, "a", "1")
	put(t, s, "a", "2")
	put(t, s, "b", "1")
	require.NoError(t, s.Delete(ctx, v1.KindConfigMap.GVK(), "default", "a", ""))

	out, cursor, err := cl.Changes(ctx, 0, 0, all)
	require.NoError(t, err)
	require.Len(t, out, 4)
	var types []store.EventType
	for i, c := range out {
		types = append(types, c.Type)
		require.Equal(t, uint64(i+1), c.Rev)
		require.Equal(t, v1.KindConfigMap, c.Kind)
	}
	require.Equal(t, []store.EventType{store.Added, store.Modified, store.Added, store.Deleted}, types)
	head, err := cl.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, head, cursor)
	var gone v1.ConfigMap
	require.NoError(t, json.Unmarshal(out[3].Object, &gone))
	require.Equal(t, "2", gone.Spec.Data["n"], "a Deleted record carries the object as it was")

	out, cursor, err = cl.Changes(ctx, 0, 1, func(c store.Change) bool { return c.Name == "b" })
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, head, cursor, "the limit was not reached, so the follower resumes after the head")

	out, cursor, err = cl.Changes(ctx, 0, 1, func(c store.Change) bool { return c.Name == "a" })
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, uint64(1), cursor, "at the limit, the follower resumes after the last record it got")
}

func TestChangeLogSkipsStatusWritesAndRecordsTheOrigin(t *testing.T) {
	ctx := context.Background()
	s, cl := changeLog(t)
	c := configMap("x", "1")
	_, err := s.Create(store.WithoutChangeLog(ctx), c)
	require.NoError(t, err)
	_, err = s.Create(store.WithOrigin(ctx, "vm1"), configMap("y", "1"))
	require.NoError(t, err)

	out, _, err := cl.Changes(ctx, 0, 0, all)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, v1.ObjectName("y"), out[0].Name)
	require.Equal(t, v1.ObjectName("vm1"), out[0].Origin)
}

type flipEncryptor struct{}

func (flipEncryptor) Encrypt(_ context.Context, p []byte) ([]byte, error) { return flip(p), nil }
func (flipEncryptor) Decrypt(_ context.Context, c []byte) ([]byte, error) { return flip(c), nil }

func flip(b []byte) []byte {
	out := bytes.Clone(b)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out
}

func TestChangeLogServesASealedKindDecrypted(t *testing.T) {
	ctx := context.Background()
	s, cl := changeLog(t, store.WithEncryptor([]v1.Kind{v1.KindSecret}, flipEncryptor{}))
	sec := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	sec.Name, sec.Namespace, sec.ResourceGroup = "token", "default", "rg1"
	sec.Spec.Data = map[string][]byte{"k": []byte("v")}
	_, err := s.Create(ctx, sec)
	require.NoError(t, err)

	out, _, err := cl.Changes(ctx, 0, 0, all)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Nil(t, out[0].Sealed)
	var got v1.Secret
	require.NoError(t, json.Unmarshal(out[0].Object, &got))
	require.Equal(t, []byte("v"), got.Spec.Data["k"])
}

func TestChangeLogCompactsAndRefusesARevisionBelowItsFloor(t *testing.T) {
	ctx := context.Background()
	s := store.New(memory.New(), store.WithChangeLog(10))
	cl := s.(store.ChangeLog)
	for i := range 1030 {
		put(t, s, "c"+strconv.Itoa(i%3), strconv.Itoa(i))
	}
	_, _, err := cl.Changes(ctx, 5, 0, all)
	require.Equal(t, fault.PreconditionFailed, fault.KindOf(err))
	out, _, err := cl.Changes(ctx, 1014, 0, all)
	require.NoError(t, err)
	require.Len(t, out, 1030-1014)
}

func TestChangeSignalClosesAtTheNextRecord(t *testing.T) {
	s, cl := changeLog(t)
	signal := cl.ChangeSignal()
	put(t, s, "a", "1")
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("the signal did not close after a write")
	}
}

func TestNoChangeLogWithoutTheOption(t *testing.T) {
	s := store.New(memory.New())
	put(t, s, "a", "1")
	out, _, err := s.(store.ChangeLog).Changes(context.Background(), 0, 0, all)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestACopyKeepsItsOriginalsIdentity(t *testing.T) {
	ctx := context.Background()
	from, to := store.New(memory.New()), store.New(memory.New())
	put(t, from, "a", "1")
	put(t, from, "a", "2")
	obj, err := from.Get(ctx, v1.KindConfigMap.GVK(), "default", "a")
	require.NoError(t, err)
	orig := *obj.GetObjectMeta()
	require.Equal(t, int64(2), orig.Generation)

	copied := obj.(*v1.ConfigMap)
	copied.GenerateName = "a-"
	got, err := to.Create(store.AsCopy(ctx), copied)
	require.NoError(t, err)
	m := got.GetObjectMeta()
	require.Equal(t, orig.Name, m.Name)
	require.Equal(t, orig.UID, m.UID)
	require.Equal(t, orig.Generation, m.Generation)
	require.Equal(t, orig.CreationTime, m.CreationTime)
	require.NotEqual(t, orig.ResourceVersion, m.ResourceVersion, "a version is its own store's")
	_, err = to.Create(store.AsCopy(ctx), copied)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a copy never takes another name")

	put(t, from, "a", "3")
	obj, err = from.Get(ctx, v1.KindConfigMap.GVK(), "default", "a")
	require.NoError(t, err)
	next := obj.(*v1.ConfigMap)
	next.ResourceVersion = m.ResourceVersion
	got, err = to.Update(store.AsCopy(ctx), next)
	require.NoError(t, err)
	require.Equal(t, int64(3), got.GetObjectMeta().Generation)

	own := got.(*v1.ConfigMap)
	own.Spec.Data = map[string]string{"n": "4"}
	own.UID, own.Generation = "forged", 99
	got, err = to.Update(ctx, own)
	require.NoError(t, err)
	require.Equal(t, orig.UID, got.GetObjectMeta().UID, "any other write keeps the stored identity")
	require.Equal(t, int64(4), got.GetObjectMeta().Generation)
}
