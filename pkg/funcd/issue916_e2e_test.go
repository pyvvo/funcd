//go:build e2e

package funcd_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Parallel callers of buildCmd share one build of a binary, which outlives the caller that started it.
func TestIssue916_ParallelBuildCmdBuildsOnce(t *testing.T) {
	bins := make([]string, 4)
	t.Run("callers", func(t *testing.T) {
		for i := range bins {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()
				bins[i] = buildCmd(t, "funcdctl")
			})
		}
	})
	for i, bin := range bins {
		require.Equal(t, bins[0], bin, "caller %d gets the one build", i)
	}
	require.FileExists(t, bins[0], "the build outlives its callers")
	cmdBuilds.mu.Lock()
	runs := cmdBuilds.runs["funcdctl"]
	cmdBuilds.mu.Unlock()
	require.Equal(t, 1, runs, "the parallel callers share one build")
}
