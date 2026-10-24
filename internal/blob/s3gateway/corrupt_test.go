package s3gateway_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

// scenario: corrupt-object-refused (ADR-0278) — an object whose bytes miss the MD5 of attributes naming their file is
// refused through the gateway: a GET and a ranged GET answer 500 InternalError with no byte of it, and it stays.
func TestScenarioCorruptObjectRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := newGateway(t, lakehouseMeta(), fixedPolicies{rev: "0"}, nil, fileBucketIn(dir))
	ctx := context.Background()
	bucket, key := ptrS("lakehouse"), ptrS("bronze/c.bin")
	_, err := g.client(t, "default", "etl-svc").PutObject(ctx, &awss3.PutObjectInput{
		Bucket: bucket, Key: key, Body: bytes.NewReader([]byte("0123456789")),
	})
	require.NoError(t, err)
	path := filepath.Join(dir, "bronze", "c.bin")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("01234X6789"), 0o600))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))

	reader := g.client(t, "default", "analytics")
	for _, rng := range []*string{nil, ptrS("bytes=0-3")} {
		out, err := reader.GetObject(ctx, &awss3.GetObjectInput{Bucket: bucket, Key: key, Range: rng}, oneAttempt)
		require.Nil(t, out, "range %q", aws.ToString(rng))
		require.Equal(t, "InternalError", errorCode(err), "range %q: %v", aws.ToString(rng), err)
	}
	require.FileExists(t, path)
}
