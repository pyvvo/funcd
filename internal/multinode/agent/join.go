// Package agent is a worker node's side of multi-node (FEAT-0002): the join that swaps a one-time token for the node's
// certificate, and the agent that keeps the node's local store a copy of the namespaces placed on it, writes their
// status and the children the node owns back, and answers the node calls. Every connection is the node's own: it dials
// the control plane, which never dials it.
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
)

// The node's identity files, in its cluster directory.
const (
	keyFile    = "node.key"
	certFile   = "node.crt"
	caFile     = "ca.crt"
	serverFile = "server"
	nameFile   = "name"
	cursorFile = "cursor"
)

// The join's and the node API client's bounds.
const (
	joinRequestTimeout = 30 * time.Second
	maxJoinAnswer      = 1 << 20
	idleConnTimeout    = 90 * time.Second
)

// Join swaps a one-time join token for this node's certificate (FEAT-0002 decision 2), and writes the node's identity to
// dir. Before it sends the token, it checks that the control plane at server presents a certificate chain whose CA has
// the hash the token carries, so a token never reaches an impostor.
func Join(ctx context.Context, server, token, dir, version, platform string) (v1.ObjectName, error) {
	const op = "agent.Join"
	tok, err := multinode.ParseToken(token)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fault.Invalidf(op, "the control plane's node API is an https:// URL, not %q", server)
	}
	var caDER []byte
	pin := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate pins the CA the token names, below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, der := range raw {
				if multinode.CertHash(der) == tok.CAHash {
					caDER = der
					return verifyChain(raw, der, u.Hostname())
				}
			}
			return fmt.Errorf("the control plane at %s does not present the CA the join token names", server)
		},
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "generate the node key")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: string(tok.Node)},
	}, key)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "make the CSR")
	}
	body, err := json.Marshal(multinode.JoinRequest{Token: tok.String(), Version: version, Platform: platform,
		CSR: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})})
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "marshal the join")
	}
	hc := &http.Client{Timeout: joinRequestTimeout, Transport: &http.Transport{TLSClientConfig: pin}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+multinode.PathJoin, bytes.NewReader(body))
	if err != nil {
		return "", fault.Wrapf(err, fault.Invalid, op, "build the join request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "reach the control plane at %s", server)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJoinAnswer))
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "read the join answer")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fault.Forbiddenf(op, "the control plane refused the join (%d): %s", resp.StatusCode, detail(data))
	}
	var out multinode.JoinResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "decode the join answer")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "encode the node key")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create %s", dir)
	}
	files := map[string][]byte{
		keyFile:    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		certFile:   out.Certificate,
		caFile:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		serverFile: []byte(strings.TrimRight(server, "/") + "\n"),
		nameFile:   []byte(string(tok.Node) + "\n"),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "write %s", name)
		}
	}
	_ = os.Remove(filepath.Join(dir, cursorFile))
	return tok.Node, nil
}

// verifyChain checks the presented leaf against the pinned CA, for host.
func verifyChain(raw [][]byte, caDER []byte, host string) error {
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// JoinedName is the name this node joined under, from its cluster directory dir.
func JoinedName(dir string) (v1.ObjectName, error) {
	b, err := os.ReadFile(filepath.Join(dir, nameFile)) //nolint:gosec // the node's own cluster directory
	if err != nil {
		return "", fault.Wrapf(err, fault.Invalid, "agent.JoinedName", "this node has not joined (read %s); run funcd node join", nameFile)
	}
	return v1.ObjectName(strings.TrimSpace(string(b))), nil
}

// identity is what a joined node reads back from its cluster directory.
type identity struct {
	name   v1.ObjectName
	server string
	client *http.Client
}

func loadIdentity(dir string) (identity, error) {
	const op = "agent.loadIdentity"
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // the node's own cluster directory
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "this node has not joined (read %s); run funcd node join", name)
		}
		return b, nil
	}
	name, err := read(nameFile)
	if err != nil {
		return identity{}, err
	}
	server, err := read(serverFile)
	if err != nil {
		return identity{}, err
	}
	caPEM, err := read(caFile)
	if err != nil {
		return identity{}, err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err != nil {
		return identity{}, fault.Wrapf(err, fault.Invalid, op, "load the node certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return identity{}, fault.Invalidf(op, "%s holds no certificate", caFile)
	}
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, MinVersion: tls.VersionTLS13},
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     idleConnTimeout,
	}
	return identity{name: v1.ObjectName(strings.TrimSpace(string(name))), server: strings.TrimSpace(string(server)),
		client: &http.Client{Transport: tr}}, nil
}
