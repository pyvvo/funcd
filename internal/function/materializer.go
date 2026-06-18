package function

import (
	"context"
	"os"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Materializer resolves a Function's artifact to a local path the runtime shim reads
// (ADR-0030). It is a seam: the local-file driver here serves the dev/e2e process path;
// the OCI/oras driver (ADR-0031) is the production driver of this same interface.
type Materializer interface {
	Materialize(ctx context.Context, fn *v1.Function) (localPath string, err error)
}

// ArtifactResolver resolves an OCI artifact ref (tag) to its digest (ADR-0035), so the
// reconciler can pin it into the immutable Revision at stamp time instead of the user
// typing it. The oras driver implements it; the file driver needs none (file:// has no
// digest). Optional in Deps: nil → no resolution (file/legacy mode).
type ArtifactResolver interface {
	Resolve(ctx context.Context, uri string) (digest string, err error)
}

// FileMaterializer resolves a "file://" (or bare-path) ArtifactRef.URI to a local path.
// It is the no-dependency dev/test driver; production uses the oras driver (ADR-0031).
type FileMaterializer struct{}

// NewFileMaterializer builds the local-file Materializer.
func NewFileMaterializer() *FileMaterializer { return &FileMaterializer{} }

// Materialize returns the local filesystem path of the Function's artifact, verifying it
// exists. It accepts "file:///abs/path", "file://abs/path", or a bare local path.
func (FileMaterializer) Materialize(_ context.Context, fn *v1.Function) (string, error) {
	const op = "function.FileMaterializer.Materialize"
	uri := string(fn.Spec.Artifact.URI)
	if uri == "" {
		return "", fault.Invalidf(op, "function %s/%s has no spec.artifact.uri", fn.Namespace, fn.Name)
	}
	path := strings.TrimPrefix(uri, "file://")
	if path == "" {
		return "", fault.Invalidf(op, "artifact uri %q resolves to an empty path", uri)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fault.NotFoundf(op, "artifact %q not found: %v", path, err)
	}
	return path, nil
}
