package longpoll_test

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/nodeapi/longpoll"
	"github.com/pyvvo/funcd/internal/nodeapi/nodeapicontract"
)

// listen is the driver's control-plane side on loopback, under a CA made for the test, and that CA.
func listen(t *testing.T) (nodeapi.Server, *multinode.CA) {
	t.Helper()
	ca, err := multinode.LoadOrCreateCA(t.TempDir(), time.Now())
	require.NoError(t, err)
	tlsCfg, err := ca.ServerTLS(nil, time.Now)
	require.NoError(t, err)
	srv, err := longpoll.Listen(longpoll.Config{Addr: "127.0.0.1:0", TLS: tlsCfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ca
}

// identity is node's identity under a certificate ca signed.
func identity(t *testing.T, ca *multinode.CA, server string, node v1.ObjectName) nodeapi.Identity {
	t.Helper()
	sign := func(csr []byte, node v1.ObjectName) ([]byte, error) {
		cert, _, _, err := ca.SignNode(csr, node, time.Now())
		return cert, err
	}
	return nodeapicontract.NodeIdentity(t, sign, ca.Pool(), server, node)
}

// scenario: driver-conformance — the HTTPS long poll carries the node API as the port's contract says.
func TestScenarioLongPollPassesTheNodeAPIContract(t *testing.T) {
	nodeapicontract.RunContract(t, func(t *testing.T) nodeapicontract.Driver {
		srv, ca := listen(t)
		block, _ := pem.Decode(ca.PEM())
		require.NotNil(t, block)
		url := "https://" + srv.Addr()
		return nodeapicontract.Driver{Server: srv, Connector: longpoll.NewConnector(), URL: url, CA: block.Bytes,
			Identity: func(node v1.ObjectName) nodeapi.Identity { return identity(t, ca, url, node) }}
	})
}

// admitAll is a Handler that lets every node in and answers nothing else.
type admitAll struct{ nodeapi.Handler }

func (admitAll) Admit(context.Context, nodeapi.Peer) error { return nil }

// A node on a node API version this control plane does not serve gets 426, whatever it sends (ADR-0374 Decision 1).
func TestANodeOnAnotherNodeAPIVersionGets426(t *testing.T) {
	srv, ca := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, admitAll{}) }()
	id := identity(t, ca, "https://"+srv.Addr(), "vm1")
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{id.Certificate},
		RootCAs: id.Roots, MinVersion: tls.VersionTLS13}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id.Server+"/node/v1/children", nil)
	require.NoError(t, err)
	req.Header.Set("Funcd-Node-Api", "99")
	var resp *http.Response
	require.Eventually(t, func() bool {
		r, err := hc.Do(req.Clone(ctx))
		if err != nil {
			return false
		}
		resp = r
		return true
	}, 5*time.Second, 20*time.Millisecond)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUpgradeRequired, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "serves node API versions 1, and the node speaks 99")

	join, err := http.NewRequestWithContext(ctx, http.MethodPost, id.Server+"/node/v1/join", strings.NewReader(`{"token":"t"}`))
	require.NoError(t, err)
	join.Header.Set("Funcd-Node-Api", "99")
	answer, err := hc.Do(join)
	require.NoError(t, err)
	defer func() { _ = answer.Body.Close() }()
	require.Equal(t, http.StatusUpgradeRequired, answer.StatusCode, "a join too, before its token is read")
}
