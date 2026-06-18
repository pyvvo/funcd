package function

import (
	"slices"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

func poolFn(rt string) *v1.Function {
	fn := &v1.Function{}
	fn.Namespace, fn.ResourceGroup, fn.Name = "default", "rg1", "fn"
	fn.Spec.Runtime = v1.RuntimeName(rt)
	fn.Spec.Pooling.Worker = "w1"
	return fn
}

// scenario: family-selects-pool-host (gate + host selection) — a function pools IFF it opted in AND
// a pool host exists for its runtime family (ADR-0050): node via the default poolShimCommand, python
// via a WithPoolShimFor registration. A family with no host runs solo (ADR-0049 Decision 8 preserved).
func TestPoolKeyForGatesOnPoolHost(t *testing.T) {
	nodeHost := []string{"node", "/opt/funcd/pool.mjs"}
	pyHost := []string{"python3.14", "/opt/funcd/pool.py"}

	// Both hosts registered: node and python both pool, each via its own host.
	both := &Reconciler{poolShimCommand: nodeHost, poolShimsByFamily: map[string][]string{"python": pyHost}}
	if _, ok := both.poolKeyFor(poolFn("nodejs22")); !ok {
		t.Error("nodejs22 must pool when a node pool host is configured")
	}
	if _, ok := both.poolKeyFor(poolFn("python312")); !ok {
		t.Error("python312 must pool when a python pool host is configured")
	}
	if got := both.poolHostFor("nodejs22"); !slices.Equal(got, nodeHost) {
		t.Errorf("nodejs22 → node pool host, got %v", got)
	}
	if got := both.poolHostFor("python312"); !slices.Equal(got, pyHost) {
		t.Errorf("python312 → python pool host, got %v", got)
	}

	// Only the node host, but the python RUNTIME shim is registered (the real daemon config when
	// python < 3.14: a python runtime shim exists, no python pool host). python has no pool host of
	// its own and must NOT fall into the node host → solo; node still pools.
	nodeOnly := &Reconciler{
		poolShimCommand: nodeHost,
		shimByFamily:    map[string][]string{"python": {"python3", "/opt/funcd/shim.py"}},
	}
	if _, ok := nodeOnly.poolKeyFor(poolFn("nodejs22")); !ok {
		t.Error("nodejs22 must pool with the node host")
	}
	if got := nodeOnly.poolHostFor("python312"); got != nil {
		t.Errorf("python312 must NOT fall into the node pool host, got %v", got)
	}
	if _, ok := nodeOnly.poolKeyFor(poolFn("python312")); ok {
		t.Error("python312 must run SOLO when no python pool host is configured")
	}

	// Python-ONLY pool host — the production config (cmd/funcd registers WithPoolShimFor("python",…)
	// and never WithPoolShim, so poolShimCommand is empty). python must still pool (and so route to
	// the pool worker, not solo — the upstreamForFn bug the review caught); node is solo (no node host).
	pyOnly := &Reconciler{
		poolShimsByFamily: map[string][]string{"python": pyHost},
		shimByFamily:      map[string][]string{"python": {"python3", "/opt/funcd/shim.py"}},
	}
	if _, ok := pyOnly.poolKeyFor(poolFn("python312")); !ok {
		t.Error("python312 must pool with a python-only pool host (no node host configured)")
	}
	if _, ok := pyOnly.poolKeyFor(poolFn("nodejs22")); ok {
		t.Error("nodejs22 must be solo when only a python pool host is configured")
	}

	// No pool host at all: everything solo.
	none := &Reconciler{}
	if _, ok := none.poolKeyFor(poolFn("nodejs22")); ok {
		t.Error("no pool host → every function solo")
	}

	// Opt-out (no worker id) is solo regardless of hosts.
	noWorker := poolFn("nodejs22")
	noWorker.Spec.Pooling.Worker = ""
	if _, ok := both.poolKeyFor(noWorker); ok {
		t.Error("no worker id → solo")
	}
}
