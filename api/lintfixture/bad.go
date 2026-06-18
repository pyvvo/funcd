//go:build lintfixture

package lintfixture

// Importing an internal/ package from api/** production code must be denied by depguard's
// api-boundary rule (ADR-0027 / ADR-0002 §7). (Expected: depguard "api/** production code
// must not import internal/*".) Gated behind the lintfixture build tag so the normal gate
// skips it; tests/lint-fixtures/lintrules_test.go runs it explicitly and asserts the finding.
import "github.com/green-0-rabbit/funcd/internal/store"

var _ = store.New
