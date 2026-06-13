// Package anyleak holds a known-bad lint fixture for scenario: lint-blocks-any-leak
// (ADR-0002). The violating code lives in files gated behind the `lintfixture` build
// tag, so the default build/lint skips them; the scenario test
// (tests/lint-fixtures/lintrules_test.go) re-runs golangci-lint with
// --build-tags lintfixture and asserts it fails. This untagged file keeps the package
// non-empty and clean under the default build.
package anyleak
