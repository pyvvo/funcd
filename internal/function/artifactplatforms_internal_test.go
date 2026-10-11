package function

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

type failingPlatforms struct{}

func (failingPlatforms) Platforms(context.Context, string, string) ([]v1.OCIPlatform, error) {
	return nil, fault.Unavailablef("fake.Platforms", "registry unreachable")
}

// A digest-pinned image resolves to its own digest, so the error names that digest once.
func TestArtifactPlatformsErrorNamesDigestOnce(t *testing.T) {
	t.Parallel()
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	r := &Reconciler{platformsOf: failingPlatforms{}}
	for _, uri := range []string{"registry.example/fn:v1", "registry.example/fn@" + digest} {
		_, err := r.artifactPlatforms(context.Background(), uri, digest)
		require.Error(t, err)
		require.Equal(t, 1, strings.Count(err.Error(), digest), "uri %s: %v", uri, err)
		require.Contains(t, err.Error(), "registry.example/fn")
	}
}
