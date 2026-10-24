package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
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

// EngineTarget is the single Lake this proxy endpoint fronts (ADR-0137): its Cedar resource
// ref (the lake::query resource, namespace and name both fixed by the endpoint), its netns engine URL,
// and the shared engine token the caller token is swapped for on allow. One EngineTarget per listener
// endpoint, so the target lake is fixed by the endpoint (never parsed from the opaque Quack body).
//
// The ADR Contracts sketch typed Lake as `v1.EntityRef`; there is no such type — the PDP resource is
// auth.EntityRef (a Lake ref), which is what this carries.
type EngineTarget struct {
	Lake        auth.EntityRef // Type=KindLake, Namespace/Name = the fronted Lake
	Upstream    string         // the netns engine endpoint (an http URL)
	EngineToken string         // the shared engine token, never exposed to callers
	SaveURL     string         // the engine's save endpoint; "" when lake.saveInterval is set: no barrier (ADR-0279)
}

// handshakeHeadMax is how much of a request body the proxy reads before it decides: the handshake
// preamble, the token field's header and length, and a token far longer than any funcd issues (a JWT
// over {ns, fn}, or an Identity token of at most 255 bytes). A longer token does not parse as a handshake and is
// forwarded un-swapped, which the engine rejects (fail-closed, as for any non-handshake body).
const handshakeHeadMax = preambleLen + tokenHdrLen + binary.MaxVarintLen64 + 4<<10

const proxyOp = "lake.gateway.lakeProxy"

// lakeProxy is the lake PEP proxy (ADR-0137): resolve principal → lake::query PEP → swap the
// handshake token → reverse-proxy to the engine. Deny/unresolved ⇒ 403, upstream never called.
type lakeProxy struct {
	keys   LakeKeys
	pdp    auth.Authorizer
	engine EngineTarget
	rp     *httputil.ReverseProxy // nil ⇒ a malformed Upstream (every request is an Internal problem)
	log    *slog.Logger
	saves  *saveBarrier // nil ⇒ no SaveURL: answers go out as the engine sends them
}

// NewLakeProxy builds the lake PEP proxy handler fronting one Lake engine (ADR-0137).
// It logs through log (nil means slog.Default()), as the Manager's proxies log through its logger (ADR-0002 §6).
func NewLakeProxy(keys LakeKeys, pdp auth.Authorizer, engine EngineTarget, log *slog.Logger) http.Handler {
	return newLakeProxy(keys, pdp, engine, httpx.NodeTransport(), cmp.Or(log, slog.Default()).With("component", "lake.gateway"))
}

// newLakeProxy builds the proxy over transport and log. A failed engine call is a 502 logged through log, not
// the ReverseProxy default (logged through the stdlib log package).
func newLakeProxy(keys LakeKeys, pdp auth.Authorizer, engine EngineTarget, transport http.RoundTripper, log *slog.Logger) *lakeProxy {
	p := &lakeProxy{keys: keys, pdp: pdp, engine: engine, log: log}
	if u, err := url.Parse(engine.Upstream); err == nil && u.Host != "" {
		p.rp = httputil.NewSingleHostReverseProxy(u)
		p.rp.Transport = transport
		p.rp.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelWarn)
		p.rp.ErrorHandler = p.engineFailed
		if engine.SaveURL != "" {
			p.saves = &saveBarrier{url: engine.SaveURL, client: &http.Client{Transport: transport}}
			p.rp.ModifyResponse = p.saves.hold
		}
	}
	return p
}

// engineFailed answers a failed engine call with an Unavailable problem+json logged through slog
// (ADR-0002), not the ReverseProxy default (a bare 502 logged through the stdlib log package). The
// cause stays in the log: it names the engine's netns endpoint.
func (p *lakeProxy) engineFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotSaved) {
		p.log.WarnContext(r.Context(), "lake catalog not saved", "lake", p.engine.Lake.Name, "err", err)
		fault.WriteProblem(w, fault.Unavailablef(proxyOp, "lake catalog not saved"))
		return
	}
	p.log.WarnContext(r.Context(), "lake engine call failed", "lake", p.engine.Lake.Name, "err", err)
	fault.WriteProblem(w, fault.Unavailablef(proxyOp, "lake engine unavailable"))
}

// ServeHTTP is the per-request PEP (ADR-0137). On the token-bearing handshake it resolves the caller
// token → principal, denies a principal outside this endpoint's namespace, runs lake::query on this
// endpoint's Lake, and on allow swaps the caller token → the shared engine token before
// forwarding. A non-handshake (session-id-keyed) request forwards opaquely (fail-closed: an un-swapped
// body carries the caller token, which the engine rejects). Deny/unresolved ⇒ 403 with the upstream never called.
func (p *lakeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.rp == nil {
		fault.WriteProblem(w, fault.Internalf(proxyOp, "lake engine upstream is not a valid URL"))
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
			p.log.Debug("lake PEP: unresolved credential", "lake", p.engine.Lake.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "lake query denied"))
			return
		}
		if principal.Namespace != p.engine.Lake.Namespace {
			p.log.Debug("lake PEP denied: principal outside the lake's namespace", "lake", p.engine.Lake.Name, "principal", principal.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "lake query denied"))
			return
		}
		resource := p.engine.Lake
		dec, aerr := p.pdp.Authorize(r.Context(), auth.Request{
			Action:   auth.ActionLakeQuery,
			Resource: &resource,
			Identity: auth.Identity{Principal: &principal},
		})
		if aerr != nil {
			p.log.Error("lake PEP error", "lake", p.engine.Lake.Name, "err", aerr)
			fault.WriteProblem(w, fault.Internalf(proxyOp, "lake query authorization failed"))
			return
		}
		if !dec.Allowed {
			p.log.Debug("lake PEP denied", "lake", p.engine.Lake.Name, "principal", principal.Name)
			fault.WriteProblem(w, fault.Forbiddenf(proxyOp, "lake query denied"))
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
	if p.saves != nil {
		engine, err := p.saves.instance(r.Context())
		if err != nil {
			p.engineFailed(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), instanceKey{}, engine))
	}
	p.rp.ServeHTTP(w, r)
}
