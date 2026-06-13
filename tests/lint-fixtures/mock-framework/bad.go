//go:build lintfixture

package mockframework

// Importing a mock framework must be denied by depguard's no-mock rule.
// (Expected: depguard "no mock frameworks; use in-memory drivers + contract suites".)
import "github.com/stretchr/testify/mock"

var _ = mock.Anything
