package scripts_test

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The lane tools read scripts/lanes.yaml with PyYAML through the dev shell's python3. When the dev shell pinned a
// Python without PyYAML, `just lima-example-all` listed no lane, ran only the metastore lane and still reported every
// lane passed. lane.py --venom-lanes lists the lanes the registry runs under Venom, from the dev shell's interpreter.
func TestLaneRegistryListsItsVenomLanes(t *testing.T) {
	raw, err := os.ReadFile("lanes.yaml")
	require.NoError(t, err)
	var registry map[string]struct {
		Venom string `yaml:"venom"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &registry))
	var want []string
	for name, spec := range registry {
		if spec.Venom != "" {
			want = append(want, name)
		}
	}
	require.NotEmpty(t, want, "scripts/lanes.yaml runs lanes under Venom")

	out, err := exec.Command("python3", "lane.py", "--venom-lanes").CombinedOutput()
	require.NoError(t, err, "the dev shell's python3 must run the lane tools: %s", out)
	got := strings.Fields(string(out))
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}
