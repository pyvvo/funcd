package funcd

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// devContracts is a dev resolver that knows one image, built with a contract, and one built with none.
type devContracts struct{}

func (devContracts) Contract(_ context.Context, image string) (v1.WorkflowContract, string, error) {
	switch image {
	case "dev://typed":
		return v1.WorkflowContract{Input: json.RawMessage(`{"type":"object"}`)}, "", nil
	case "dev://plain":
		return v1.WorkflowContract{}, "", nil
	}
	return v1.WorkflowContract{}, "", fault.NotFoundf("test", "%s is not a build of this session", image)
}

// ADR-0347 Decision 2: a server reads a Job's contract by its digest, and funcdctl dev's resolver serves the images it
// builds by their reference, so a run's admission checks its input under dev as on a server.
func TestJobContracts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := jobContracts(nil).Inspect(ctx, "file:///x.mjs", "")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an image pinned by no digest has no contract to read by it")

	dev := jobContracts(devContracts{})
	blob, err := dev.Inspect(ctx, "dev://typed", "")
	require.NoError(t, err)
	require.JSONEq(t, `{"input":{"type":"object"}}`, string(blob))
	for _, image := range []string{"dev://plain", "dev://other"} {
		_, err := dev.Inspect(ctx, image, "")
		require.Equal(t, fault.NotFound, fault.KindOf(err), image)
	}
}
