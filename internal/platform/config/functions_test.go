package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// functions.defaultResources (ADR-0291 Decision 3): 1 CPU and 512Mi unless set, an empty field setting no default, and a
// value ResourceSpec.Validate refuses fails the load.
func TestFunctionDefaultResources(t *testing.T) {
	t.Parallel()
	parse := func(yaml string) (config.Config, error) {
		return config.Parse("test.yaml", []byte(yaml), config.Flags{})
	}
	c, err := parse("storage:\n  mode: memory\n")
	require.NoError(t, err)
	got, err := c.FunctionDefaultResources()
	require.NoError(t, err)
	require.Equal(t, v1.ResourceSpec{CPU: "1", Memory: "512Mi"}, got)

	c, err = parse("storage:\n  mode: memory\nfunctions:\n  defaultResources:\n    cpu: \"\"\n    memory: 1Gi\n")
	require.NoError(t, err)
	got, err = c.FunctionDefaultResources()
	require.NoError(t, err)
	require.Equal(t, v1.ResourceSpec{Memory: "1Gi"}, got, "an empty cpu sets no default cpu")

	_, err = parse("storage:\n  mode: memory\nfunctions:\n  defaultResources:\n    cpu: lots\n")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "functions.defaultResources")
}
