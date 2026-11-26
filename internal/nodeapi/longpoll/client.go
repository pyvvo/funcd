package longpoll

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// The node side's bounds: requestTimeout bounds one request, above a long poll's wait, so a connection that died unseen
// is dialed again.
const (
	requestTimeout     = time.Minute
	joinRequestTimeout = 30 * time.Second
	maxJoinAnswer      = 1 << 20
	idleConnTimeout    = 90 * time.Second
)

type connector struct{}

// NewConnector is the node side of the HTTPS long poll.
func NewConnector() nodeapi.Connector { return connector{} }

func (connector) Join(ctx context.Context, server string, trusted func(caDER []byte) bool, in nodeapi.JoinRequest) (nodeapi.JoinResponse, []byte, error) {
	const op = "longpoll.Join"
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nodeapi.JoinResponse{}, nil, fault.Invalidf(op, "the control plane's node API is an https:// URL, not %q", server)
	}
	var caDER []byte
	pin := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate pins the CA that trusted accepts, below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, der := range raw {
				if trusted(der) {
					caDER = der
					return verifyChain(raw, der, u.Hostname())
				}
			}
			return fmt.Errorf("the control plane at %s does not present the CA the join token names", server)
		},
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Internal, op, "marshal the join")
	}
	hc := &http.Client{Timeout: joinRequestTimeout, Transport: &http.Transport{TLSClientConfig: pin}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+pathJoin, bytes.NewReader(body))
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Invalid, op, "build the join request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Unavailable, op, "reach the control plane at %s", server)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJoinAnswer))
	if err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Unavailable, op, "read the join answer")
	}
	if resp.StatusCode != http.StatusOK {
		return nodeapi.JoinResponse{}, nil, fault.Forbiddenf(op, "the control plane refused the join (%d): %s", resp.StatusCode, detail(data))
	}
	var out nodeapi.JoinResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nodeapi.JoinResponse{}, nil, fault.Wrapf(err, fault.Internal, op, "decode the join answer")
	}
	return out, caDER, nil
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

func (connector) Dial(id nodeapi.Identity) (nodeapi.Client, error) {
	if id.Server == "" || id.Roots == nil || len(id.Certificate.Certificate) == 0 {
		return nil, fault.Invalidf("longpoll.Dial", "a control plane, the cluster CA and a node certificate are required")
	}
	return &client{id: id, hc: &http.Client{Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{Certificates: []tls.Certificate{id.Certificate}, RootCAs: id.Roots, MinVersion: tls.VersionTLS13},
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     idleConnTimeout,
	}}}, nil
}

type client struct {
	id nodeapi.Identity
	hc *http.Client
}

func (c *client) Snapshot(ctx context.Context, ns v1.NamespaceName) (nodeapi.Snapshot, error) {
	path := pathSnapshot
	if ns != "" {
		path += "?namespace=" + url.QueryEscape(string(ns))
	}
	var out nodeapi.Snapshot
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *client) Feed(ctx context.Context, since uint64, timeline string, wait time.Duration) (nodeapi.Feed, error) {
	var out nodeapi.Feed
	path := pathFeed + "?since=" + strconv.FormatUint(since, 10) + "&timeline=" + url.QueryEscape(timeline) + "&wait=" + wait.String()
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *client) Children(ctx context.Context) ([]store.Change, error) {
	var out []store.Change
	return out, c.do(ctx, http.MethodGet, pathChildren, nil, &out)
}

func (c *client) Status(ctx context.Context, in nodeapi.StatusWrite) error {
	return c.do(ctx, http.MethodPut, pathStatus, in, nil)
}

func (c *client) PutObject(ctx context.Context, in nodeapi.ObjectWrite) error {
	return c.do(ctx, http.MethodPut, pathObjects, in, nil)
}

