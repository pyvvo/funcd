package funcd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func TestANodesBackupLeavesItsSecretsOut(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	for _, k := range []v1.Kind{v1.KindSecret, v1.KindConfigMap} {
		obj, ok := v1.NewObject(k)
		require.True(t, ok)
		m := obj.GetObjectMeta()
		m.Name, m.Namespace, m.ResourceGroup = "app", "e1", "rg1"
		if s, ok := obj.(*v1.Secret); ok {
			s.Spec.Data = map[string][]byte{"token": []byte("s3cr3t")}
		}
		_, err := st.Create(ctx, obj)
		require.NoError(t, err)
	}
	var all, node [][]byte
	_, err := st.Snapshot(ctx, func(r snapshot.Record) error { all = append(all, r.Key); return nil })
	require.NoError(t, err)
	_, err = withoutSecrets{st}.Snapshot(ctx, func(r snapshot.Record) error { node = append(node, r.Key); return nil })
	require.NoError(t, err)
	has := func(keys [][]byte, k v1.Kind) bool {
		for _, key := range keys {
			if bytes.HasPrefix(key, []byte(k.GVK().String()+"\x00")) {
				return true
			}
		}
		return false
	}
	require.True(t, has(all, v1.KindSecret))
	require.False(t, has(node, v1.KindSecret), "a node's backup holds no Secret")
	require.True(t, has(node, v1.KindConfigMap), "and every other object")
	require.Len(t, node, len(all)-1)
}
