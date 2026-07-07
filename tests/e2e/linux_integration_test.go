//go:build linux && integration

package e2e_test

import "testing"

// scenario: linux-integration-deploy-invoke (DEFERRED — ADR-0025 L4).
//
// The full exit-criterion walk, on a Linux runner with a container runtime:
//  1. funcd.New(funcd.Production(), WithBlob(file), WithBus(file), WithRuntime(containerd/crun), WithStore(badger))
//  2. funcdctl / SDK apply a JS or Python source artifact (no Dockerfile, no registry)
//  3. the controller reconciles it to Ready and a gateway route is programmed
//  4. invoke over HTTP (a CloudEvent) and by a timer EventSource
//  5. the handler reads a Secret and persists state in the KV service through the SDK
//  6. idle → scale-to-zero; a new request wakes it on demand
//
// Deferred until its prerequisites land: the curated runtime shim (F12/F13) and the
// facade→control-plane wiring (the ADR-0014 "later seam"). Built only under
// `//go:build linux && integration` (so `just ci` on macOS skips it); run via
// `just test-integration` on a Linux runner.
func TestLinuxIntegrationDeployInvoke(t *testing.T) {
	t.Skip("L4 deferred: needs the curated runtime shim (F12/F13) + the facade→control-plane wiring (ADR-0014 seam)")
}
