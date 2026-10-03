//go:build lintfixture

package sharedhttpclient

import "net/http"

// Send uses the process-wide client — forbidigo's http.DefaultClient rule must flag it.
// (Expected: forbidigo "use internal/platform/httpx instead of ...".)
func Send(req *http.Request) (*http.Response, error) { return http.DefaultClient.Do(req) }

// Pooled clones the process-wide transport — the http.DefaultTransport rule must flag it too.
func Pooled() *http.Transport { return http.DefaultTransport.(*http.Transport).Clone() }
