// Command laneregistry serves one image, read from a `docker save` tar whose OCI layout holds it, gzipped or not, as a
// read-only OCI registry over TLS, so the addon-runtime lane's containerd pulls it by digest from a registry that
// addons.registries lists, as an Addon's runtime image is pulled (ADR-0344 Decision 5). Its certificate, for
// 127.0.0.1, is made at its start and written to cert-out for the guest to trust; the image's digest goes to
// digest-out.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pyvvo/funcd/internal/testkit/imageregistry"
)

// headerTimeout bounds the read of a request's header.
const headerTimeout = 10 * time.Second

// certLifetime is how long the made certificate is valid: one lane.
const certLifetime = 24 * time.Hour

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: laneregistry <image.tar> <repository> <listen-addr> <cert-out> <digest-out>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
		fmt.Fprintln(os.Stderr, "laneregistry:", err)
		os.Exit(1)
	}
}

func run(tarPath, repository, addr, certOut, digestOut string) error {
	img, err := readLayout(tarPath)
	if err != nil {
		return err
	}
	cert, err := selfSigned(certOut)
	if err != nil {
		return err
	}
	if err := os.WriteFile(digestOut, []byte(img.Digest()+"\n"), 0o644); err != nil { //nolint:gosec // the guest reads it
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: img.Handler(repository), ReadHeaderTimeout: headerTimeout,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	return srv.ServeTLS(ln, "", "")
}

// readLayout reads the image of the tar at path.
func readLayout(path string) (*imageregistry.Image, error) {
	f, err := os.Open(path) //nolint:gosec // the lane's staged image
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, err := imageregistry.Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// selfSigned makes a self-signed certificate for 127.0.0.1, which may sign itself as a root, and writes it to out.
func selfSigned(out string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "funcd lane registry"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(certLifetime),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(out, certPEM, 0o644); err != nil { //nolint:gosec // a public certificate the guest trusts
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}
