//go:build e2e

package funcd_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	edgetls "github.com/green-0-rabbit/funcd/internal/edge/tls"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
)

func bringUp(t *testing.T, opts ...funcd.Option) *funcd.Platform {
	t.Helper()
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	base := []funcd.Option{
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"), funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithArtifactStore(t.TempDir()),
	}
	p, err := funcd.New(append(base, opts...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })
	return p
}

// scenario: tls-invoke-e2e — a funcd with selfsigned TLS serves the data-plane listener over HTTPS.
// The handshake completes with a client trusting the generated cert, and the data-plane handler runs
// over TLS (a 404 for an unknown function is a valid data-plane response — the point is `resp.TLS`).
func TestScenarioE2ETLSSelfSignedServesHTTPS(t *testing.T) {
	dir := t.TempDir()
	p := bringUp(t, funcd.WithTLS(edgetls.Spec{Mode: edgetls.ModeSelfSigned, Hosts: []string{"127.0.0.1"}, StorageDir: dir}))

	// Wait for the persisted cert, then build a client trusting it.
	var pool *x509.CertPool
	require.Eventually(t, func() bool {
		pem, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
		if err != nil {
			return false
		}
		pool = x509.NewCertPool()
		return pool.AppendCertsFromPEM(pem)
	}, 10*time.Second, 100*time.Millisecond, "selfsigned cert persisted")

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}}}
	resp, err := client.Get("https://" + p.DataPlaneAddr() + "/function/none")
	require.NoError(t, err, "the HTTPS handshake against the self-signed cert must succeed")
	defer func() { _ = resp.Body.Close() }()
	require.NotNil(t, resp.TLS, "the data-plane listener terminated TLS")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "the data-plane handler ran over TLS (unknown function → 404)")

	// The control-plane listener also serves HTTPS (both listeners get the *tls.Config).
	cpResp, err := client.Get("https://" + p.Addr() + "/healthz")
	require.NoError(t, err, "the control-plane also terminates TLS")
	defer func() { _ = cpResp.Body.Close() }()
	require.NotNil(t, cpResp.TLS, "control-plane over TLS")
}

// scenario: plaintext-opt-out — with no WithTLS, both listeners serve plain HTTP (back-compat).
func TestScenarioE2EPlaintextDefault(t *testing.T) {
	p := bringUp(t) // no WithTLS
	resp, err := http.Get("http://" + p.DataPlaneAddr() + "/function/none")
	require.NoError(t, err, "the default is plaintext HTTP")
	defer func() { _ = resp.Body.Close() }()
	require.Nil(t, resp.TLS, "no TLS by default")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
