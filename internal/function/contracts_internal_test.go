package function

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ContractOf takes the contract of the revision that serves a call (ADR-0303 Decision 2): a pinned step's revision,
// else a pooled Function's current revision, else its serving revision.
func TestContractOfPicksTheRevisionThatServes(t *testing.T) {
	t.Parallel()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New(nil)
	t.Cleanup(func() { _ = rt.Close() })
	st := store.New(memory.New())
	r, err := NewReconciler(Deps{Store: st, Runtime: rt, Scheduler: sch, Gateway: embedded.New(), Validator: NewBasicValidator(),
		Materializer: NewFileMaterializer(), ShimCommand: []string{"node", "shim.mjs"}})
	require.NoError(t, err)
	for rev, field := range map[v1.ObjectName]string{"fn-1": "a", "fn-2": "b"} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.mjs"), []byte("export function handle() {}\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"),
			[]byte(`{"input": {"type": "object", "required": ["`+field+`"]}, "output": {}}`), 0o600))
		snap := &v1.Revision{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRevision.GVK().APIVersion(), Kind: v1.KindRevision}}
		snap.Name, snap.Namespace, snap.ResourceGroup = rev, "default", "rg1"
		snap.Spec.Function, snap.Spec.Runtime, snap.Spec.Handler = v1.ObjectRef{Name: "fn"}, "nodejs22", "handle"
		snap.Spec.Number, snap.Spec.Image = map[v1.ObjectName]int64{"fn-1": 1, "fn-2": 2}[rev], "file://"+filepath.Join(dir, "handler.mjs")
		_, err := st.Create(context.Background(), snap)
		require.NoError(t, err)
	}
	fn := &v1.Function{}
	fn.Name, fn.Namespace = "fn", "default"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Status.ServingRevision, fn.Status.CurrentRevision = "fn-1", "fn-2"

	requires := func(ref activator.FunctionRef, want string) {
		t.Helper()
		pair, err := r.ContractOf(context.Background(), ref, fn)
		require.NoError(t, err)
		require.NotNil(t, pair)
		d, err := pair.Input.Validate([]byte(`{"` + want + `": 1}`))
		require.NoError(t, err)
		require.Empty(t, d, "the contract that requires %q", want)
	}
	ref := activator.FunctionRef{Namespace: "default", Name: "fn"}
	requires(ref, "a") // solo: the serving revision
	fn.Status.Pool = "p1"
	requires(ref, "b") // pooled: the current revision, the one the pool runs
	ref.Revision = "fn-1"
	requires(ref, "a") // a pinned step: its own revision
}
