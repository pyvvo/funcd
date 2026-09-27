package static_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"

	"github.com/pyvvo/funcd/internal/edge/tls/static"
)

// serveTLS starts an httptest TLS server using cfg and returns it.
func serveTLS(t *testing.T, cfg *tls.Config) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.TLS = cfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// clientTrusting builds an HTTPS client that trusts cfg's leaf cert and uses serverName for SNI+verify.
func clientTrusting(t *testing.T, cfg *tls.Config, serverName string) *http.Client {
	t.Helper()
	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: serverName})
	require.NoError(t, err)
	leaf := cert.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName}}}
}

// scenario: selfsigned-serves-https
func TestScenarioSelfSignedServesHTTPS(t *testing.T) {
	d := static.New(true, "", "", t.TempDir(), nil)
	require.NoError(t, d.Manage(context.Background(), []string{"funcd.local"}))
	cfg, err := d.TLSConfig()
	require.NoError(t, err)
	srv := serveTLS(t, cfg)
	resp, err := clientTrusting(t, cfg, "funcd.local").Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.TLS)
}

// scenario: selfsigned-persists
func TestScenarioSelfSignedPersists(t *testing.T) {
	dir := t.TempDir()
	d1 := static.New(true, "", "", dir, nil)
	require.NoError(t, d1.Manage(context.Background(), []string{"funcd.local"}))
	first, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	require.NoError(t, err)

	// A fresh driver over the same dir + same hosts reuses the persisted cert (no regeneration).
	d2 := static.New(true, "", "", dir, nil)
	require.NoError(t, d2.Manage(context.Background(), []string{"funcd.local"}))
	second, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	require.NoError(t, err)
	require.Equal(t, first, second, "a restart with the same hosts reuses the persisted cert")
}

// scenario: sni-serves-route-hosts — one multi-SAN cert covers every host.
func TestScenarioSNIServesRouteHosts(t *testing.T) {
	d := static.New(true, "", "", t.TempDir(), nil)
	require.NoError(t, d.Manage(context.Background(), []string{"a.example.com", "b.example.com"}))
	cfg, _ := d.TLSConfig()
	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	require.NoError(t, err)
	leaf := cert.Leaf
	require.NotNil(t, leaf)
	require.NoError(t, leaf.VerifyHostname("a.example.com"))
	require.NoError(t, leaf.VerifyHostname("b.example.com"), "one multi-SAN cert covers both route hosts")
}

// scenario: selfsigned regenerates when a new host is not covered.
func TestSelfSignedRegeneratesOnNewHost(t *testing.T) {
	dir := t.TempDir()
	d := static.New(true, "", "", dir, nil)
	require.NoError(t, d.Manage(context.Background(), []string{"a.example.com"}))
	first, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	require.NoError(t, d.Manage(context.Background(), []string{"a.example.com", "c.example.com"}))
	second, _ := os.ReadFile(filepath.Join(dir, "cert.pem"))
	require.NotEqual(t, first, second, "a host not in the persisted cert's SANs triggers regeneration")
}

// scenario: provided-serves-https
func TestScenarioProvidedServesHTTPS(t *testing.T) {
	dir := t.TempDir()
	// Generate a cert via the selfsigned path, then feed its files to the provided path.
	gen := static.New(true, "", "", dir, nil)
	require.NoError(t, gen.Manage(context.Background(), []string{"provided.local"}))

	d := static.New(false, filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), "", nil)
	require.NoError(t, d.Manage(context.Background(), nil))
	cfg, err := d.TLSConfig()
	require.NoError(t, err)
	srv := serveTLS(t, cfg)
	resp, err := clientTrusting(t, cfg, "provided.local").Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestProvidedBadCertIsInvalid(t *testing.T) {
	d := static.New(false, "/nonexistent/cert.pem", "/nonexistent/key.pem", "", nil)
	require.Error(t, d.Manage(context.Background(), nil), "a missing provided cert is an error, not a panic")
}

// scenario: alpn-negotiates-h2
func TestScenarioALPNNegotiatesH2(t *testing.T) {
	d := static.New(true, "", "", t.TempDir(), nil)
	require.NoError(t, d.Manage(context.Background(), []string{"h2.local"}))
	cfg, _ := d.TLSConfig()
	require.Contains(t, cfg.NextProtos, "h2")
	require.Contains(t, cfg.NextProtos, "http/1.1")

	// An h2-capable client negotiates h2 against a server using this config.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	srv.TLS = cfg
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := clientTrusting(t, cfg, "h2.local")
	require.NoError(t, http2.ConfigureTransport(client.Transport.(*http.Transport)))
	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, "HTTP/2.0", resp.Proto)
}
