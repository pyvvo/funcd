// Package edgetls is the TLS termination provider port (ADR-0111, F74): it builds the *tls.Config
// funcd attaches to its own http.Servers (no listener handover — the caller drives ServeTLS). Three
// modes select a driver: `selfsigned` + `provided` are pure stdlib (internal/edge/tls/static);
// `acme` is certmagic (internal/edge/tls/acme) — the only mode that pulls the ACME dependency. The
// drivers satisfy Provider structurally (they do not import this package), so there is no cycle.
package edgetls

import (
	"context"
	"crypto/tls"
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/edge/tls/acme"
	"github.com/pyvvo/funcd/internal/edge/tls/static"
)

// Mode selects the certificate issuance strategy.
type Mode string

const (
	// ModeSelfSigned generates + persists one multi-SAN self-signed cert (stdlib). The default when
	// TLS is enabled: offline, zero-config (homebox/LAN). Browser-untrusted by design.
	ModeSelfSigned Mode = "selfsigned"
	// ModeProvided serves an operator-supplied cert + key (stdlib).
	ModeProvided Mode = "provided"
	// ModeACME obtains automatic public certificates via certmagic/ACME.
	ModeACME Mode = "acme"
)

// Spec configures the provider.
type Spec struct {
	Mode       Mode
	Hosts      []string // configured hosts, merged with the F79 Router.Hosts() at Manage time
	CertFile   string   // provided: PEM cert (chain)
	KeyFile    string   // provided: PEM key
	Email      string   // acme: ACME account email
	CADir      string   // acme: ACME directory URL (empty ⇒ Let's Encrypt; a Pebble URL in tests)
	StorageDir string   // persistence dir (self-signed cert / acme accounts+certs); required by selfsigned and acme
}

// Provider builds the *tls.Config funcd attaches to its http.Servers (no listener handover).
type Provider interface {
	// TLSConfig returns the config to attach to an http.Server (GetCertificate-driven). static sets
	// NextProtos=[h2,http/1.1]; acme returns certmagic's config unmodified (keeps acme-tls/1).
	TLSConfig() (*tls.Config, error)
	// Manage ensures/authorizes certs for the host set (Router.Hosts() + Spec.Hosts).
	Manage(ctx context.Context, hosts []string) error
	// Close halts background work (acme renewal goroutine); no-op for static. Wired into shutdown.
	Close(ctx context.Context) error
}

// New builds the provider for a Spec, selecting the driver by Mode. An empty Mode defaults to
// selfsigned (the enabled-TLS default).
func New(spec Spec, logger *slog.Logger) (Provider, error) {
	switch spec.Mode {
	case "", ModeSelfSigned:
		if spec.StorageDir == "" {
			return nil, fault.Invalidf("edgetls.New", "mode selfsigned requires a storage dir")
		}
		return static.New(true, "", "", spec.StorageDir, logger), nil
	case ModeProvided:
		if spec.CertFile == "" || spec.KeyFile == "" {
			return nil, fault.Invalidf("edgetls.New", "mode provided requires certFile and keyFile")
		}
		return static.New(false, spec.CertFile, spec.KeyFile, "", logger), nil
	case ModeACME:
		if spec.Email == "" {
			return nil, fault.Invalidf("edgetls.New", "mode acme requires an account email")
		}
		if spec.StorageDir == "" {
			return nil, fault.Invalidf("edgetls.New", "mode acme requires a storage dir")
		}
		return acme.New(spec.Email, spec.CADir, spec.StorageDir, logger)
	default:
		return nil, fault.Invalidf("edgetls.New", "unknown TLS mode %q (selfsigned|provided|acme)", spec.Mode)
	}
}
