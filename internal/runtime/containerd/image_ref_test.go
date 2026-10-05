package containerd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/stretchr/testify/require"
)

// A runtime image under a custom imagePrefix with no registry host (acme/runtime-<rt>:latest) must be pulled from
// Docker Hub, not from a registry host named "acme". The resolver is the one containerd's Pull uses. Found while judging draft ADR-0149.
func TestNormalizedRef_PullsShortRefFromDockerHub(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the resolver builds the registry request and fails before dialing, so no network is needed

	_, _, err := docker.NewResolver(docker.ResolverOptions{}).Resolve(ctx, normalizedRef("acme/runtime-deno:latest"))
	var uerr *url.Error
	require.ErrorAs(t, err, &uerr, "the resolve must reach the registry request")
	u, err := url.Parse(uerr.URL)
	require.NoError(t, err)
	require.Equal(t, "registry-1.docker.io", u.Host, "a short ref names a Docker Hub repository, not a registry host")
	require.Equal(t, "/v2/acme/runtime-deno/manifests/latest", u.Path)
	require.Equal(t, "Bad Ref", normalizedRef("Bad Ref"), "a ref that does not parse is left for the pull to reject")
}

// ADR-0149 Decision 3: a pull error means an absent image only when it is a NotFound of any origin, or Docker Hub's
// invalid authorization for a repository it does not have; a timeout or a token endpoint's 403 is not absence.
func TestADR0149_ImageAbsentClassification(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err    error
		absent bool
	}{
		"manifest 404":   {fmt.Errorf("docker.io/acme/runtime-ruby3:latest: %w", errdefs.ErrNotFound), true},
		"invalid auth":   {fmt.Errorf("pull access denied: %w", fmt.Errorf("server message: insufficient_scope: %w", docker.ErrInvalidAuthorization)), true},
		"no platform":    {fmt.Errorf("no match for platform in manifest: %w", errdefs.ErrNotFound), true},
		"timeout":        {fmt.Errorf("resolve: %w", context.DeadlineExceeded), false},
		"registry 404":   {resolveAgainst(t, registry404), true},
		"docker hub 401": {resolveAgainst(t, dockerHubMissingRepo), true},
		"ghcr token 403": {resolveAgainst(t, ghcrTokenDenied), false},
		"registry 5xx":   {resolveAgainst(t, registry503), false},
	} {
		require.Equal(t, tc.absent, imageAbsent(tc.err), "%s: %v", name, tc.err)
	}
}

// resolveAgainst resolves a ref on a local registry served by handler, as containerd's Pull does, and returns its error.
func resolveAgainst(t *testing.T, handler http.HandlerFunc) error {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ref := strings.TrimPrefix(srv.URL, "http://") + "/acme/runtime-ruby3:latest"
	_, _, err := docker.NewResolver(docker.ResolverOptions{}).Resolve(ctx, ref)
	require.Error(t, err)
	return err
}

func registry404(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }

func registry503(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// dockerHubMissingRepo answers like Docker Hub for a repository it does not have: an anonymous token, then a 401 with
// error="insufficient_scope" for the request that carries it.
func dockerHubMissingRepo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		_, _ = w.Write([]byte(`{"token":"anonymous"}`))
		return
	}
	challenge := `Bearer realm="http://` + r.Host + `/token",service="registry.test",scope="repository:acme/runtime-ruby3:pull"`
	if r.Header.Get("Authorization") != "" {
		challenge += `,error="insufficient_scope"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
}

// ghcrTokenDenied answers like GHCR for a repository it does not have: a 401 challenge, then a 403 from the token endpoint.
func ghcrTokenDenied(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry.test",scope="repository:acme/runtime-ruby3:pull"`)
	w.WriteHeader(http.StatusUnauthorized)
}
