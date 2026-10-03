// Package sharedhttpclient holds a known-bad lint fixture for issues #564 and #571 (the shared http.DefaultClient and
// http.DefaultTransport ban, also through http.Get, Head, Post and PostForm), gated behind the `lintfixture` build tag — see any-leak/doc.go and
// tests/lint-fixtures/lintrules_test.go.
package sharedhttpclient
