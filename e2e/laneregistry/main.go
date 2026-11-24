// Command laneregistry serves one image, read from a `docker save` tar whose OCI layout holds it, gzipped or not, as a
// read-only OCI registry over TLS, so the addon-runtime lane's containerd pulls it by digest from a registry that
// addons.registries lists, as an Addon's runtime image is pulled (ADR-0344 Decision 5). Its certificate, for
// 127.0.0.1, is made at its start and written to cert-out for the guest to trust; the image's digest goes to
// digest-out.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

// image is an OCI layout read into memory: its blobs by digest, and the descriptor of index.json's first manifest.
type image struct {
	blobs map[string][]byte
	top   descriptor
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
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
	if err := os.WriteFile(digestOut, []byte(img.top.Digest+"\n"), 0o644); err != nil { //nolint:gosec // the guest reads it
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: img.handler(repository), ReadHeaderTimeout: headerTimeout,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	return srv.ServeTLS(ln, "", "")
}

// readLayout reads every blob and index.json of the tar at path.
func readLayout(path string) (*image, error) {
	f, err := os.Open(path) //nolint:gosec // the lane's staged image
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	var src io.Reader = r
	if magic, _ := r.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		src = zr
	}
	img := &image{blobs: map[string][]byte{}}
	var index []byte
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		hex, isBlob := strings.CutPrefix(name, "blobs/sha256/")
		if name != "index.json" && !isBlob {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		if isBlob {
			img.blobs["sha256:"+hex] = b
		} else {
			index = b
		}
	}
	var idx struct {
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(index, &idx); err != nil || len(idx.Manifests) == 0 {
		return nil, fmt.Errorf("%s holds no OCI index.json with a manifest", path)
	}
	img.top = idx.Manifests[0]
	return img, nil
}

// handler serves the distribution API's reads of repository: /v2/, its manifests by digest or by the tag latest, and
// its blobs by digest.
func (img *image) handler(repository string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, "/v2/"+repository+"/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch kind, ref, _ := strings.Cut(rest, "/"); kind {
		case "manifests":
			if ref == "latest" {
				ref = img.top.Digest
			}
			img.serve(w, r, ref, img.mediaType(ref))
		case "blobs":
			img.serve(w, r, ref, "application/octet-stream")
		default:
			http.NotFound(w, r)
		}
	})
}

// mediaType is the media type of the manifest or index of digest: its own mediaType field, else the index's.
func (img *image) mediaType(digest string) string {
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(img.blobs[digest], &m); err == nil && m.MediaType != "" {
		return m.MediaType
	}
	return img.top.MediaType
}

func (img *image) serve(w http.ResponseWriter, r *http.Request, digest, mediaType string) {
	b, ok := img.blobs[digest]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
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
