//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// applyBucket parses a Bucket manifest and applies it through the control-plane client (ADR-0080).
func applyBucket(t *testing.T, c *sdk.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var b v1.Bucket
	require.NoError(t, yaml.Unmarshal(data, &b), "parse %s", path)
	_, err = c.Apply(context.Background(), &b)
	require.NoError(t, err)
}

// scenario (e2e): blob-object-via-context-blob (ADR-0127) — the REAL native path: build the blob-object
// handler, push it, apply it, then POST. The handler put/get/lists its BOUND, OWNED gold prefix through
// context.blob (→ worker-node local API UDS → binding-gated, S3Capability-authorized Facade → the same
// substrate the S3 frontend serves) with NO keypair and NO @aws-sdk, and proves bind-as-grant default-deny
// by attempting an UNBOUND alias (→ 403). Covers scenarios blob-read-write, blob-list, blob-unbound-forbidden.
func TestScenarioE2EBlobObjectViaContextBlob(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := tsExample(t, "blob-object")
	layout := t.TempDir()
	ref, digest := pushExampleFn(t, layout, exDir, "object")

	data, err := os.ReadFile(filepath.Join(exDir, "function.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse function.yaml")
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest

	call := func() (int, []byte) {
		resp, perr := http.Post(dpURL+"/function/blob-object", "application/json", strings.NewReader(`{"data":{"inv":"a"}}`))
		require.NoError(t, perr)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	// scenario (e2e): unbound-access-denied — WITHOUT spec.blob, context.blob.put('gold', …) is Forbidden
	// (default-deny): the un-caught put throws, so the invocation does NOT return a 200 success.
	unbound := fn
	unbound.Spec.Blob = nil
	applyFnObj(t, c, &unbound)
	waitReady(t, c, "blob-object")
	status, body := call()
	require.NotEqual(t, http.StatusOK, status, "without a spec.blob binding, context.blob is denied (default-deny): %s", body)

	// Apply the Bucket (gold owner=blob-object), then re-apply the function WITH spec.blob (the binding
	// grants read, the ownership grants write). ADR-0121: owner existence is reconcile-time, any order.
	applyBucket(t, c, filepath.Join(exDir, "bucket.yaml"))
	applyFnObj(t, c, &fn)
	waitReady(t, c, "blob-object")

	// scenario (e2e): blob-read-write + blob-list + blob-unbound-forbidden — the bound owner put/gets/lists
	// its gold prefix natively, and the UNBOUND 'other' alias is denied (denied=true).
	status, body = call()
	require.Equal(t, http.StatusOK, status, "with the spec.blob binding, the native context.blob round-trip succeeds: %s", body)
	var out struct {
		Put    bool `json:"put"`
		Get    bool `json:"get"`
		List   int  `json:"list"`
		Denied bool `json:"denied"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "parse output: %s", body)
	require.True(t, out.Put, "put to the owned gold prefix succeeds (context.blob, no keypair)")
	require.True(t, out.Get, "get reads the object back")
	require.GreaterOrEqual(t, out.List, 1, "list sees the written object under the bound prefix")
	require.True(t, out.Denied, "an UNBOUND alias is Forbidden (bind-as-grant default-deny)")
}
