package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func TestALakeTokenVerifiesOnlyUnderItsNamespacesMaster(t *testing.T) {
	masters := map[v1.NamespaceName][]byte{"e1": []byte("the master of e1"), "e2": []byte("the master of e2")}
	keys := NewNamespaceLakeKeys([]byte("the node's own master"), func(ns v1.NamespaceName) ([]byte, error) {
		if m, ok := masters[ns]; ok {
			return m, nil
		}
		return nil, fault.Unavailablef("test", "no master for namespace %s", ns)
	}, nil)
	token := func(master string, ns v1.NamespaceName) string {
		tok, err := DeriveLakeToken([]byte(master), ns, v1.KindFunction, "fn")
		require.NoError(t, err)
		return tok
	}
	ref, ok := keys.PrincipalFor(token("the master of e1", "e1"))
	require.True(t, ok)
	require.Equal(t, v1.NamespaceName("e1"), ref.Namespace)
	_, ok = keys.PrincipalFor(token("the master of e2", "e1"))
	require.False(t, ok, "another namespace's master does not sign for e1")
	_, ok = keys.PrincipalFor(token("the node's own master", "e1"))
	require.False(t, ok, "nor does the node's own")
	_, ok = keys.PrincipalFor(token("the node's own master", "e3"))
	require.False(t, ok, "a namespace with no master on the node has no tokens")
}
