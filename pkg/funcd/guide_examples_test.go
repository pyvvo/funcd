package funcd_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The guide's examples are files that tests run (ADR-0325): every manifest a page shows applies to a fresh platform,
// and every ::include of a page names a file, and a region, that exist.

// scenario: examples-apply
func TestScenarioGuideExamplesApply(t *testing.T) {
	t.Parallel()
	files := guideManifests(t)
	examples, err := filepath.Glob(filepath.Join("..", "..", "api", "types", "v1alpha1", "examples", "*", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, examples, "the guide has no example manifest")
	for _, file := range examples {
		require.Contains(t, files, file, "every example file is applied")
	}
	for _, inc := range guideIncludes(t, filepath.Join("docs", "guide")) {
		if file, pinned := inc.target(t); pinned && filepath.Ext(file) == ".yaml" {
			require.Contains(t, files, file, "every lane manifest that a page includes is applied")
		}
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			require.NoError(t, applyOnFreshPlatform(t, file))
		})
	}
}

// scenario: example-manifest-refused
func TestScenarioExampleManifestRefused(t *testing.T) {
	t.Parallel()
	file := filepath.Join("testdata", "guide-undeclared-binding.yaml")
	err := applyOnFreshPlatform(t, file)
	require.ErrorContains(t, err, file)
	require.ErrorContains(t, err, "binding-declared")
}

// scenario: include-resolves
func TestScenarioGuideIncludesResolve(t *testing.T) {
	t.Parallel()
	require.Empty(t, includeProblems(t, filepath.Join("docs", "guide")))
	problems := includeProblems(t, filepath.Join("pkg", "funcd", "testdata", "guide-broken"))
	require.Len(t, problems, 3, "%v", problems)
	require.Contains(t, problems[0], "no-such-file.yaml")
	require.Contains(t, problems[1], "own-store.yaml#no-such-region")
	require.Contains(t, problems[2], "attributes")
	for _, problem := range problems {
		require.Contains(t, problem, filepath.Join("guide-broken", "page.md"), "a problem names its page")
	}
}

// guideInclude is one ::include line of a guide page: the page's path, the file and region the line names, and the
// attributes it carries, which this check does not read.
type guideInclude struct{ page, path, region, attributes string }

func guideIncludes(t *testing.T, pages string) []guideInclude {
	t.Helper()
	line := regexp.MustCompile(`(?m)^\s*::include\[([^\]]+)\](\{[^}]*\})?\s*$`)
	var found []guideInclude
	err := filepath.WalkDir(filepath.Join("..", "..", pages), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".md" && filepath.Ext(path) != ".mdx") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range line.FindAllStringSubmatch(string(data), -1) {
			file, region, _ := strings.Cut(m[1], "#")
			found = append(found, guideInclude{page: path, path: file, region: region, attributes: m[2]})
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

// target returns the file an include names, relative to its page. A path under _repos/<repo>/ at funcd's root names
// a file of the repo that go.mod pins, read from the module cache; pinned reports that case.
func (inc guideInclude) target(t *testing.T) (file string, pinned bool) {
	t.Helper()
	file = filepath.Join(filepath.Dir(inc.page), inc.path)
	rel, err := filepath.Rel(filepath.Join("..", "..", "_repos"), file)
	require.NoError(t, err)
	if strings.HasPrefix(rel, "..") {
		return file, false
	}
	repo, path, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return filepath.Join(langmod.Dir(t, "github.com/pyvvo/"+repo), filepath.FromSlash(path)), true
}

func includeProblems(t *testing.T, pages string) []string {
	t.Helper()
	var problems []string
	for _, inc := range guideIncludes(t, pages) {
		name := inc.path
		if inc.region != "" {
			name += "#" + inc.region
		}
		file, _ := inc.target(t)
		data, err := os.ReadFile(file)
		switch {
		case inc.attributes != "":
			problems = append(problems, inc.page+": "+name+": an include with attributes is not checked; write it without")
		case err != nil:
			problems = append(problems, inc.page+": "+name+": "+err.Error())
		case inc.region != "" && !marksRegion(string(data), inc.region):
			problems = append(problems, inc.page+": "+name+": the file marks no such region")
		}
	}
	return problems
}

// marksRegion reports whether content opens and closes region with the markers that fumadocs reads in // and #
// comments (ADR-0325 Decision 3).
func marksRegion(content, region string) bool {
	start := regexp.MustCompile(`^\s*(?://|#)\s*#?region\b\s*(.*?)\s*$`)
	end := regexp.MustCompile(`^\s*(?://|#)\s*#?endregion\b\s*(.*?)\s*$`)
	open := false
	for _, line := range strings.Split(content, "\n") {
		if m := start.FindStringSubmatch(line); m != nil && m[1] == region {
			open = true
		} else if m := end.FindStringSubmatch(line); open && m != nil && (m[1] == region || m[1] == "") {
			return true
		}
	}
	return false
}

// guideManifests lists the files under api/types/v1alpha1/examples and the YAML files that the guide's pages include
// from a pinned repo.
func guideManifests(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join("..", "..", "api", "types", "v1alpha1", "examples"),
		func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Ext(path) == ".yaml" {
				files = append(files, path)
			}
			return err
		})
	require.NoError(t, err)
	for _, inc := range guideIncludes(t, filepath.Join("docs", "guide")) {
		if file, pinned := inc.target(t); pinned && filepath.Ext(file) == ".yaml" {
			files = append(files, file)
		}
	}
	slices.Sort(files)
	return slices.Compact(files)
}

// applyOnFreshPlatform applies file's documents in order, as funcdctl apply sends them, to a platform assembled over
// memory, whose write path runs the daemon's validation and admissions. InMemory sets no runtime shim, so the platform
// fetches no image and runs no function code.
func applyOnFreshPlatform(t *testing.T, file string) error {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	docs, err := sdk.DecodeManifestDocuments(data)
	require.NoError(t, err, file)
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
	client, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	for _, doc := range docs {
		if _, err := client.Apply(ctx, doc.Object); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "guide example", "%s: document %d (%s %q)",
				file, doc.Number, doc.Object.GroupVersionKind().Kind, doc.Object.GetName())
		}
	}
	return nil
}
