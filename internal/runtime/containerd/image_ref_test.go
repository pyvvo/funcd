package containerd

import (
	"context"
	"net/url"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
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
