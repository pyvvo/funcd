package gocloud_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// A static key (ADR-0222 Decision 6) signs every request alone; it applies to s3:// only, not beside a credentials
// file, and needs an id and a secret.
func TestOpenWithStaticKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, nil)
	b, err := gocloud.OpenWith(ctx, s.URL("prefix=orders/"), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Put(ctx, "a", []byte("x"), blob.PutOptions{IfNotExist: true}))
	require.Equal(t, []string{"orders/a"}, s.Keys("orders/"))
	_, auth := s.Seen()
	require.NotEmpty(t, auth)
	for _, a := range auth {
		require.True(t, strings.Contains(a, "Credential=AKIDOWNER/"), a)
	}
	for name, c := range map[string]struct {
		url  string
		opts gocloud.OpenOptions
	}{
		"file":   {gocloud.FileURL(t.TempDir()), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "a", Secret: "s"}}},
		"both":   {s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "a", Secret: "s"}, CredentialsFile: s3stub.CredentialsFile(t, "AKID")}},
		"no key": {s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "a"}}},
	} {
		_, err := gocloud.OpenWith(ctx, c.url, c.opts)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}
}
