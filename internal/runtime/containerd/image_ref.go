package containerd

import (
	"errors"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
)

// normalizedRef returns the fully qualified name containerd stores and pulls ref under, so
// "funcd/runtime-<rt>:latest" becomes "docker.io/funcd/runtime-<rt>:latest": the image store matches names
// exactly, and the pull resolver reads a short ref's first path element as the registry host. A ref that does
// not parse is returned unchanged.
func normalizedRef(ref string) string {
	named, err := reference.ParseDockerRef(ref)
	if err != nil {
		return ref
	}
	return named.String()
}

// imageAbsent reports a pull error that means the image does not exist (ADR-0149 Decision 3): any errdefs NotFound, or
// docker.ErrInvalidAuthorization, Docker Hub's answer for a repository it does not have. Every other error, such as a
// network failure, a 5xx or a token endpoint's 403, is not absence.
func imageAbsent(err error) bool {
	return errdefs.IsNotFound(err) || errors.Is(err, docker.ErrInvalidAuthorization)
}
