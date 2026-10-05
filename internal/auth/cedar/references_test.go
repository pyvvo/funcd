package cedar_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
)

func TestFunctionGrantsFindsScopeAndConditionReferences(t *testing.T) {
	text := `permit (principal == Function::"ns/a", action, resource);
permit (principal, action, resource) when { principal in [Function::"ns/b", Function::"other/c"] };
forbid (principal, action, resource == Function::"ns/b");
permit (principal, action, resource);`
	got, err := cedarauth.FunctionGrants("ns", "p", text)
	require.NoError(t, err)
	require.Len(t, got["a"], 1)
	require.Contains(t, got["a"][0], "policy/ns/p#0@")
	require.Len(t, got["b"], 2)
	require.Contains(t, got["b"][0], "policy/ns/p#1@")
	require.Contains(t, got["b"][1], "policy/ns/p#2@")
	require.NotContains(t, got, "c", "a Function of another namespace is not this namespace's")

	same, err := cedarauth.FunctionGrants("ns", "p", text)
	require.NoError(t, err)
	require.Equal(t, got, same, "a grant is stable")

	_, err = cedarauth.FunctionGrants("ns", "p", "not cedar")
	require.Error(t, err)
}
