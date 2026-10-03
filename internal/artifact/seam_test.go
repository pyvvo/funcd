package artifact_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/testkit/realshim"
)

// scenario: materializer-satisfies-adr0030-seam (node-gated) — the OrasMaterializer is
// wired into ADR-0030's Function reconciler in place of the local-file driver; the
// reconciler pulls the pushed artifact by digest from a local OCI layout and the real Node
// shim runs it to Ready. Proves the P-V-1 + P-V-A seam composes, not just type-asserts.
func TestScenarioMaterializerSatisfiesADR0030SeamNode(t *testing.T) {
	gw := seamFunction(t, "export function handle(_, e) { return { echoed: e }; }\n")

	rs, err := gw.Routes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	resp, err := http.Post(rs[0].Upstream, "application/json", strings.NewReader(`{"hello":"world"}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the shim served the oras-pulled handler over HTTP")
}

// A shim that takes longer to boot than the seam test's former fixed 5 s wait, as on a loaded CI runner, still
// reaches Ready: the wait ends on the reconciler's verdict, not on a deadline of the test's own.
func TestIssue393_SeamWaitsOutASlowShimBoot(t *testing.T) {
	seamFunction(t, "await new Promise((resolve) => setTimeout(resolve, 6000));\nexport function handle() {}\n")
}

// seamFunction pushes handler src to a local OCI layout (no registry) and brings Function "echo", pulled by digest
// through the OrasMaterializer, up to Ready on the real Node shim.
func seamFunction(t *testing.T, src string) gateway.Gateway {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte(src), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	digest, err := artifact.Push(context.Background(), ref, bundle, nil, "", "")
	require.NoError(t, err)
	return realshim.Ready(t, artifact.NewOrasMaterializer(t.TempDir(), ""), ref, digest)
}
