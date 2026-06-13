// Package lintfixtures proves the ADR-0002 lint rules actually fire on known-bad code.
// The fixtures live in sibling subpackages gated behind the `lintfixture` build tag, so
// the normal `just lint` skips them. Each test below re-runs golangci-lint with the real
// .golangci.yml config plus --build-tags lintfixture and asserts the expected finding —
// the executable, drift-free proof that the rules are wired correctly.
package lintfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from this test file to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above " + file)
		}
		dir = parent
	}
}

// lintFixture runs golangci-lint over one fixture package with the real config and the
// lintfixture build tag, returning the combined output and the run error (non-nil when
// golangci-lint reports findings).
func lintFixture(t *testing.T, pkg string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", "tool", "golangci-lint", "run", "--build-tags", "lintfixture", "./"+pkg)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scenario: lint-blocks-any-leak (ADR-0002)
func TestScenario_LintBlocksAnyLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to golangci-lint; skipped under -short")
	}
	out, err := lintFixture(t, "tests/lint-fixtures/any-leak")
	if err == nil {
		t.Fatalf("expected golangci-lint to FAIL on the any-leak fixture, but it passed:\n%s", out)
	}
	if !strings.Contains(out, "forbidigo") {
		t.Fatalf("expected a forbidigo finding for `any`, got:\n%s", out)
	}
}

// scenario: no-mock-framework (ADR-0002)
func TestScenario_NoMockFramework(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to golangci-lint; skipped under -short")
	}
	out, err := lintFixture(t, "tests/lint-fixtures/mock-framework")
	if err == nil {
		t.Fatalf("expected golangci-lint to FAIL on the mock-framework fixture, but it passed:\n%s", out)
	}
	if !strings.Contains(out, "depguard") {
		t.Fatalf("expected a depguard finding for the mock import, got:\n%s", out)
	}
}
