package acme_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge/tls/acme"
)

// scenario: acme-config-built — the driver builds a well-formed certmagic config for a CA directory
// with NO network dial (live issuance is the deferred Pebble lane). The config keeps certmagic's own
// ALPN (acme-tls/1 for the TLS-ALPN-01 challenge), which the provider must not override.
func TestScenarioACMEConfigBuilt(t *testing.T) {
	d, err := acme.New("ops@example.com", "https://acme.example/directory", t.TempDir(), nil)
	require.NoError(t, err)

	cfg, err := d.TLSConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.GetCertificate, "certmagic config is GetCertificate-driven")
	require.Contains(t, cfg.NextProtos, "acme-tls/1", "TLS-ALPN-01 challenge ALPN must be preserved")

	// Manage with no hosts is a no-op (nothing to obtain yet); no dial.
	require.NoError(t, d.Manage(context.Background(), nil))
	require.NoError(t, d.Manage(context.Background(), []string{""}))

	// Close stops the renewal goroutine cleanly.
	require.NoError(t, d.Close(context.Background()))
}

func TestACMEDefaultCA(t *testing.T) {
	// Empty CA dir ⇒ certmagic's default (Let's Encrypt); still builds without dialing.
	d, err := acme.New("ops@example.com", "", t.TempDir(), nil)
	require.NoError(t, err)
	cfg, err := d.TLSConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NoError(t, d.Close(context.Background()))
}
