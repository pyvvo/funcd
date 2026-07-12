//go:build dev

package devengine

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/green-0-rabbit/funcd/internal/catalog/embedengine"
	"github.com/green-0-rabbit/funcd/internal/provider"
)

func testSpec() provider.ProviderSpec {
	return provider.ProviderSpec{
		Ref:      provider.ProviderRef{Namespace: "default", Name: "lake"},
		Image:    "funcd/runtime-duckdb",
		Port:     8080,
		Replicas: 1,
		Env:      map[string]string{"FUNCD_DUCKLAKE_CATALOG": "s3://dev/lake"},
	}
}

// scenario: dev-catalog-query (contract half, ADR-0125 M2) — a dev binary built WITHOUT the engine
// (the committed placeholder) reports catalog-unavailable at Converge gracefully (not-Ready, no
// error), so `funcdctl dev` still boots. This runs everywhere; the live query through the real
// engine is the deferred lane.
func TestConvergeNotBundledReportsUnavailable(t *testing.T) {
	if embedengine.Bundled() {
		t.Skip("a real engine is embedded; this case asserts the placeholder path")
	}
	st, err := New(nil).Converge(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Converge (placeholder): unexpected error %v", err)
	}
	if st.Ready {
		t.Fatal("Converge (placeholder): Ready=true, want false")
	}
	if st.Reason != "CatalogEngineNotBundled" {
		t.Fatalf("Converge (placeholder): Reason=%q, want CatalogEngineNotBundled", st.Reason)
	}
}

// scenario: dev-catalog-query — with the real engine embedded (just build-catalog-engine), Converge
// extracts it, launches duckdb serving Quack, and reports Ready with a reachable Address; a second
// Converge is idempotent; Teardown stops it. Skipped where only the placeholder is embedded (CI) —
// the same gating the other engine-dependent lanes use. No docker, no network: purely the embedded
// subprocess.
func TestConvergeLaunchesEngineWhenBundled(t *testing.T) {
	if !embedengine.Bundled() {
		t.Skip("no real engine embedded (placeholder); run `just build-catalog-engine <os> <arch>` first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	r := New(nil)
	spec := testSpec()

	st, err := r.Converge(ctx, spec)
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !st.Ready || st.Address == "" {
		t.Fatalf("Converge: Ready=%v Address=%q, want Ready with an address", st.Ready, st.Address)
	}
	conn, derr := net.DialTimeout("tcp", st.Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("Quack engine not reachable at %s: %v", st.Address, derr)
	}
	_ = conn.Close()

	// Idempotent: a second Converge returns the same running engine, not a new one.
	st2, err2 := r.Converge(ctx, spec)
	if err2 != nil || st2.Address != st.Address {
		t.Fatalf("Converge (2nd): err=%v addr=%q, want the same addr %q", err2, st2.Address, st.Address)
	}

	if terr := r.Teardown(ctx, spec.Ref); terr != nil {
		t.Fatalf("Teardown: %v", terr)
	}
	time.Sleep(300 * time.Millisecond)
	if c, e := net.DialTimeout("tcp", st.Address, 500*time.Millisecond); e == nil {
		_ = c.Close()
		t.Fatalf("engine still reachable at %s after Teardown", st.Address)
	}
}
