package gocloud_test

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// openHermeticS3 opens an s3blob bucket that signs with dummy environment credentials and never reaches a network:
// a set profile resolves before environment credentials (aws-sdk-go-v2/config resolve_credentials.go), so the
// profile is cleared and the config files point into an empty temp dir (ADR-0198 Test plan).
func openHermeticS3(t *testing.T) blob.Bucket {
	t.Helper()
	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":           "AKIDTEST",
		"AWS_SECRET_ACCESS_KEY":       "secret",
		"AWS_SESSION_TOKEN":           "",
		"AWS_PROFILE":                 "",
		"AWS_DEFAULT_PROFILE":         "",
		"AWS_CONFIG_FILE":             filepath.Join(dir, "config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(dir, "credentials"),
		"AWS_ENDPOINT_URL":            "",
		"AWS_ENDPOINT_URL_S3":         "",
		"AWS_EC2_METADATA_DISABLED":   "true",
	} {
		t.Setenv(k, v)
	}
	b, err := gocloud.Open(context.Background(), "s3://bkt?region=us-east-1&endpoint=http://127.0.0.1:9&use_path_style=true")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func amzExpires(t *testing.T, b blob.Bucket, opts blob.SignOptions) string {
	t.Helper()
	signed, err := b.SignedURL(context.Background(), "p/k", opts)
	require.NoError(t, err)
	u, err := url.Parse(signed)
	require.NoError(t, err)
	return u.Query().Get("X-Amz-Expires")
}

// scenario: valid-expiry-honoured (signer) — s3blob signs exactly the requested lifetime: 10m is 600 s, 1h30m 5400 s.
func TestScenarioValidExpiryHonouredS3Signer(t *testing.T) {
	b := openHermeticS3(t)
	require.Equal(t, "600", amzExpires(t, b, blob.SignOptions{Method: blob.SignPut, Expiry: 10 * time.Minute}))
	require.Equal(t, "5400", amzExpires(t, b, blob.SignOptions{Method: blob.SignGet, Expiry: 90 * time.Minute}))
}

// scenario: absent-expiry-defaults (signer) — a zero Expiry signs the 15-minute driver default, 900 s.
func TestScenarioAbsentExpiryDefaultsS3Signer(t *testing.T) {
	require.Equal(t, "900", amzExpires(t, openHermeticS3(t), blob.SignOptions{Method: blob.SignGet}))
}
