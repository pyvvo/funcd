package s3gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// onlyE1 is a worker node's namespace masters: it runs namespace e1 alone (FEAT-0002 decision 8).
func onlyE1(ns v1.NamespaceName) ([]byte, error) {
	if ns == "e1" {
		return []byte("the master of e1"), nil
	}
	return nil, fault.Unavailablef("test", "no master for namespace %s", ns)
}

func TestAWorkerNodeDerivesEachNamespacesPairsFromItsOwnMaster(t *testing.T) {
	d := Deriver{Master: []byte("the node's own master"), NamespaceMasters: onlyE1}
	kp, err := d.KeypairOf(v1.KindFunction, "e1", "fn")
	require.NoError(t, err)
	require.Equal(t, DeriveKeypair([]byte("the master of e1"), v1.KindFunction, "e1", "fn"), kp)
	_, _, _, secret, ok := d.Secret(kp.AccessKey)
	require.True(t, ok)
	require.Equal(t, kp.SecretKey, secret)

	_, err = d.KeypairOf(v1.KindFunction, "e2", "fn")
	require.Error(t, err, "no pair for a namespace whose master the node lacks")
	_, _, _, _, ok = d.Secret(DeriveKeypair([]byte("the node's own master"), v1.KindFunction, "e2", "fn").AccessKey)
	require.False(t, ok, "nor does the gateway take one")

	now := func() time.Time { return time.Unix(1_800_000_000, 0) }
	node := Deriver{Master: []byte("the node's own master"), NamespaceMasters: onlyE1, Salt: []byte("salt"), Period: time.Hour, Now: now}
	single := Deriver{Master: []byte("the master of e1"), Salt: []byte("salt"), Period: time.Hour, Now: now}
	got, err := node.KeypairOf(v1.KindLake, "e1", "lake")
	require.NoError(t, err)
	require.Equal(t, single.Keypair(v1.KindLake, "e1", "lake"), got, "a rotating pair derives the same way")
}
