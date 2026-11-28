package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/secrets"
	secretsvc "github.com/pyvvo/funcd/internal/services/secret"
	"github.com/pyvvo/funcd/internal/store"
)

// openResolver allows every Secret read and write.
type openResolver struct{}

func (openResolver) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (openResolver) CheckRead(context.Context, auth.Identity, v1.NamespaceName, string) error {
	return nil
}
func (openResolver) CheckWrite(context.Context, auth.Identity, v1.NamespaceName, string) error {
	return nil
}

func TestAWorkloadsSecretWriteGoesToTheControlPlane(t *testing.T) {
	ctx := context.Background()
	n := joinNode(t, func(c *multinode.Config) {
		f, err := secretsvc.NewFacade(secretsvc.Deps{Store: c.Store, Resolver: openResolver{}})
		require.NoError(t, err)
		c.Secrets = f
	})
	defer n.start(t)()
	sec := &v1.Secret{TypeMeta: meta(v1.KindSecret)}
	sec.Name, sec.Namespace, sec.ResourceGroup = "oauth", "e1", "rg1"
	sec.Spec.Data = map[string][]byte{"TOKEN": []byte("t1")}
	_, err := n.cp.Create(ctx, sec)
	require.NoError(t, err)
	j := job("refresher")
	j.Spec.Bindings.Secret = []v1.ObjectName{"oauth"}
	_, err = n.cp.Create(ctx, j)
	require.NoError(t, err)
	onNode(t, n, v1.KindJob, "refresher", "the Job reaches the node", anyJob)

	facade, err := secretsvc.NewFacade(secretsvc.Deps{Store: n.node, Resolver: openResolver{}, Forward: n.agent.WriteSecret})
	require.NoError(t, err)
	fn := v1.JobOwner("refresher")
	require.NoError(t, facade.Put(ctx, "e1", fn, "oauth", "TOKEN", "t2"))
	got, err := n.node.Get(ctx, v1.KindSecret.GVK(), "e1", "oauth")
	require.NoError(t, err)
	require.Equal(t, "t2", string(got.(*v1.Secret).Spec.Data["TOKEN"]), "the workload reads its own write")
	got, err = n.cp.Get(ctx, v1.KindSecret.GVK(), "e1", "oauth")
	require.NoError(t, err)
	require.Equal(t, "t2", string(got.(*v1.Secret).Spec.Data["TOKEN"]), "the control plane holds it")
	require.NoError(t, facade.Delete(ctx, "e1", fn, "oauth", "TOKEN"))
	got, err = n.cp.Get(ctx, v1.KindSecret.GVK(), "e1", "oauth")
	require.NoError(t, err)
	require.Empty(t, got.(*v1.Secret).Spec.Data)

	// The control plane makes the checks again, so its refusal keeps its kind.
	require.NoError(t, n.cp.Delete(store.WithoutChangeLog(ctx), v1.KindSecret.GVK(), "e1", "oauth", ""))
	err = facade.Put(ctx, "e1", fn, "oauth", "TOKEN", "t3")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)
	err = n.agent.WriteSecret(ctx, "elsewhere", fn, "oauth", "TOKEN", nil)
	require.ErrorContains(t, err, "not placed on worker node vm1")

	n.cancelCP()
	require.NoError(t, n.cluster.Close())
	err = facade.Put(ctx, "e1", fn, "oauth", "TOKEN", "t4")
	require.ErrorIs(t, err, secrets.ErrControlPlaneAway)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
}
