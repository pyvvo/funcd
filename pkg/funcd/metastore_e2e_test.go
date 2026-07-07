package funcd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/store"
	bstore "github.com/green-0-rabbit/funcd/internal/store/badger"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario (e2e): durable-metastore-survives-restart — boot funcd with a FILE-mode Badger metastore
// (ADR-0065), apply a ConfigMap over the real control-plane API via the SDK, shut the platform down,
// reboot against the SAME data dir, and read the ConfigMap back. Proves the new engine persists
// control-plane state end-to-end (the engine-level test proves the driver; this proves the platform).
func TestScenarioE2EBadgerMetastorePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	// boot returns the control-plane base URL + a stop fn that shuts the platform AND closes the store
	// (the platform does not own an injected store's lifecycle, so the test releases the Badger lock).
	boot := func() (string, func()) {
		eng, err := bstore.Open(dir+"/store", bstore.WithValueLogGCInterval(0))
		require.NoError(t, err)
		st := store.New(eng)
		p, err := funcd.New(funcd.InMemory(), funcd.WithStore(st))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		addr := "http://" + p.Addr()
		stop := func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("platform Run did not return after cancel")
			}
			require.NoError(t, st.Close())
		}
		return addr, stop
	}

	// boot #1 — apply a ConfigMap through the control plane
	addr1, stop1 := boot()
	c1, err := sdk.New(addr1, sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cc := obj.(*v1.ConfigMap)
	cc.Name, cc.Namespace, cc.ResourceGroup = "persisted", "default", "rg1"
	cc.Spec.Data = map[string]string{"hello": "metastore"}
	applied, err := c1.Apply(context.Background(), cc)
	require.NoError(t, err)
	wantRV := applied.GetObjectMeta().ResourceVersion
	require.NotEmpty(t, wantRV)
	stop1()

	// boot #2 — same data dir; the resource must be recovered (crash-only) on the durable engine
	addr2, stop2 := boot()
	defer stop2()
	c2, err := sdk.New(addr2, sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	got, err := c2.Get(context.Background(), v1.KindConfigMap, "default", "persisted")
	require.NoError(t, err, "the ConfigMap survived the platform restart on the durable Badger metastore")
	require.Equal(t, wantRV, got.GetObjectMeta().ResourceVersion, "resourceVersion preserved")
	require.Equal(t, map[string]string{"hello": "metastore"}, got.(*v1.ConfigMap).Spec.Data)
}
