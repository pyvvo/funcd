// Package agent is a worker node's side of multi-node (FEAT-0002): the join that swaps a one-time token for the node's
// certificate, and the agent that keeps the node's local store a copy of the namespaces placed on it, writes their
// status and the children the node owns back, and answers the node calls. It speaks to the control plane through the
// node API port (internal/nodeapi): every connection is the node's own, and the control plane never dials it.
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
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// The node's identity files, in its cluster directory.
const (
	identityFile = "node.pem" // the node certificate and its key, in one file that a renewal replaces at once
	caFile       = "ca.crt"
	serverFile   = "server"
	nameFile     = "name"
	cursorFile   = "cursor"
)

// Join swaps a one-time join token for this node's certificate (ADR-0364 Decision 2), through conn, the node side of the
// node API's driver, and writes the node's identity to dir. Before it sends the token, conn checks that the control plane
// at server presents a certificate chain whose CA has the hash the token carries, so a token never reaches an impostor.
func Join(ctx context.Context, conn nodeapi.Connector, server, token, dir, version, platform string) (v1.ObjectName, error) {
	const op = "agent.Join"
	tok, err := multinode.ParseToken(token)
	if err != nil {
		return "", err
	}
	key, csr, err := newKey(tok.Node)
	if err != nil {
		return "", err
	}
	trusted := func(caDER []byte) bool { return multinode.CertHash(caDER) == tok.CAHash }
	out, caDER, err := conn.Join(ctx, server, trusted, nodeapi.JoinRequest{Token: tok.String(), Version: version, Platform: platform, CSR: csr})
	if err != nil {
		return "", err
	}
	id, err := identityPEM(out.Certificate, key)
	if err != nil {
		return "", err
	}
	if err := (durable.Disk{}).MkdirAll(dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create %s", dir)
	}
	// The name goes first and comes back last, each file whole: loadIdentity reads it first, so a join a crash cut
	// short reads as no join, a first one and a rejoin alike (ADR-0364 Decision 2).
	if err := os.Remove(filepath.Join(dir, nameFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fault.Wrapf(err, fault.Internal, op, "remove %s", nameFile)
	}
	for _, f := range []struct {
		name string
		b    []byte
	}{
		{identityFile, id},
		{caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})},
		{serverFile, []byte(strings.TrimRight(server, "/") + "\n")},
		{nameFile, []byte(string(tok.Node) + "\n")},
	} {
		if err := (durable.Disk{}).WriteFile(dir, f.name, f.b, 0o600); err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "write %s", f.name)
		}
	}
	joinedAgain(dir)
	return tok.Node, nil
}

// newKey makes a node key, and its CSR for node.
func newKey(node v1.ObjectName) (*ecdsa.PrivateKey, []byte, error) {
	const op = "agent.newKey"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "generate the node key")
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: string(node)}}, key)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "make the CSR")
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// identityPEM is the node certificate with its key.
func identityPEM(certPEM []byte, key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "agent.identityPEM", "encode the node key")
	}
	return append(bytes.Clone(certPEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})...), nil
}

// joinedAgain makes a node that joins anew start from a whole snapshot; its store, if it still has it, keeps the runs the
// node made, which the control plane takes back.
func joinedAgain(dir string) {
	cursor := filepath.Join(dir, cursorFile)
	b, err := os.ReadFile(cursor) //nolint:gosec // the node's own cluster directory
	if err != nil {
		return
	}
	if f := strings.Fields(string(b)); len(f) == 3 {
		if os.WriteFile(cursor, []byte(restoredMark+" 0 "+f[2]+"\n"), 0o600) == nil {
			return
		}
	}
	_ = os.Remove(cursor)
}

// JoinedName is the name this node joined under, from its cluster directory dir.
func JoinedName(dir string) (v1.ObjectName, error) {
	b, err := os.ReadFile(filepath.Join(dir, nameFile)) //nolint:gosec // the node's own cluster directory
	if err != nil {
		return "", fault.Wrapf(err, fault.Invalid, "agent.JoinedName", "this node has not joined (read %s); run funcd node join", nameFile)
	}
	return v1.ObjectName(strings.TrimSpace(string(b))), nil
}

// identity is what a joined node reads back from its cluster directory, and the client its certificate makes.
type identity struct {
	name     v1.ObjectName
	server   string
	roots    *x509.CertPool
	notAfter time.Time
	client   nodeapi.Client
}

// loadIdentity reads back the identity of the node that joined under dir, and its certificate with its key.
func loadIdentity(dir string) (identity, []byte, error) {
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
		return identity{}, nil, err
	}
	server, err := read(serverFile)
	if err != nil {
		return identity{}, nil, err
	}
	caPEM, err := read(caFile)
	if err != nil {
		return identity{}, nil, err
	}
	pair, err := read(identityFile)
	if err != nil {
		return identity{}, nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return identity{}, nil, fault.Invalidf(op, "%s holds no certificate", caFile)
	}
	return identity{name: v1.ObjectName(strings.TrimSpace(string(name))), server: strings.TrimSpace(string(server)), roots: roots}, pair, nil
}

// with is id under the certificate and key of pair, with a client of its own that conn dials.
func (id identity) with(conn nodeapi.Connector, pair []byte, version, platform string) (identity, error) {
	const op = "agent.identity"
	cert, err := tls.X509KeyPair(pair, pair)
	if err != nil {
		return identity{}, fault.Wrapf(err, fault.Invalid, op, "load the node certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return identity{}, fault.Wrapf(err, fault.Invalid, op, "parse the node certificate")
	}
	id.notAfter = leaf.NotAfter
	if id.client, err = conn.Dial(nodeapi.Identity{Server: id.server, Certificate: cert, Roots: id.roots, Version: version,
		Platform: platform}); err != nil {
		return identity{}, err
	}
	return id, nil
}
