package oidc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// Each call to a provider stops at backChannelTimeout and reads at most maxBackChannelBytes (ADR-0335 Decision 7,
// ADR-0336 Decision 6).
const (
	backChannelTimeout  = 10 * time.Second
	maxBackChannelBytes = 1 << 20
	dialTimeout         = 5 * time.Second
)

// errTooLarge is an answer past maxBackChannelBytes.
var errTooLarge = errors.New("the provider's answer exceeds 1 MiB")

// globalTransport reaches a provider over https only, at globally reachable addresses checked at each connection,
// redirects included (ADR-0335 Decision 7). It uses no proxy, so the address checked is the provider's.
func globalTransport() http.RoundTripper {
	tr := httpx.Transport()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: dialTimeout, Control: dialGlobal}).DialContext
	return httpsOnly{next: tr}
}

// dialGlobal refuses a connection to an address that is not globally reachable.
func dialGlobal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ok, err := egress.GloballyReachable(ip)
	if err != nil {
		return err
	}
	if !ok {
		return fault.Forbiddenf("oidc.backChannel", "%s is not a globally reachable address", ip)
	}
	return nil
}

// httpsOnly refuses every request that is not https.
type httpsOnly struct{ next http.RoundTripper }

func (h httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, fault.Forbiddenf("oidc.backChannel", "%s is not https", r.URL.Redacted())
	}
	return h.next.RoundTrip(r)
}

// capped stops reading an answer past maxBackChannelBytes.
type capped struct{ next http.RoundTripper }

func (c capped) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{body: resp.Body, left: maxBackChannelBytes}
	return resp, nil
}

type cappedBody struct {
	body io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		if n, _ := b.body.Read(one[:]); n > 0 {
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *cappedBody) Close() error { return b.body.Close() }

// backChannel is how a provider is reached: at the engine's own address with spec.addon, else through Decision 7's
// transport.
type backChannel struct {
	global *http.Client
	engine *http.Client
}

func newBackChannel(global http.RoundTripper) backChannel {
	if global == nil {
		global = globalTransport()
	}
	return backChannel{
		global: &http.Client{Timeout: backChannelTimeout, Transport: capped{next: global}},
		engine: &http.Client{Timeout: backChannelTimeout, Transport: capped{next: httpx.NodeTransport()},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// client is the client of a provider served by the engine at address, or of one outside funcd when address is "".
func (b backChannel) client(address string) *http.Client {
	if address != "" {
		return b.engine
	}
	return b.global
}

// rebase sends u to the engine's own address, keeping its path and query (ADR-0336 Decision 3): the provider names
// its endpoints after its public host. An empty address keeps u.
func rebase(u, address string) (string, error) {
	if address == "" {
		return u, nil
	}
	pu, err := url.Parse(u)
	if err != nil {
		return "", fault.Invalidf("oidc.rebase", "%q is not a URL", u)
	}
	pu.Scheme, pu.Host = "http", address
	return pu.String(), nil
}

// get reads the answer to GET u with c; an answer other than 200 is an error naming its status only.
func get(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	const op = "oidc.get"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fault.Invalidf(op, "GET %s: %v", u, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "GET %s", u)
	}
	defer httpx.CloseBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fault.Unavailablef(op, "GET %s answered %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "read GET %s", u)
	}
	return body, nil
}
