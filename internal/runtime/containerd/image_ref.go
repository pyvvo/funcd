package containerd

import "github.com/distribution/reference"

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
