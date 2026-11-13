package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/funcdenv"
)

// TestUsagePageConfigParses holds every funcdconfig.yaml example of the funcd Usage page to what funcd accepts, so the
// page cannot drift from the config struct.
func TestUsagePageConfigParses(t *testing.T) {
	funcdenv.Clear(t)
	page, err := os.ReadFile("../../docs/guide/develop/running-funcd/index.md")
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)\n```").FindAllSubmatch(page, -1)
	require.NotEmpty(t, blocks, "the page shows no configuration example")
	for _, block := range blocks {
		_, err := config.Parse("the funcd Usage page", block[1], config.Flags{})
		require.NoError(t, err, "%s", block[1])
	}
}
