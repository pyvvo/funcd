//go:build lintfixture

package boundaryfixture

// Importing an internal/ package from under tests/e2e/** must be denied by depguard's
// e2e-boundary rule (ADR-0025). (Expected: depguard "tests/e2e must only import
// pkg/** + api/**".) Gated behind the lintfixture build tag so the normal gate skips it;
// tests/lint-fixtures/lintrules_test.go runs it explicitly and asserts the finding fires.
import "github.com/green-0-rabbit/funcd/internal/store"

var _ = store.New
