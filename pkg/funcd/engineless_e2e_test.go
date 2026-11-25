//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ADR-0345 on a real Function: e2e/fixtures/infrastack-reconciler.mjs reconciles the engineless kind InfraStack, so a
// stack turns Ready on its answer, and a stack with deletion: delete is held at its deletion until that Function's ok
// to a call that carried it as deleting.
func TestEnginelessKindOnARealFunction(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	require.NoError(t, err, "node serves the kind's Function")
	p, err := funcd.New(funcd.InMemory(), funcd.WithRuntimeShim(node, langmod.NodeShim(t)), funcd.WithArtifactStore(t.TempDir()),
		funcd.WithDevAdminToken())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	dev, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	admin, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)

	contract, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	ref := "oci-layout://" + t.TempDir() + ":reconciler"
	_, err = artifact.Push(ctx, ref, filepath.Join("..", "..", "e2e", "fixtures", "infrastack-reconciler.mjs"), contract, "nodejs22", "")
	require.NoError(t, err)
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "infrastack-reconciler", Namespace: "default", ResourceGroup: "rg1"}}
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image, fn.Spec.Replicas = "nodejs22", "handle", ref, 1
	_, err = dev.Apply(ctx, fn)
	require.NoError(t, err)
	addon := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon},
		ObjectMeta: v1.ObjectMeta{Name: "infrastack"}}
	addon.Spec.Runtimes = []v1.ShippedRuntime{{Name: "infrastack", Workloads: []v1.Workload{v1.WorkloadEngineless},
		Attributes: &v1.AddonAttributes{Kind: "InfraStack", Function: &v1.AddonFunction{Ref: "infrastack-reconciler"}}}}
	_, err = admin.Apply(ctx, addon)
	require.NoError(t, err)

	stack := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: "InfraStack"},
		ObjectMeta: v1.ObjectMeta{Name: "bunny-edge", Namespace: "default", ResourceGroup: "rg1"}}
	stack.Spec.Deletion, stack.Spec.Config = v1.DeletionDelete, json.RawMessage(`{"job":"bunny-edge"}`)
	require.Eventually(t, func() bool { _, err := dev.Apply(ctx, stack); return err == nil }, 10*time.Second, 50*time.Millisecond,
		"the Addon's kind is served")
	require.Eventually(t, func() bool {
		obj, err := dev.Get(ctx, "InfraStack", "default", "bunny-edge")
		if err != nil {
			return false
		}
		o := obj.(*v1.AddonObject)
		return o.Status.Phase == v1.PhaseReady && o.Status.Workload == v1.WorkloadEngineless
	}, 60*time.Second, 100*time.Millisecond, "bunny-edge turns Ready on its Function's answer")

	var held bool
	require.NoError(t, dev.Delete(ctx, "InfraStack", "default", "bunny-edge", sdk.Held(&held)))
	require.True(t, held, "the DELETE answers 202: the stack is held until its Function destroys what it holds")
	require.Eventually(t, func() bool {
		_, err := dev.Get(ctx, "InfraStack", "default", "bunny-edge")
		return fault.KindOf(err) == fault.NotFound
	}, 30*time.Second, 100*time.Millisecond, "the Function's ok to the deleting call removes it")
}
