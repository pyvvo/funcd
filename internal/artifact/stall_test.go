package artifact_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/testkit/stallregistry"
)

// stallWait is how long a call may take against a stalled registry: one wait for response headers or body bytes, which
// is not retried, and a margin.
const stallWait = 30 * time.Second

// A registry that accepts a request and never answers ends the call after one bounded wait, so the reconcile that
// made it fails and the controller's only worker moves on (#697). The whole platform's case is in tests/chaos.
func TestIssue697_StalledRegistryCallEnds(t *testing.T) {
	host, requests := stallregistry.Start(t)
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	for repo, call := range map[string]func(ref string) error{
		"resolve":   func(ref string) error { _, err := m.Resolve(context.Background(), ref); return err },
		"platforms": func(ref string) error { _, err := m.Platforms(context.Background(), ref, digest); return err },
		"materialize": func(ref string) error {
			fn := &v1.Function{}
			fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
			_, err := m.Materialize(context.Background(), fn)
			return err
		},
		"site": func(ref string) error { _, err := artifact.ResolveSite(context.Background(), ref); return err },
		"contract": func(ref string) error {
			_, _, err := artifact.InspectContract(context.Background(), ref, digest)
			return err
		},
	} {
		t.Run(repo, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { done <- call(host + "/" + repo + "/fn:latest") }()
			select {
			case err := <-done:
				require.Error(t, err)
				require.Equal(t, 1, requests(repo), "a request that timed out is not retried")
			case <-time.After(stallWait):
				t.Fatalf("the call still waits on the stalled registry after %s", stallWait)
			}
		})
	}
}

// A registry that sends a response's headers and then stops sending its body ends the call after one bounded wait for
// the next bytes, whether it stalls a manifest or a blob (#697).
func TestIssue697_StalledRegistryBodyEnds(t *testing.T) {
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.DescriptorEmptyJSON,
		Layers: []ocispec.Descriptor{{
			MediaType: artifact.BundleTarMediaType,
			Digest:    digest.FromString("bundle"),
			Size:      stallregistry.BodySize,
		}},
	})
	require.NoError(t, err)
	host := stallregistry.StartBody(t, manifest)
	const stalled = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	for repo, call := range map[string]func(ref string) error{
		"platforms": func(ref string) error { _, err := m.Platforms(context.Background(), ref, stalled); return err },
		"contract": func(ref string) error {
			_, _, err := artifact.InspectContract(context.Background(), ref, stalled)
			return err
		},
		"blob": func(ref string) error {
			fn := &v1.Function{}
			fn.Spec.Image, fn.Spec.ImageDigest = ref, digest.FromBytes(manifest).String()
			_, err := m.Materialize(context.Background(), fn)
			return err
		},
	} {
		t.Run(repo, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { done <- call(host + "/" + repo + "/fn:latest") }()
			select {
			case err := <-done:
				require.ErrorContains(t, err, "the registry sent no response bytes")
			case <-time.After(stallWait):
				t.Fatalf("the call still reads the stalled response body after %s", stallWait)
			}
		})
	}
}
