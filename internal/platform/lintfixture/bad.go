//go:build lintfixture

package lintfixture

// Importing a non-platform internal/ package from internal/platform/** must be denied by
// depguard's platform-leaf rule (ADR-0027 / ADR-0002 §7). (Expected: depguard "internal/platform
// is a leaf; must not import other internal packages".) Gated behind the lintfixture build tag;
// tests/lint-fixtures/lintrules_test.go runs it explicitly and asserts the finding.
import "github.com/pyvvo/funcd/internal/store"

var _ = store.New
