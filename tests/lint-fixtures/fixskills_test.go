package lintfixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssue442_RevertCheckCoversRuntimeReads: a `go test -overlay` revert check changes only what the
// build reads, so a test that reads a file at runtime still sees the fixed file and passes. The /fix and
// /fix-review revert checks must say how to check such a test: embed the file, or run the test where the
// pre-fix file is on disk.
func TestIssue442_RevertCheckCoversRuntimeReads(t *testing.T) {
	root := repoRoot(t)
	for _, skill := range []string{"fix", "fix-review"} {
		raw, err := os.ReadFile(filepath.Join(root, ".claude", "skills", skill, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(raw)), " ")
		for _, want := range []string{"//go:embed", "scratch worktree of `origin/main`"} {
			if !strings.Contains(text, want) {
				t.Errorf("/%s revert check does not cover a test that reads a file at runtime: missing %q", skill, want)
			}
		}
	}
}
