//go:build lintfixture

package sharedhttpclient

import (
	"net/http"
	"net/url"
	"strings"
)

// Send uses the process-wide client — forbidigo's http.DefaultClient rule must flag it.
// (Expected: forbidigo "use internal/platform/httpx instead of ...".)
func Send(req *http.Request) (*http.Response, error) { return http.DefaultClient.Do(req) }

// Pooled clones the process-wide transport — the http.DefaultTransport rule must flag it too.
func Pooled() *http.Transport { return http.DefaultTransport.(*http.Transport).Clone() }

// The package-level helpers send through http.DefaultClient, so the rule must flag each of them (#571).
func Get(u string) (*http.Response, error)  { return http.Get(u) }
func Head(u string) (*http.Response, error) { return http.Head(u) }
func Post(u string) (*http.Response, error) {
	return http.Post(u, "text/plain", strings.NewReader("x"))
}
func PostForm(u string) (*http.Response, error) { return http.PostForm(u, url.Values{}) }
