package artifact_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/artifact"
)

// Issue 237: the same content pushed twice yields the same manifest digest even when the pushes fall in different
// seconds, so a tag re-pushed with unchanged content does not move (ADR-0089: same tree, same digest; ADR-0035).
func TestIssue237_IdenticalPushesKeepTheirDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bundle, entry := goodBundle(t)
	file := writeBundle(t, "export function handle() {}\n")
	site := siteDir(t)
	pushes := map[string]func(ref string) (string, error){
		"site":   func(ref string) (string, error) { return artifact.PushSite(ctx, ref, site) },
		"bundle": func(ref string) (string, error) { return artifact.PushBundle(ctx, ref, bundle, entry, "python314", "") },
		"file":   func(ref string) (string, error) { return artifact.Push(ctx, ref, file, nil, "nodejs22", "") },
	}
	for name, push := range pushes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, err := push(layoutRef(t, "first"))
			require.NoError(t, err)
			time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second))) // cross a second boundary
			second, err := push(layoutRef(t, "second"))
			require.NoError(t, err)
			require.Equal(t, first, second, "an identical push a second later keeps its digest")
		})
	}
}
