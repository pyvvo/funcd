package langmod

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// A pin names a release tag, never a pseudo-version or a pre-release (ADR-0141 D2).
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// scenario: bump-language-release (ADR-0141) — go.mod requires both language modules directly, at
// release tags. go.mod is read as written, so a local go.work override does not mask a bad pin.
func TestPinsAreReleaseTags(t *testing.T) {
	out, err := exec.Command("go", "mod", "edit", "-json").Output()
	require.NoError(t, err)
	var mod struct {
		Require []struct {
			Path     string
			Version  string
			Indirect bool
		}
	}
	require.NoError(t, json.Unmarshal(out, &mod))
	pinned := map[string]bool{}
	for _, r := range mod.Require {
		if r.Path != TypeScript && r.Path != Python {
			continue
		}
		pinned[r.Path] = true
		require.Regexp(t, releaseTag, r.Version, "%s is pinned to a release tag", r.Path)
		require.False(t, r.Indirect, "%s is a direct requirement", r.Path)
	}
	require.True(t, pinned[TypeScript] && pinned[Python], "go.mod pins both language modules")
}

type lane struct {
	Module string       `json:"module"`
	Dir    string       `json:"dir"`
	Build  []string     `json:"build"`
	Stage  []stageEntry `json:"stage"`
	Venom  string       `json:"venom"`
}

// stageEntry is a lanes.yaml stage item: a path under the lane's dir, or a {from, module, to} copy.
type stageEntry struct {
	Path   string
	From   string `json:"from"`
	Module string `json:"module"`
	To     string `json:"to"`
}

func (e *stageEntry) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &e.Path)
	}
	type plain stageEntry
	return json.Unmarshal(b, (*plain)(e))
}

// scenario: lane-reads-committed-build (ADR-0141, static half) — every lane's dir exists in its pinned
// module, a build-less lane stages only committed files, every `from` file exists, and every Venom
// suite exists. The live half is `just lima-example <name>`.
func TestScenarioLaneStagesResolve(t *testing.T) {
	funcdRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(funcdRoot, "scripts", "lanes.yaml"))
	require.NoError(t, err)
	var lanes map[string]lane
	require.NoError(t, yaml.Unmarshal(data, &lanes))
	require.NotEmpty(t, lanes)

	roots := map[string]string{"": funcdRoot}
	root := func(mod string) string {
		if r, ok := roots[mod]; ok {
			return r
		}
		roots[mod] = Dir(t, mod)
		return roots[mod]
	}
	exists := func(p, what string) {
		_, serr := os.Stat(p)
		require.NoError(t, serr, what)
	}
	for name, l := range lanes {
		dir := filepath.Join(root(l.Module), l.Dir)
		require.DirExists(t, dir, "lane %s: dir", name)
		require.FileExists(t, filepath.Join(funcdRoot, l.Venom), "lane %s: venom suite", name)
		for _, e := range l.Stage {
			switch {
			case e.From != "":
				exists(filepath.Join(root(e.Module), e.From), "lane "+name+": from "+e.From)
			case len(l.Build) == 0:
				exists(filepath.Join(dir, e.Path), "lane "+name+": committed "+e.Path)
			}
		}
	}
}
