package artifact_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/testkit/stallregistry"
)

// stallWait is how long a call may take against a registry that never answers: one wait for response headers, which
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
