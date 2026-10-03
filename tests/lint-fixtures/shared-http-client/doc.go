// Package sharedhttpclient holds a known-bad lint fixture for issue #564 (the shared http.DefaultClient and
// http.DefaultTransport ban), gated behind the `lintfixture` build tag — see any-leak/doc.go and
// tests/lint-fixtures/lintrules_test.go.
package sharedhttpclient
