//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// scenario: remote-store-serves (ADR-0208) — with the blob store on a versioned S3-compatible bucket, an object a
// Function puts through context.blob lies in that bucket under s3/<ns>/<bucket>/. That cmd/funcd then creates no
// <storage.dataDir>/blob is its TestRemoteStoreOpensNoLocalDir.
func TestScenarioRemoteStoreServes(t *testing.T) {
	t.Parallel()
	s := s3stub.New(t, nil)
	s.Set(func(s *s3stub.Stub) { s.Versioning, s.ObjectLock = "Enabled", true })
	store, err := gocloud.OpenWith(context.Background(), s.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKIDSTORE")})
	require.NoError(t, err)
	c, dpURL := shimPlatformOCI(t, funcd.WithBlob(blob.NoSign(store)))

	exDir := tsExample(t, "blob-object")
	ref, digest := pushExampleFn(t, t.TempDir(), exDir, "object")
	data, err := os.ReadFile(filepath.Join(exDir, "function.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse function.yaml")
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	applyBucket(t, c, filepath.Join(exDir, "bucket.yaml"))
	applyFnObj(t, c, &fn)
	waitReady(t, c, "blob-object")

	resp, err := http.Post(dpURL+"/function/blob-object", "application/json", strings.NewReader(`{"data":{"inv":"remote"}}`))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	require.NotEmpty(t, s.Keys("s3/default/lakehouse/gold/"), "the object lies in the store's bucket under s3/<ns>/<bucket>/")
}
