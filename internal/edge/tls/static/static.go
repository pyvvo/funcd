// Package static is the stdlib TLS driver (ADR-0111, F74): it builds a *tls.Config from a static
// certificate — either operator-PROVIDED (tls.LoadX509KeyPair) or SELF-SIGNED (one multi-SAN cert
// this package generates with crypto/x509 and persists under the storage dir). Pure stdlib — it
// imports no ACME/certmagic code, so a self-signed/provided deployment pays for no TLS-automation
// dependency. It satisfies the internal/edge/tls Provider interface structurally (no import of the
// facade → no cycle).
package static

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
)

const op = "edge.tls.static"

// Driver serves a single static certificate (provided or self-signed).
type Driver struct {
	selfSigned bool
	certFile   string
	keyFile    string
	storageDir string
	logger     *slog.Logger

	mu   sync.RWMutex
	cert *tls.Certificate
}

// New builds a static driver. selfSigned=false ⇒ load certFile/keyFile (provided); selfSigned=true
// ⇒ generate/persist a multi-SAN cert under storageDir at Manage time.
func New(selfSigned bool, certFile, keyFile, storageDir string, logger *slog.Logger) *Driver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Driver{selfSigned: selfSigned, certFile: certFile, keyFile: keyFile, storageDir: storageDir, logger: logger.With("component", op)}
}

// TLSConfig returns a GetCertificate-driven config advertising h2 + http/1.1.
func (d *Driver) TLSConfig() (*tls.Config, error) {
	return &tls.Config{
		GetCertificate: d.getCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}, nil
}

func (d *Driver) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.cert == nil {
		return nil, fault.Unavailablef(op, "no certificate loaded (call Manage first)")
	}
	return d.cert, nil
}

// Manage loads the provided cert, or loads-or-generates the self-signed one. hosts is the SAN set
// (F79 Router.Hosts() + configured hosts). Idempotent: a persisted self-signed cert whose SANs
// already cover hosts is reused (so a restart keeps the same cert — the selfsigned-persists rule);
// otherwise one multi-SAN cert is generated and persisted.
func (d *Driver) Manage(_ context.Context, hosts []string) error {
	if !d.selfSigned {
		cert, err := tls.LoadX509KeyPair(d.certFile, d.keyFile)
		if err != nil {
			return fault.Invalidf(op, "load provided cert/key: %v", err)
		}
		d.set(&cert)
		return nil
	}

	certPath := filepath.Join(d.storageDir, "cert.pem")
	keyPath := filepath.Join(d.storageDir, "key.pem")
	if cert, ok := loadIfCovers(certPath, keyPath, hosts); ok {
		d.set(cert)
		d.logger.Debug("reusing persisted self-signed certificate", "hosts", hosts)
		return nil
	}
	cert, certPEM, keyPEM, err := generateSelfSigned(hosts)
	if err != nil {
		return fault.Internalf(op, "generate self-signed cert: %v", err)
	}
	if err := persist(d.storageDir, certPath, keyPath, certPEM, keyPEM); err != nil {
		return err
	}
	d.set(cert)
	d.logger.Info("generated self-signed certificate", "hosts", hosts, "dir", d.storageDir)
	return nil
}

// Close is a no-op for static (no background work).
func (d *Driver) Close(context.Context) error { return nil }

func (d *Driver) set(c *tls.Certificate) {
	d.mu.Lock()
	d.cert = c
	d.mu.Unlock()
}

// loadIfCovers loads a persisted cert/key iff both files exist and the cert's SANs cover every host.
func loadIfCovers(certPath, keyPath string, hosts []string) (*tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, false
	}
	sans := map[string]bool{}
	for _, n := range leaf.DNSNames {
		sans[n] = true
	}
	for _, ip := range leaf.IPAddresses {
		sans[ip.String()] = true
	}
	for _, h := range hosts {
		if h != "" && !sans[h] {
			return nil, false
		}
	}
	cert.Leaf = leaf
	return &cert, true
}

// generateSelfSigned makes one multi-SAN self-signed cert (10-year, ECDSA P-256) covering hosts +
// loopback, and returns the parsed cert plus its PEM encodings for persistence.
func generateSelfSigned(hosts []string) (*tls.Certificate, []byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"funcd (self-signed)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	// Always cover loopback so a local client (tests, homebox self-calls) can connect.
	tmpl.IPAddresses = append(tmpl.IPAddresses, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	tmpl.DNSNames = append(tmpl.DNSNames, "localhost")

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	cert.Leaf = leaf
	return &cert, certPEM, keyPEM, nil
}

func persist(dir, certPath, keyPath string, certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fault.Internalf(op, "create tls dir: %v", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fault.Internalf(op, "write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fault.Internalf(op, "write key: %v", err)
	}
	return nil
}
