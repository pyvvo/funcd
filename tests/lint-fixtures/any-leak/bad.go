//go:build lintfixture

package anyleak

// Process takes a bare `any` in an exported signature — forbidigo's `\bany\b` rule
// must flag it. (Expected: forbidigo "do not use 'any'...".)
func Process(data any) error { return nil }

// Config carries a map[string]any field — the `any` identifier must be flagged too.
type Config struct {
	Fields map[string]any
}
