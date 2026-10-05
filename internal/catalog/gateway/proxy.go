package gateway

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// EngineTarget is the single CatalogService this proxy endpoint fronts (ADR-0137): its Cedar resource
// ref (the catalog::query resource, namespace and name both fixed by the endpoint), its netns engine URL,
// and the shared engine token the caller token is swapped for on allow. One EngineTarget per listener
// endpoint, so the target catalog is fixed by the endpoint (never parsed from the opaque Quack body).
//
// The ADR Contracts sketch typed Catalog as `v1.EntityRef`; there is no such type — the PDP resource is
// auth.EntityRef (a CatalogService ref), which is what this carries.
type EngineTarget struct {
	Catalog     auth.EntityRef // Type=KindCatalogService, Namespace/Name = the fronted CatalogService
	Upstream    string         // the netns engine endpoint (an http URL)
	EngineToken string         // the shared engine token, never exposed to callers
}

// handshakeHeadMax is how much of a request body the proxy reads before it decides: the handshake
// preamble, the token field's header and length, and a token far longer than any funcd issues (a JWT
// over {ns, fn}, or a 44-byte minted Identity token). A longer token does not parse as a handshake and is
// forwarded un-swapped, which the engine rejects (fail-closed, as for any non-handshake body).
const handshakeHeadMax = preambleLen + tokenHdrLen + binary.MaxVarintLen64 + 4<<10

const proxyOp = "catalog.gateway.catalogProxy"

// catalogProxy is the catalog PEP proxy (ADR-0137): resolve principal → catalog::query PEP → swap the
// handshake token → reverse-proxy to the engine. Deny/unresolved ⇒ 403, upstream never called.
type catalogProxy struct {
	keys   CatalogKeys
	pdp    auth.Authorizer
	engine EngineTarget
	rp     *httputil.ReverseProxy // nil ⇒ a malformed Upstream (every request is an Internal problem)
	log    *slog.Logger
}

// NewCatalogProxy builds the catalog PEP proxy handler fronting one CatalogService engine (ADR-0137).
// It logs through slog.Default; the Manager builds its proxies over its own logger (ADR-0002 §6).
func NewCatalogProxy(keys CatalogKeys, pdp auth.Authorizer, engine EngineTarget) http.Handler {
	return newCatalogProxy(keys, pdp, engine, httpx.Transport(), slog.Default().With("component", "catalog.gateway"))
}

// newCatalogProxy builds the proxy over transport and log. A failed engine call is a 502 logged through log, not
// the ReverseProxy default (logged through the stdlib log package).
func newCatalogProxy(keys CatalogKeys, pdp auth.Authorizer, engine EngineTarget, transport http.RoundTripper, log *slog.Logger) *catalogProxy {
	p := &catalogProxy{keys: keys, pdp: pdp, engine: engine, log: log}
	if u, err := url.Parse(engine.Upstream); err == nil && u.Host != "" {
		p.rp = httputil.NewSingleHostReverseProxy(u)
		p.rp.Transport = transport
		p.rp.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
		p.rp.ErrorHandler = p.engineFailed
	}
	return p
}

// engineFailed answers a failed engine call with an Unavailable problem+json logged through slog
// (ADR-0002), not the ReverseProxy default (a bare 502 logged through the stdlib log package). The
// cause stays in the log: it names the engine's netns endpoint.
func (p *catalogProxy) engineFailed(w http.ResponseWriter, r *http.Request, err error) {
	p.log.WarnContext(r.Context(), "catalog engine call failed", "catalog", p.engine.Catalog.Name, "err", err)
	fault.WriteProblem(w, fault.Unavailablef(proxyOp, "catalog engine unavailable"))
}

// ServeHTTP is the per-request PEP (ADR-0137). On the token-bearing handshake it resolves the caller
// token → principal, denies a principal outside this endpoint's namespace, runs catalog::query on this
// endpoint's CatalogService, and on allow swaps the caller token → the shared engine token before
// forwarding. A non-handshake (session-id-keyed) request forwards opaquely (fail-closed: an un-swapped
// body carries the caller token, which the engine rejects). Deny/unresolved ⇒ 403 with the upstream never called.
func (p *catalogProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.rp == nil {
		fault.WriteProblem(w, fault.Internalf(proxyOp, "catalog engine upstream is not a valid URL"))
		return
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, handshakeHeadMax))
	if err != nil {
		fault.WriteProblem(w, fault.Wrapf(err, fault.Invalid, proxyOp, "read request body"))
		return
	}
	whole := len(head) < handshakeHeadMax
	length := r.ContentLength
	if whole {
		length = int64(len(head))
	}

	rewritten, callerToken, isHandshake := swapHandshakeToken(head, p.engine.EngineToken)
	if isHandshake {
		principal, ok := p.keys.PrincipalFor(callerToken)
		if !ok {
			p.log.Debug("catalog PEP: unresolved credential", "catalog", p.engine.Catalog.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "catalog query denied"))
			return
		}
		if principal.Namespace != p.engine.Catalog.Namespace {
			p.log.Debug("catalog PEP denied: principal outside the catalog's namespace", "catalog", p.engine.Catalog.Name, "principal", principal.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "catalog query denied"))
			return
		}
		resource := p.engine.Catalog
		dec, aerr := p.pdp.Authorize(r.Context(), auth.Request{
			Action:   auth.ActionCatalogQuery,
			Resource: &resource,
			Identity: auth.Identity{Principal: &principal},
		})
		if aerr != nil {
			p.log.Error("catalog PEP error", "catalog", p.engine.Catalog.Name, "err", aerr)
			fault.WriteProblem(w, fault.Internalf(proxyOp, "catalog query authorization failed"))
			return
		}
		if !dec.Allowed {
			p.log.Debug("catalog PEP denied", "catalog", p.engine.Catalog.Name, "principal", principal.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "catalog query denied"))
			return
		}
		if length >= 0 {
			length += int64(len(rewritten) - len(head))
		}
		head = rewritten // authorized: forward with the caller token swapped for the engine token
	}

	// The rest of the body streams to the engine unread: only the head is held in memory.
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if whole {
		// A parked engine connection can be dead before the request is written to it, when the engine restarted
		// on the same address (#634); a rewindable body lets net/http send the request again on a fresh one.
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(head)), nil }
	}
	r.ContentLength = length
	if length >= 0 {
		r.Header.Set("Content-Length", strconv.FormatInt(length, 10))
	} else {
		r.Header.Del("Content-Length")
	}
	p.rp.ServeHTTP(w, r)
}
