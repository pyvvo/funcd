package funcd_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// laneSpec is the part of a scripts/lanes.yaml section this test reads (ADR-0077 registry).
type laneSpec struct {
	Module string       `json:"module"`
	Dir    string       `json:"dir"`
	Build  []string     `json:"build"`
	Stage  []stageEntry `json:"stage"`
	Push   []struct {
		Artifact string `json:"artifact"`
		Site     bool   `json:"site"`
	} `json:"push"`
}

// stageEntry is a lane stage item: a path relative to the lane's dir, or {from, module, to}.
type stageEntry struct {
	Path   string
	From   string `json:"from"`
	Module string `json:"module"`
	To     string `json:"to"`
}

func (e *stageEntry) UnmarshalJSON(data []byte) error {
	if json.Unmarshal(data, &e.Path) == nil {
		return nil
	}
	type plain stageEntry
	return json.Unmarshal(data, (*plain)(e))
}

// scenario: examples-build-with-the-tools (ADR-0144, ADR-0247) — the pinned language modules' examples carry no build
// script, no schema file and no funcdctl.yaml (the contract lives in the contract file), and every lane push of a file
// finds its contract file staged beside it, where `funcdctl push` reads it; a bundle directory is built by
// funcd-bundle, which puts the contract file at its root.
func TestScenarioExamplesBuildWithTheTools(t *testing.T) {
	for _, mod := range []string{langmod.TypeScript, langmod.Python} {
		root := filepath.Join(langmod.Dir(t, mod), "examples")
		require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "dist" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir // local build output and tool caches (a go.work clone), never in a module
			}
			if d.IsDir() && strings.HasPrefix(rel, "releve-lakehouse") {
				return filepath.SkipDir // it keeps its build until it has a uv.lock (ADR-0144)
			}
			name := d.Name()
			require.False(t, name == "build.ts" || name == "build.py" || strings.HasSuffix(name, ".schema.json"),
				"%s/examples/%s: examples build with the toolchain plugins and keep the contract in a contract file", mod, rel)
			require.False(t, strings.HasSuffix(name, "funcdctl.yaml"), "%s/examples/%s: funcdctl.yaml is read nowhere (ADR-0247)", mod, rel)
			return nil
		}))
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "lanes.yaml"))
	require.NoError(t, err)
	var lanes map[string]laneSpec
	require.NoError(t, yaml.Unmarshal(data, &lanes))
	for name, lane := range lanes {
		staged := stagedFiles(t, lane)
		for _, push := range lane.Push {
			if push.Site {
				continue
			}
			dir, file := path.Split(push.Artifact)
			ext := path.Ext(file)
			if ext == "" {
				// a built bundle directory: the lane's build writes it, funcd-bundle puts its contract file inside
				require.Contains(t, strings.Join(lane.Build, " "), "funcd-bundle", "lane %s builds %s", name, push.Artifact)
				continue
			}
			stem := strings.TrimSuffix(file, ext)
			yamlSrc, jsonSrc := staged[dir+stem+".contract.yaml"], staged[dir+stem+".contract.json"]
			require.True(t, (yamlSrc != "") != (jsonSrc != ""),
				"lane %s stages no single contract file beside %s (%s.contract.yaml or .json)", name, push.Artifact, stem)
			src := yamlSrc + jsonSrc
			_, serr := os.Stat(src)
			require.NoError(t, serr, "lane %s stages the contract file of %s from %s", name, push.Artifact, src)
		}
	}
}

// stagedFiles maps each staged layout path of a lane to its source file in the pinned module (or funcd).
func stagedFiles(t *testing.T, lane laneSpec) map[string]string {
	t.Helper()
	moduleRoot := func(mod string) string {
		if mod == "" {
			return filepath.Join("..", "..")
		}
		return langmod.Dir(t, mod)
	}
	out := map[string]string{}
	for _, e := range lane.Stage {
		if e.Path != "" {
			out[e.Path] = filepath.Join(moduleRoot(lane.Module), lane.Dir, e.Path)
			continue
		}
		out[e.To] = filepath.Join(moduleRoot(e.Module), e.From)
	}
	return out
}