func (c *client) DeleteObject(ctx context.Context, in nodeapi.ObjectRef) error {
	q := url.Values{"kind": {string(in.Kind)}, "namespace": {string(in.Namespace)}, "name": {string(in.Name)}, "uid": {string(in.UID)}}
	return c.do(ctx, http.MethodDelete, pathObjects+"?"+q.Encode(), nil, nil)
}

func (c *client) Report(ctx context.Context, in nodeapi.NodeReport) error {
	return c.do(ctx, http.MethodPut, pathReport, in, nil)
}

func (c *client) Key(ctx context.Context, ns v1.NamespaceName) (nodeapi.NamespaceKey, error) {
	var out nodeapi.NamespaceKey
	return out, c.do(ctx, http.MethodGet, pathKeys+"?namespace="+url.QueryEscape(string(ns)), nil, &out)
}

func (c *client) Secret(ctx context.Context, in nodeapi.SecretWrite) (nodeapi.SecretAnswer, error) {
	var out nodeapi.SecretAnswer
	return out, c.do(ctx, http.MethodPost, pathSecrets, in, &out)
}

func (c *client) Renew(ctx context.Context, in nodeapi.RenewRequest) (nodeapi.JoinResponse, error) {
	var out nodeapi.JoinResponse
	return out, c.do(ctx, http.MethodPost, pathRenew, in, &out)
}

func (c *client) Renewed(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, pathRenew, nil, nil)
}

func (c *client) Calls(ctx context.Context, wait time.Duration) ([]nodeapi.Call, error) {
	var out []nodeapi.Call
	return out, c.do(ctx, http.MethodGet, pathCalls+"?wait="+wait.String(), nil, &out)
}

func (c *client) Answer(ctx context.Context, id string, a nodeapi.Answer) error {
	return c.do(ctx, http.MethodPost, pathCalls+"/"+url.PathEscape(id), a, nil)
}

func (c *client) Close() error {
	c.hc.CloseIdleConnections()
	return nil
}

// do sends one request of the node API and decodes its answer into out; a failed one is an error of the kind the control
// plane answered, and a revision it no longer serves (410) fault.PreconditionFailed.
//
//nolint:forbidigo // a JSON round trip of the node API: body and out are its typed requests and answers
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	const op = "longpoll.request"
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "marshal the %s %s", method, path)
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.id.Server+path, rd)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "build the %s %s", method, path)
	}
	req.Header.Set(headerAPIVersion, strconv.Itoa(nodeapi.APIVersion))
	req.Header.Set(headerVersion, c.id.Version)
	req.Header.Set(headerPlatform, c.id.Platform)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "%s %s", method, path)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "read the answer of %s %s", method, path)
	}
	switch {
	case resp.StatusCode == http.StatusGone:
		return fault.PreconditionFailedf(op, "%s %s: %d %s", method, path, resp.StatusCode, detail(data))
	case resp.StatusCode == http.StatusUpgradeRequired:
		return fault.Conflictf(op, "%s %s: %d %s", method, path, resp.StatusCode, detail(data))
	case resp.StatusCode >= http.StatusMultipleChoices:
		p := problem(data, resp.StatusCode)
		return fault.Wrapf(nil, fault.KindOfProblem(p), op, "%s %s: %d %s", method, path, resp.StatusCode, detail(data))
	case out != nil && len(data) > 0:
		if err := json.Unmarshal(data, out); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "decode the answer of %s %s", method, path)
		}
	}
	return nil
}

// problem is the answer's problem document, or one of its status alone.
func problem(answer []byte, status int) fault.Problem {
	var p fault.Problem
	if json.Unmarshal(answer, &p) != nil || p.Status == 0 {
		p.Status = status
	}
	return p
}

// detail is what an answer's problem says went wrong, or the whole answer of another kind.
func detail(answer []byte) string {
	var p fault.Problem
	if json.Unmarshal(answer, &p) == nil && p.Detail != "" {
		return p.Detail
	}
	return strings.TrimSpace(string(answer))
}
