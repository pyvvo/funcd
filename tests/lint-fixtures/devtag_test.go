package lintfixtures

import (
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var justVar = regexp.MustCompile(`(?m)^([A-Za-z_][\w-]*) := "([^"]*)"$`)

// TestIssue297_JustRecipesCoverDevBuildTag: the `dev` build tag (ADR-0125) gates whole packages, so
// a recipe that lints or tests without it never sees them. `just lint`, `just test` and `just ci` (what
// CI runs) must each lint and/or test every package that has a dev-constrained file with the dev tag.
func TestIssue297_JustRecipesCoverDevBuildTag(t *testing.T) {
	root := repoRoot(t)
	devPkgs := devConstrainedPackages(t, root)
	if len(devPkgs) == 0 {
		t.Fatal("found no package with a dev-constrained file; the scan is broken")
	}
	raw, err := os.ReadFile(filepath.Join(root, "justfile"))
	if err != nil {
		t.Fatal(err)
	}
	justfile := string(raw)
	for _, m := range justVar.FindAllStringSubmatch(justfile, -1) {
		justfile = strings.ReplaceAll(justfile, "{{"+m[1]+"}}", m[2])
	}
	const lintDev, testDev = "golangci-lint run --build-tags dev", "go test -tags dev"
	for recipe, cmds := range map[string][]string{
		"lint": {lintDev},
		"test": {testDev},
		"ci":   {lintDev, testDev},
	} {
		body := recipeBody(justfile, recipe)
		for _, cmd := range cmds {
			patterns := patternsAfter(body, cmd)
			if patterns == nil {
				t.Errorf("just %s never runs %q", recipe, cmd)
				continue
			}
			for _, pkg := range devPkgs {
				if !covered(patterns, pkg) {
					t.Errorf("just %s: %q does not cover dev-tagged package ./%s (patterns %v)", recipe, cmd, pkg, patterns)
				}
			}
		}
	}
}

func devConstrainedPackages(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var pkgs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); path != root && err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || !hasDevConstraint(t, path) {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); !seen[rel] {
			seen[rel] = true
			pkgs = append(pkgs, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

func hasDevConstraint(t *testing.T, path string) bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return mentionsDev(expr)
	}
	return false
}

func mentionsDev(x constraint.Expr) bool {
	switch x := x.(type) {
	case *constraint.TagExpr:
		return x.Tag == "dev"
	case *constraint.NotExpr:
		return mentionsDev(x.X)
	case *constraint.AndExpr:
		return mentionsDev(x.X) || mentionsDev(x.Y)
	case *constraint.OrExpr:
		return mentionsDev(x.X) || mentionsDev(x.Y)
	}
	return false
}

// recipeBody returns the indented lines under the `<name>:` recipe header.
func recipeBody(justfile, name string) []string {
	var body []string
	in := false
	for _, line := range strings.Split(justfile, "\n") {
		switch {
		case strings.HasPrefix(line, name+":") && !strings.HasPrefix(line, name+":="):
			in = true
		case in && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			body = append(body, line)
		case in:
			return body
		}
	}
	return body
}

// patternsAfter returns the ./-relative package patterns on the first body line running cmd, or nil.
func patternsAfter(body []string, cmd string) []string {
	for _, line := range body {
		_, rest, ok := strings.Cut(line, cmd)
		if !ok {
			continue
		}
		patterns := []string{}
		for _, f := range strings.Fields(rest) {
			if strings.HasPrefix(f, "./") {
				patterns = append(patterns, strings.TrimSuffix(strings.TrimPrefix(f, "./"), ";"))
			}
		}
		return patterns
	}
	return nil
}

func covered(patterns []string, pkg string) bool {
	for _, p := range patterns {
		p = strings.TrimSuffix(p, "/")
		if p == pkg {
			return true
		}
		if base, ok := strings.CutSuffix(p, "..."); ok {
			base = strings.TrimSuffix(base, "/")
			if base == "" || pkg == base || strings.HasPrefix(pkg, base+"/") {
				return true
			}
		}
	}
	return false
}
