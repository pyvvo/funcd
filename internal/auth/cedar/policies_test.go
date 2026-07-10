package cedar

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// mutableSource is a PolicySource whose revision (and policy slice) the test controls, counting how many
// times the cache polls it — so the test can prove the compile only re-runs on a revision change.
type mutableSource struct {
	rev   string
	pols  []v1.Policy
	calls int
}

func (m *mutableSource) Policies(context.Context) ([]v1.Policy, string, error) {
	m.calls++
	return m.pols, m.rev, nil
}

// scenario: policy-cache-atomic-revision-swap — the compiled PolicySet is held behind an atomic pointer
// (ADR-0117 §4a): an unchanged source revision returns the SAME cached PolicySet (no rebuild — a
// lock-free load), while a revision change swaps in a freshly compiled set. The source is polled every
// Get (the revision-detection contract), but compile() runs only when the revision actually moves.
func TestScenarioPolicyCacheAtomicSwap(t *testing.T) {
	t.Parallel()
	src := &mutableSource{rev: "1"}
	c := &policyCache{src: src}

	ps1, err := c.Get(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ps1)

	ps2, err := c.Get(context.Background())
	require.NoError(t, err)
	require.Same(t, ps1, ps2, "same revision ⇒ the cached PolicySet is returned (no rebuild)")

	src.rev = "2" // a Policy/EgressPolicy/Function write bumped the store revision
	ps3, err := c.Get(context.Background())
	require.NoError(t, err)
	require.NotSame(t, ps1, ps3, "a revision change ⇒ a freshly compiled PolicySet (atomic swap)")

	require.Equal(t, 3, src.calls, "the source is polled on every Get (cheap revision detection)")
}
