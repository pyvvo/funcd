package sdk_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// DecodeManifest accepts YAML (the kubectl-style `funcdcli apply -f fn.yaml`) AND JSON.
func TestDecodeManifestAcceptsYAMLAndJSON(t *testing.T) {
	yamlManifest := []byte(`
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: front
  namespace: default
  resourceGroup: rg1
spec:
  runtime: nodejs22
  handler: handle
  artifact:
    uri: oci-layout:///mnt/funcd-deps/registry:front
  links:
    - alias: greeter
      target: greeter
`)
	obj, err := sdk.DecodeManifest(yamlManifest)
	require.NoError(t, err)
	fn, ok := obj.(*v1.Function)
	require.True(t, ok)
	require.Equal(t, v1.ObjectName("front"), fn.Name)
	require.Len(t, fn.Spec.Links, 1)
	require.Equal(t, "greeter", fn.Spec.Links[0].Alias)
	require.Equal(t, v1.ObjectName("greeter"), fn.Spec.Links[0].Target)

	// JSON still decodes (JSON is valid YAML).
	jsonManifest := []byte(`{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"g"}}`)
	o2, err := sdk.DecodeManifest(jsonManifest)
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("g"), o2.GetName())
}
