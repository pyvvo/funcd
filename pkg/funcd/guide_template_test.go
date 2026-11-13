package funcd_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/internal/controlplane/kindref"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// pageTemplate is .claude/skills/develop-page/template.yaml: the sections of a feature's Usage page.
type pageTemplate struct {
	First    struct{ Heading string }   `json:"first"`
	Optional []struct{ Heading string } `json:"optional"`
	Last     struct{ Heading string }   `json:"last"`
	Refused  []string                   `json:"refused"`
}

// TestDevelopPagesFollowTheTemplate holds every feature page of the Develop chapter (an index.md beside the spec.md that
// kindref generates; the funcd and funcdctl pages have their own shape) to the develop-page skill's template: its
// sections and their order, code only through includes, no ADR number, and Examples links that exist in the pinned
// modules.
func TestDevelopPagesFollowTheTemplate(t *testing.T) {
	t.Parallel()
	tpl := readTemplate(t)
	var pages []string
	err := filepath.WalkDir(filepath.Join("..", "..", "docs", "guide", "develop"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "index.md" {
			if spec, serr := os.ReadFile(filepath.Join(filepath.Dir(path), "spec.md")); serr == nil &&
				strings.Contains(string(spec), kindref.Marker) {
				pages = append(pages, path)
			}
		}
		return err
	})
	require.NoError(t, err)
	require.NotEmpty(t, pages, "no feature page")
	for _, page := range pages {
		t.Run(filepath.Base(filepath.Dir(page)), func(t *testing.T) {
			t.Parallel()
			require.Empty(t, templateProblems(t, tpl, page))
		})
	}
}

func TestTemplateRefusesABrokenPage(t *testing.T) {
	t.Parallel()
	problems := strings.Join(templateProblems(t, readTemplate(t), filepath.Join("testdata", "develop-broken", "index.md")), "\n")
	for _, want := range []string{
		`the first section is "Overview", not "Usage"`,
		`"Quotas" is a refused heading`,
		`"Limits and quotas" comes after "Access"`,
		`the free section "Conditional writes" comes after an optional one`,
		`"Examples" is not the last section`,
		`line 11: a "yaml" fence`,
		"line 12: a manifest inside a fence",
		"line 18: a manifest inside a fence",
		"names an ADR",
		"line 8: a link to funcd-typescript at v0.27.0; link to main",
		"line 9: a link to funcd-python at v0.22.0; link to main",
		"line 9: a link to funcd-python at v0.21.0; link to main",
		`line 24: code from funcd-typescript sits under a "### TypeScript" heading`,
		`line 28: a hand-written "Method" table`,
		`"Access" appears twice`,
		"an Examples link points at examples/no-such-example, which no lane in scripts/lanes.yaml runs",
		"an Examples link points at shim, which no lane in scripts/lanes.yaml runs",
	} {
		require.Contains(t, problems, want)
	}
	require.NotContains(t, problems, `"Methods"`, "a heading inside a fence is not a section")
	require.NotContains(t, problems, "v0.1.0", "a link inside a fence is not a link")
}

func TestTemplateWantsExamplesBesideALaneExample(t *testing.T) {
	t.Parallel()
	for fixture, want := range map[string]string{
		"develop-no-examples":    `the page includes a lane example, so its last section is "Examples"`,
		"develop-empty-examples": `the "Examples" section links no lane example`,
	} {
		require.Equal(t, []string{want}, templateProblems(t, readTemplate(t), filepath.Join("testdata", fixture, "index.md")))
	}
}

func TestLaneFoldersHoldEveryKVExample(t *testing.T) {
	t.Parallel()
	run := laneFolders(t)
	for _, folder := range []string{
		"github.com/pyvvo/funcd-typescript examples/kv-counter",
		"github.com/pyvvo/funcd-python examples/kv-counter",
		"github.com/pyvvo/funcd-functions apps/todo",
	} {
		require.True(t, run[folder], folder)
	}
	require.False(t, run["github.com/pyvvo/funcd-typescript examples"], "a staged file's top folder is not an example")
	require.False(t, run["github.com/pyvvo/funcd-typescript shim"])
	require.False(t, run["github.com/pyvvo/funcd e2e/fixtures"], "a fixture that funcd stages is no example")
	require.False(t, run["github.com/pyvvo/funcd-functions apps/todo/dr"], "a folder inside an example is not one")
}

func readTemplate(t *testing.T) pageTemplate {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".claude", "skills", "develop-page", "template.yaml"))
	require.NoError(t, err)
	var tpl pageTemplate
	require.NoError(t, yaml.Unmarshal(data, &tpl))
	require.NotEmpty(t, tpl.First.Heading)
	return tpl
}

