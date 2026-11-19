//go:build linux

package containerd

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// An Addon's image is pulled with the login the Docker credential store holds for its registry, and anonymously from
// a registry it holds none for (ADR-0332 Implementation plan).
func TestRegistryCredsReadTheDockerStore(t *testing.T) {
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("funcd:secret-pass"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"auths":{"ghcr.io":{"auth":"`+auth+`"},"token.example":{"identitytoken":"refresh-me"}}}`), 0o600))
	t.Setenv("DOCKER_CONFIG", dir)

	creds := registryCreds()
	user, secret, err := creds("ghcr.io")
	require.NoError(t, err)
	require.Equal(t, "funcd", user)
	require.Equal(t, "secret-pass", secret)
	user, secret, err = creds("token.example")
	require.NoError(t, err)
	require.Empty(t, user, "an identity token is a refresh token with no user")
	require.Equal(t, "refresh-me", secret)
	user, secret, err = creds("quay.io")
	require.NoError(t, err)
	require.Empty(t, user+secret, "a registry with no login is anonymous")
}
