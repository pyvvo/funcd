// Package acme is the certmagic-backed ACME TLS driver (ADR-0111, F74): automatic public
// certificates via an ACME CA (Let's Encrypt by default; a test directory URL for Pebble). It is
// the ONLY TLS driver that imports certmagic — the static (self-signed/provided) driver is pure
// stdlib, so a self-signed/provided deployment routes through no ACME automation. It satisfies the
// internal/edge/tls Provider interface structurally. Challenge: TLS-ALPN-01 on the same TLS listener
// (no extra HTTP-01 port), which is why TLSConfig returns certmagic's config UNMODIFIED (it carries
// the acme-tls/1 ALPN proto issuance needs).
package acme

import (
	"context"
	"crypto/tls"
	"log/slog"

	"github.com/caddyserver/certmagic"

	"github.com/pyvvo/funcd/api/fault"
)

const op = "edge.tls.acme"

// Driver manages ACME certificates via certmagic.
type Driver struct {
	cfg    *certmagic.Config
	cache  *certmagic.Cache
	logger *slog.Logger
}

// New builds the ACME driver. email is the ACME account email; caDir overrides the CA directory URL
// (empty ⇒ certmagic's default, Let's Encrypt production; set to a Pebble URL in the deferred lane);
// storageDir persists accounts + certs.
func New(email, caDir, storageDir string, logger *slog.Logger) (*Driver, error) {
	if logger == nil {
		logger = slog.Default()
	}
	d := &Driver{logger: logger.With("component", op)}
	d.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return d.cfg, nil },
	})
	cfg := certmagic.New(d.cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: storageDir},
	})
	tmpl := certmagic.ACMEIssuer{Email: email, Agreed: true}
	if caDir != "" {
		tmpl.CA = caDir
	}
	cfg.Issuers = []certmagic.Issuer{certmagic.NewACMEIssuer(cfg, tmpl)}
	d.cfg = cfg
	return d, nil
}

// TLSConfig returns certmagic's config UNMODIFIED — it advertises h2 + http/1.1 PLUS acme-tls/1 for
// the TLS-ALPN-01 challenge. Overriding NextProtos would break issuance.
func (d *Driver) TLSConfig() (*tls.Config, error) { return d.cfg.TLSConfig(), nil }

// Manage obtains + renews (async) certificates for hosts. An empty host set is a no-op (nothing to
// obtain yet; certs are obtained on demand as Routes add hosts).
func (d *Driver) Manage(ctx context.Context, hosts []string) error {
	nonEmpty := hosts[:0]
	for _, h := range hosts {
		if h != "" {
			nonEmpty = append(nonEmpty, h)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	if err := d.cfg.ManageAsync(ctx, nonEmpty); err != nil {
		return fault.Unavailablef(op, "manage acme certs: %v", err)
	}
	return nil
}

// Close stops certmagic's background maintenance goroutine (renewal) so it does not leak past shutdown.
func (d *Driver) Close(context.Context) error {
	d.cache.Stop()
	return nil
}
