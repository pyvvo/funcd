package multinode

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const (
	caCertFile   = "ca.crt"
	caKeyFile    = "ca.key"
	caLifetime   = 10 * 365 * 24 * time.Hour
	certLifetime = 365 * 24 * time.Hour
	certBackdate = time.Hour
	// renewWithin is the end of a certificate's life in which it is renewed.
	renewWithin = certLifetime / 3
)

// RenewalDue reports whether a certificate that ends at notAfter is to be renewed.
func RenewalDue(notAfter, now time.Time) bool { return now.After(notAfter.Add(-renewWithin)) }

// CA is the cluster's certificate authority (FEAT-0002 decision 2). It signs the node API's server certificate and each
// node's client certificate; its key stays in the control plane's cluster directory.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

// LoadOrCreateCA reads the CA from dir, or makes one there when dir holds none.
func LoadOrCreateCA(dir string, now time.Time) (*CA, error) {
	const op = "multinode.LoadOrCreateCA"
	certPEM, err := os.ReadFile(filepath.Join(dir, caCertFile)) //nolint:gosec // the operator's cluster directory
	switch {
	case err == nil:
		keyPEM, kerr := os.ReadFile(filepath.Join(dir, caKeyFile)) //nolint:gosec // the operator's cluster directory
		if kerr != nil {
			return nil, fault.Wrapf(kerr, fault.Internal, op, "read the CA key")
		}
		return parseCA(certPEM, keyPEM)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fault.Wrapf(err, fault.Internal, op, "read the CA certificate")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "generate the CA key")
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "funcd cluster CA"},
		NotBefore: now.Add(-certBackdate), NotAfter: now.Add(caLifetime),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "sign the CA certificate")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "encode the CA key")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create %s", dir)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "write the CA key")
	}
	if err := os.WriteFile(filepath.Join(dir, caCertFile), certPEM, 0o600); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "write the CA certificate")
	}
	return parseCA(certPEM, keyPEM)
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	const op = "multinode.parseCA"
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, fault.Invalidf(op, "the CA certificate or key is not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "parse the CA certificate")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "parse the CA key")
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fault.Invalidf(op, "the CA key is not ECDSA")
	}
	return &CA{cert: cert, key: key, pem: certPEM}, nil
}

// Hash is the SHA-256 of the CA certificate, in hexadecimal: the value a join token carries, so a node checks it reached
// the real control plane before it sends the token.
func (c *CA) Hash() string { return CertHash(c.cert.Raw) }

// CertHash is the SHA-256 of a DER certificate, in hexadecimal.
func CertHash(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// PEM is the CA certificate, PEM.
func (c *CA) PEM() []byte { return c.pem }

// Pool holds the CA certificate alone.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// ServerCertificate signs a fresh certificate for the node API, for hosts (names or addresses) and loopback; its chain
// carries the CA, so a joining node can check the CA's hash.
func (c *CA) ServerCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	const op = "multinode.ServerCertificate"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fault.Wrapf(err, fault.Internal, op, "generate the key")
	}
	serial, err := newSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "funcd node API"},
		NotBefore: now.Add(-certBackdate), NotAfter: now.Add(certLifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fault.Wrapf(err, fault.Internal, op, "sign the certificate")
	}
	return tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}, nil
}

// SignNode signs a node's CSR as its client certificate, for node alone whatever the CSR asks, and returns the
// certificate PEM, its serial in hexadecimal and its expiry.
func (c *CA) SignNode(csrPEM []byte, node v1.ObjectName, now time.Time) ([]byte, string, time.Time, error) {
	const op = "multinode.SignNode"
	b, _ := pem.Decode(csrPEM)
	if b == nil {
		return nil, "", time.Time{}, fault.Invalidf(op, "the CSR is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fault.Wrapf(err, fault.Invalid, op, "parse the CSR")
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fault.Wrapf(err, fault.Invalid, op, "check the CSR's signature")
	}
	serial, err := newSerial()
	if err != nil {
		return nil, "", time.Time{}, err
	}
	notAfter := now.Add(certLifetime)
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: string(node), Organization: []string{"funcd-node"}},
		NotBefore: now.Add(-certBackdate), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, "", time.Time{}, fault.Wrapf(err, fault.Internal, op, "sign the node certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), serial.Text(16), notAfter, nil
}

// ServerTLS is the node API's TLS (FEAT-0002 decision 2): a certificate for hosts that the CA signs, signed again before
// it ends, and the certificates of the nodes the CA signed, verified when a node gives one: the join comes with none.
func (c *CA) ServerTLS(hosts []string, now func() time.Time) (*tls.Config, error) {
	cert := &serverCertificate{ca: c, hosts: hosts, now: now}
	if _, err := cert.get(nil); err != nil {
		return nil, err
	}
	return &tls.Config{GetCertificate: cert.get, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: c.Pool(),
		MinVersion: tls.VersionTLS13}, nil
}

// serverCertificate is the node API's certificate, signed again before it ends.
type serverCertificate struct {
	ca    *CA
	hosts []string
	now   func() time.Time

	mu       sync.Mutex
	cert     *tls.Certificate
	notAfter time.Time
}

func (s *serverCertificate) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cert == nil || RenewalDue(s.notAfter, now) {
		cert, err := s.ca.ServerCertificate(s.hosts, now)
		if err != nil {
			return nil, err
		}
		s.cert, s.notAfter = &cert, now.Add(certLifetime)
	}
	return s.cert, nil
}

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "multinode.newSerial", "draw a serial")
	}
	return n, nil
}
