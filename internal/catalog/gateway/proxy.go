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

	"github.com/pyvvo/funcd/internal/auth"
)

// EngineTarget is the single CatalogService this proxy endpoint fronts (ADR-0137): its Cedar resource
// ref (the catalog::query resource — its NAME fixes the target catalog; the namespace is taken from the
// resolved principal), its netns engine URL, and the shared engine token the caller token is swapped for
// on allow. One EngineTarget per listener endpoint, so the target catalog is fixed by the endpoint (never
// parsed from the opaque Quack body).
//
// The ADR Contracts sketch typed Catalog as `v1.EntityRef`; there is no such type — the PDP resource is
// auth.EntityRef (a CatalogService ref), which is what this carries.
type EngineTarget struct {
	Catalog     auth.EntityRef // Type=KindCatalogService, Name=<catalog>; namespace overridden per-principal
	Upstream    string         // the netns engine endpoint (an http URL)
	EngineToken string         // the shared engine token, never exposed to callers
}

// handshakeHeadMax is how much of a request body the proxy reads before it decides: the handshake
// preamble, the token field's header and length, and a token far longer than any funcd issues (a JWT
// over {ns, fn}, or a 44-byte minted Identity token). A longer token does not parse as a handshake and is
// forwarded un-swapped, which the engine rejects (fail-closed, as for any non-handshake body).
const handshakeHeadMax = preambleLen + tokenHdrLen + binary.MaxVarintLen64 + 4<<10

// catalogProxy is the catalog PEP proxy (ADR-0137): resolve principal → catalog::query PEP → swap the
// handshake token → reverse-proxy to the engine. Deny/unresolved ⇒ 403, upstream never called.
type catalogProxy struct {
	keys   CatalogKeys
	pdp    auth.Authorizer
	engine EngineTarget
	rp     *httputil.ReverseProxy // nil ⇒ a malformed Upstream (every request 502s)
	log    *slog.Logger
}

// NewCatalogProxy builds the catalog PEP proxy handler fronting one CatalogService engine (ADR-0137).
func NewCatalogProxy(keys CatalogKeys, pdp auth.Authorizer, engine EngineTarget) http.Handler {
	p := &catalogProxy{
		keys:   keys,
		pdp:    pdp,
		engine: engine,
		log:    slog.Default().With("component", "catalog.gateway"),
	}
	if u, err := url.Parse(engine.Upstream); err == nil && u.Host != "" {
		p.rp = httputil.NewSingleHostReverseProxy(u)
	}
	return p
}

// ServeHTTP is the per-request PEP (ADR-0137). On the token-bearing handshake it resolves the caller
// token → principal, runs catalog::query on this endpoint's CatalogService (namespace from the
// principal), and on allow swaps the caller token → the shared engine token before forwarding. A
// non-handshake (session-id-keyed) request forwards opaquely (fail-closed: an un-swapped body carries the
// caller token, which the engine rejects). Deny/unresolved ⇒ 403 with the upstream never called.
func (p *catalogProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.rp == nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	head, err := io.ReadAll(io.LimitReader(r.Body, handshakeHeadMax))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	length := r.ContentLength
	if len(head) < handshakeHeadMax {
		length = int64(len(head)) // the whole body
	}

	rewritten, callerToken, isHandshake := swapHandshakeToken(head, p.engine.EngineToken)
	if isHandshake {
		principal, ok := p.keys.PrincipalFor(callerToken)
		if !ok {
			p.log.Debug("catalog PEP: unresolved credential", "catalog", p.engine.Catalog.Name)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		resource := p.engine.Catalog
		resource.Namespace = principal.Namespace // ns fixed by the principal, catalog by the endpoint
		dec, aerr := p.pdp.Authorize(r.Context(), auth.Request{
			Action:   auth.ActionCatalogQuery,
			Resource: &resource,
			Identity: auth.Identity{Principal: &principal},
		})
		if aerr != nil {
			p.log.Error("catalog PEP error", "catalog", p.engine.Catalog.Name, "err", aerr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !dec.Allowed {
			p.log.Debug("catalog PEP denied", "catalog", p.engine.Catalog.Name, "principal", principal.Name)
			http.Error(w, "forbidden", http.StatusForbidden)
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
	r.ContentLength = length
	if length >= 0 {
		r.Header.Set("Content-Length", strconv.FormatInt(length, 10))
	} else {
		r.Header.Del("Content-Length")
	}
	p.rp.ServeHTTP(w, r)
}
