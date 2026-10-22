package sdk_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// TestScenario_bindings_under_spec covers scenario: bindings-under-spec (ADR-0246) — every binding kind is read from
// spec.bindings, named like its ctx accessor. The e2e suite proves each one reaches ctx: kv (kv_e2e_test.go), blob
// (blob_e2e_test.go), sql (sql e2e tests), links (invoke_e2e_test.go), lake, config and secret (the env tests).
func TestScenario_bindings_under_spec(t *testing.T) {
	t.Parallel()
	obj, err := sdk.DecodeManifest([]byte(`apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: api
  namespace: team
  resourceGroup: rg1
spec:
  runtime: nodejs22
  handler: handle
  bindings:
    kv:
      - alias: counters
        store: counters
        table: counts
    blob:
      - alias: files
        bucket: files
        prefix: uploads
    sql:
      - alias: db
        database: todo-db
    lake:
      - alias: lake
        lake: lake
    links:
      - alias: greeter
        target: greeter
    config:
      - settings
    secret:
      - api-keys
`))
	require.NoError(t, err)
	require.Equal(t, v1.FunctionBindings{
		KV:     []v1.FunctionKV{{Alias: "counters", Store: "counters", Table: "counts"}},
		Blob:   []v1.FunctionBlob{{Alias: "files", Bucket: "files", Prefix: "uploads"}},
		SQL:    []v1.FunctionSQL{{Alias: "db", Database: "todo-db"}},
		Lake:   []v1.FunctionLake{{Alias: "lake", Lake: "lake"}},
		Links:  []v1.FunctionLink{{Alias: "greeter", Target: "greeter"}},
		Config: []v1.ObjectName{"settings"},
		Secret: []v1.ObjectName{"api-keys"},
	}, obj.(*v1.Function).Spec.Bindings)
}
