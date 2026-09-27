//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario (e2e): kv-counter-via-context-kv (ADR-0073/0074) — the REAL path: build the kv-counter
// handler, push it to an OCI layout, apply it, then POST. The handler reads+increments a per-name
// counter through context.kv (→ worker-node local API UDS → PDP-authorized Facade → durable driver).
// ADR-0074 makes reads DEFAULT-DENY: even with a resolved spec.kv binding AND an owned store, the
// context.kv.get is Forbidden until a Cedar read Policy permits it. Once policy.yaml is applied the
// count goes 1 then 2 across invocations — proving authorization is the PDP (a Policy), not the
// binding, and that the owner-write is the built-in forbid.
func TestScenarioE2EKVCounterViaContextKV(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := tsExample(t, "kv-counter")
	layout := t.TempDir()
	// push WITH the I/O contract (counter.schema.json baked by build.ts, ADR-0090) — the kv-counter
	// function is contract-validated exactly like fn-to-fn, so the artifact carries its schemas.
	ref, digest := pushExampleFn(t, layout, exDir, "counter")

	data, err := os.ReadFile(filepath.Join(exDir, "counter.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse counter.yaml")
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest

	// ADR-0073 ordering: owner-exists (KVStore.tables[].owner is a real Function) needs the function to
	// exist before the store; binding-validity (spec.kv names an existing store/table) needs the store to
	// exist before the function declares spec.kv. So: apply the function WITHOUT spec.kv (default-deny —
	// proves unbound is Forbidden), then the store (its owner now exists), then UPDATE the function to add
	// spec.kv (the store/table now exist) — proving the binding is the capability.
	unbound := fn
	unbound.Spec.KV = nil
	applyFnObj(t, c, &unbound)
	waitReady(t, c, "counter")

	call := func() (int, []byte) {
		resp, err := http.Post(dpURL+"/function/counter", "application/json", strings.NewReader(`{"data":{"name":"alice"}}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return -1, body
		}
		var out struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		require.NoError(t, json.Unmarshal(body, &out))
		return out.Count, body
	}

	// scenario (e2e): unbound-access-denied — with NO spec.kv binding, context.kv is Forbidden
	// (default-deny): the handler's kv call fails, so the invocation does not return a 200 count.
	count, body := call()
	require.NotEqual(t, 1, count, "without a spec.kv binding, the kv-counter call is denied (default-deny): %s", body)

	// Apply the owned store (owner "counter" exists now), then re-apply the function WITH spec.kv.
	applyKVStore(t, c, filepath.Join(exDir, "store.yaml"))
	applyFnObj(t, c, &fn)
	waitReady(t, c, "counter")

	// scenario (e2e): binding-grants-read / owner-writes 1→2 — with the spec.kv binding applied and NO read
	// Policy, the read is now PERMITTED by the binding (ADR-0076 binding-as-read-grant); the bound owner
	// reads (binding) + writes (built-in owner-forbid) and the count increments across invocations.
	got1, body1 := call()
	require.Equal(t, 1, got1, "with the spec.kv binding and NO Policy, first invoke → count 1 (the binding grants read): %s", body1)
	got2, _ := call()
	require.Equal(t, 2, got2, "second invoke → count 2 (KV persisted; binding grants read, owner writes)")

	// scenario (e2e): policy-revokes-read — a forbid Policy overrides the binding grant (operator revoke,
	// without editing spec.kv; forbid wins), so the next read is Forbidden and the invocation is rejected.
	applyPolicyObj(t, c, &v1.Policy{
		TypeMeta:   v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: "Policy"},
		ObjectMeta: v1.ObjectMeta{Name: "revoke-counter-read", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/counter", action == Action::"kv::read", resource);`},
	})
	revoked, revokedBody := call()
	require.NotEqual(t, 3, revoked, "a forbid Policy revokes the binding's read (forbid wins) — the invocation is rejected: %s", revokedBody)
}

// applyPolicyObj applies a Policy object through the control-plane client (ADR-0074/0076).
func applyPolicyObj(t *testing.T, c *sdk.Client, pol *v1.Policy) {
	t.Helper()
	_, err := c.Apply(context.Background(), pol)
	require.NoError(t, err)
}

// applyKVStore parses a KVStore manifest and applies it through the control-plane client (ADR-0073).
func applyKVStore(t *testing.T, c *sdk.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var ks v1.KVStore
	require.NoError(t, yaml.Unmarshal(data, &ks), "parse %s", path)
	_, err = c.Apply(context.Background(), &ks)
	require.NoError(t, err)
}