// templateProblems lists how the page in file breaks the template.
func templateProblems(t *testing.T, tpl pageTemplate, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	fenceOpen := regexp.MustCompile("^(```+|~~~+)\\s*(\\S*)")
	manifest := regexp.MustCompile(`(^|[\s{,])"?apiVersion"?\s*:`)
	laneCode := regexp.MustCompile(`^::include\[[^\]]*_repos/(funcd-typescript|funcd-python)/`)
	table := regexp.MustCompile(`^\|\s*(Method|Field|Key)\s*\|`)
	link := regexp.MustCompile(`\]\(https://github\.com/pyvvo/([a-z-]+)(/[^)\s]*)?\)`)
	tree := regexp.MustCompile(`^/(?:tree|blob)/([^/]+)/?(.*)$`)
	version := regexp.MustCompile(`v\d+\.\d+\.\d+`)
	language := map[string]string{"funcd-typescript": "TypeScript", "funcd-python": "Python"}
	var problems, headings []string
	var examples [][]string
	fence, section, sub, lane := "", "", "", false
	for i, line := range strings.Split(string(data), "\n") {
		n, trimmed, inFence := i+1, strings.TrimSpace(line), fence != ""
		switch {
		case inFence && strings.HasPrefix(trimmed, fence):
			fence = ""
		case inFence:
			if manifest.MatchString(line) {
				problems = append(problems, fmt.Sprintf("line %d: a manifest inside a fence; include it from a tested file", n))
			}
		case fenceOpen.MatchString(trimmed):
			m := fenceOpen.FindStringSubmatch(trimmed)
			fence = m[1]
			if !slices.Contains([]string{"sh", "bash", "console"}, m[2]) {
				problems = append(problems, fmt.Sprintf("line %d: a %q fence; show code through ::include, and commands as sh, bash or console", n, m[2]))
			}
		case strings.HasPrefix(line, "## "):
			section, sub = strings.TrimPrefix(line, "## "), ""
			headings = append(headings, section)
		case strings.HasPrefix(line, "### "):
			sub = strings.TrimPrefix(line, "### ")
		case laneCode.MatchString(trimmed):
			lane = true
			repo := laneCode.FindStringSubmatch(trimmed)[1]
			if sub != language[repo] {
				problems = append(problems, fmt.Sprintf("line %d: code from %s sits under a \"### %s\" heading", n, repo, language[repo]))
			}
		case table.MatchString(trimmed):
			problems = append(problems, fmt.Sprintf("line %d: a hand-written %q table; the Spec tab lists them", n, table.FindStringSubmatch(trimmed)[1]))
		}
		if inFence || fence != "" {
			continue
		}
		for _, m := range link.FindAllStringSubmatch(line, -1) {
			repo, rest, ref, folder := m[1], m[2], "", ""
			if tm := tree.FindStringSubmatch(rest); tm != nil {
				ref, folder = tm[1], strings.TrimSuffix(tm[2], "/")
			}
			switch v := version.FindString(rest); {
			case v != "":
				problems = append(problems, fmt.Sprintf("line %d: a link to %s at %s; link to main", n, repo, v))
			case ref != "" && ref != "main":
				problems = append(problems, fmt.Sprintf("line %d: a link to %s at %s; link to main", n, repo, ref))
			}
			if section == tpl.Last.Heading {
				examples = append(examples, []string{repo, folder, strings.TrimPrefix(rest, "/")})
			}
		}
	}
	if regexp.MustCompile(`\bADR-\d{4}\b`).Match(data) {
		problems = append(problems, "the page names an ADR")
	}
	if lane && !slices.Contains(headings, tpl.Last.Heading) {
		problems = append(problems, fmt.Sprintf("the page includes a lane example, so its last section is %q", tpl.Last.Heading))
	}
	if slices.Contains(headings, tpl.Last.Heading) && len(examples) == 0 {
		problems = append(problems, fmt.Sprintf("the %q section links no lane example", tpl.Last.Heading))
	}
	if len(headings) == 0 || headings[0] != tpl.First.Heading {
		first := ""
		if len(headings) > 0 {
			first = headings[0]
		}
		problems = append(problems, fmt.Sprintf("the first section is %q, not %q", first, tpl.First.Heading))
	}
	order := map[string]int{}
	for i, o := range tpl.Optional {
		order[o.Heading] = i
	}
	last, previous := -1, ""
	for i, h := range headings {
		if slices.Index(headings, h) < i {
			problems = append(problems, fmt.Sprintf("%q appears twice", h))
		}
		if i == 0 {
			continue
		}
		position, optional := order[h]
		switch {
		case slices.Contains(tpl.Refused, h):
			problems = append(problems, fmt.Sprintf("%q is a refused heading", h))
		case h == tpl.Last.Heading:
			if i != len(headings)-1 {
				problems = append(problems, fmt.Sprintf("%q is not the last section", h))
			}
		case optional:
			if position < last {
				problems = append(problems, fmt.Sprintf("%q comes after %q", h, previous))
			}
			last, previous = position, h
		case last >= 0:
			problems = append(problems, fmt.Sprintf("the free section %q comes after an optional one", h))
		}
	}
	run := laneFolders(t)
	for _, m := range examples {
		module, folder := "github.com/pyvvo/"+m[0], m[1]
		if !run[module+" "+folder] {
			shown := folder
			if shown == "" {
				shown = m[2]
			}
			problems = append(problems, fmt.Sprintf("an Examples link points at %s, which no lane in scripts/lanes.yaml runs", shown))
			continue
		}
		if _, err := os.Stat(filepath.Join(langmod.Dir(t, module), filepath.FromSlash(folder))); err != nil {
			problems = append(problems, fmt.Sprintf("%s does not exist in %s at the version go.mod pins", folder, module))
		}
	}
	return problems
}

// laneFolders is the set of "<module> <folder>" that a lane runs: each lane's dir, and the example folder (the first
// two segments) of each file that it stages from a language module; a fixture that funcd stages is no example.
func laneFolders(t *testing.T) map[string]bool {
	t.Helper()
	module := func(m string) string {
		if m == "" {
			return "github.com/pyvvo/funcd"
		}
		return m
	}
	run := map[string]bool{}
	for _, lane := range readLanes(t) {
		if lane.Dir != "" {
			run[module(lane.Module)+" "+lane.Dir] = true
		}
		for _, st := range lane.Stage {
			if parts := strings.SplitN(st.From, "/", 3); st.Module != "" && len(parts) == 3 {
				run[st.Module+" "+parts[0]+"/"+parts[1]] = true
			}
		}
	}
	return run
}
