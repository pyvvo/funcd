package funcd_test

import (
	"encoding/json"
	"fmt"
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
// script, no schema file and no funcdctl.yaml (the contract lives in the contract file), and every lane push finds its
// contract file where `funcdctl push` reads it (pushContract).
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

	for name, lane := range readLanes(t) {
		if len(lane.Push) == 0 {
			continue // a lane that pushes nothing has no contract file to find
		}
		staged := stagedFiles(t, lane)
		for _, push := range lane.Push {
			if push.Site {
				continue
			}
			require.NoError(t, pushContract(lane, staged, push.Artifact), "lane %s", name)
		}
	}
}

// pushContract reports whether `funcdctl push` finds the contract file of a lane's pushed artifact (ADR-0247
// Decision 2): `<dir>/<stem>.contract.*` for a file `<dir>/<stem>.<ext>`, and `<dir>/<base>.contract.*` for a directory
// `<dir>` named `<base>`. A directory is a bundle the lane's build makes with funcd-bundle, which writes the contract
// file at its root, or a folder the lane stages with that file at its root, as an OpenTofu module.
func pushContract(lane laneSpec, staged map[string]string, artifact string) error {
	dir, file := path.Split(artifact)
	ext := path.Ext(file)
	if ext == "" {
		if strings.Contains(strings.Join(lane.Build, " "), "funcd-bundle") {
			return nil
		}
		src, ok := staged[artifact]
		if !ok {
			return fmt.Errorf("%s is neither built by funcd-bundle nor staged", artifact)
		}
		var found []string
		for _, name := range []string{file + ".contract.yaml", file + ".contract.json"} {
			if _, err := os.Stat(filepath.Join(src, name)); err == nil {
				found = append(found, name)
			}
		}
		if len(found) != 1 {
			return fmt.Errorf("the staged folder %s holds %d of %s.contract.yaml and .json at its root, not one: %v",
				src, len(found), file, found)
		}
		return nil
	}
	stem := strings.TrimSuffix(file, ext)
	yamlSrc, jsonSrc := staged[dir+stem+".contract.yaml"], staged[dir+stem+".contract.json"]
	if (yamlSrc != "") == (jsonSrc != "") {
		return fmt.Errorf("no single contract file is staged beside %s (%s.contract.yaml or .json)", artifact, stem)
	}
	if _, err := os.Stat(yamlSrc + jsonSrc); err != nil {
		return fmt.Errorf("the contract file of %s: %w", artifact, err)
	}
	return nil
}

// A staged folder is pushed only with exactly one contract file named after it at its root, and a directory that
// nothing builds or stages is no push (ADR-0247 Decision 2).
func TestScenarioExamplesBuildWithTheToolsPushContract(t *testing.T) {
	root := t.TempDir()
	folder := func(name string, files ...string) string {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o750))
		for _, f := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
		}
		return dir
	}
	contract := filepath.Join(folder("file"), "hello.contract.yaml")
	require.NoError(t, os.WriteFile(contract, nil, 0o600))
	for _, tc := range []struct {
		name     string
		lane     laneSpec
		staged   map[string]string
		artifact string
		wantErr  string
	}{
		{"a bundle that funcd-bundle builds", laneSpec{Build: []string{"funcd-bundle --out bundle"}}, nil, "bundle", ""},
		{"a staged folder holding its yaml contract", laneSpec{}, map[string]string{"stack": folder("yaml", "main.tf", "stack.contract.yaml")}, "stack", ""},
		{"a staged folder holding its json contract", laneSpec{}, map[string]string{"stack": folder("json", "main.tf", "stack.contract.json")}, "stack", ""},
		{"a staged folder without its contract", laneSpec{}, map[string]string{"stack": folder("none", "main.tf")}, "stack", "holds 0 of stack.contract.yaml"},
		{"a staged folder whose contract has another name", laneSpec{}, map[string]string{"stack": folder("other", "main.tf", "main.contract.yaml")}, "stack", "holds 0 of stack.contract.yaml"},
		{"a staged folder holding both contracts", laneSpec{}, map[string]string{"stack": folder("both", "stack.contract.yaml", "stack.contract.json")}, "stack", "holds 2 of stack.contract.yaml"},
		{"a folder that nothing builds or stages", laneSpec{}, nil, "stack", "neither built by funcd-bundle nor staged"},
		{"a file with its contract staged beside it", laneSpec{}, map[string]string{"hello.mjs": "hello.mjs", "hello.contract.yaml": contract}, "hello.mjs", ""},
		{"a file without its contract", laneSpec{}, map[string]string{"hello.mjs": "hello.mjs"}, "hello.mjs", "no single contract file is staged beside hello.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := pushContract(tc.lane, tc.staged, tc.artifact)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// readLanes reads scripts/lanes.yaml, the lane registry, by lane name.
func readLanes(t *testing.T) map[string]laneSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "lanes.yaml"))
	require.NoError(t, err)
	var lanes map[string]laneSpec
	require.NoError(t, yaml.Unmarshal(data, &lanes))
	return lanes
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
