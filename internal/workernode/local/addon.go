package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// A Function calls an add-on object's engine through funcd (ADR-0338): on its local API's /addon facet, and through
// the node-private add-on proxy that any HTTP SDK reaches. Both make the same checks and the same call.

// DependencyAddon is the dependency-report kind of a spec.bindings.addon binding (ADR-0338 Decision 1).
const DependencyAddon = "addon"

const (
	// AddonReplyHeader marks an answer of the engine, which passes through, apart from funcd's own refusals.
	AddonReplyHeader = "X-Funcd-Addon-Reply"
	// AddonTokenHeader carries the proxy's token. It is an X-Funcd-* header, so it never reaches the engine.
	AddonTokenHeader = "X-Funcd-Addon-Token"
	// addonHeldRetryAfter is the Retry-After, in seconds, of a call refused by a namespace hold (ADR-0290 Decision 2).
	addonHeldRetryAfter = "30"
)

// The defaults of addons.callTimeout and addons.callPayloadLimit (ADR-0338 Decision 4).
const (
	DefaultAddonCallTimeout  = 10 * time.Second
	DefaultAddonPayloadLimit = 1 << 20
)

// The causes of an add-on call (ADR-0338 Decision 6), each a refusal of funcd's, which carries no reply header.
var (
	ErrAddonNoBinding = fault.NewKnownCause(fault.Forbidden, "local.addon-no-binding",
		"The Function binds no add-on object under the alias.",
		"Call an alias of spec.bindings.addon, or add it there.")
	ErrAddonForbidden = fault.NewKnownCause(fault.Forbidden, "local.addon-forbidden",
		"Cedar denies the Function addon::call on the object, as when a Policy forbids it.",
		"Ask the operator to lift the forbid.")
	ErrAddonTokenRefused = fault.NewKnownCause(fault.Forbidden, "local.addon-token-refused",
		"The request to the add-on proxy carries no X-Funcd-Addon-Token, or one that is no Function's.",
		"Send FUNCD_ADDON_<ALIAS>_TOKEN in X-Funcd-Addon-Token.")
	ErrAddonPathRefused = fault.NewKnownCause(fault.NotFound, "local.addon-path-refused",
		"The path names no alias, or the object's edge entry would refuse it: outside its Addon's basePath, under one of "+
			"its privatePaths, or with a .. segment.",
		"Call a path under the Addon's basePath that it does not keep private.")
	ErrAddonHeld = fault.NewKnownCause(fault.Unavailable, "local.addon-held",
		"The caller's namespace is held while a Restore loads its data.",
		"Retry after the Retry-After seconds.")
	ErrAddonEngineUnavailable = fault.NewKnownCause(fault.Unavailable, "local.addon-engine-unavailable",
		"The add-on object is not Ready, has no engine address yet, or its engine refused the connection.",
		"Wait until the object is Ready, then retry.")
	ErrAddonTimeout = fault.NewKnownCause(fault.Unavailable, "local.addon-timeout",
		"The engine did not answer within addons.callTimeout.",
		"Retry, or ask the operator to raise addons.callTimeout.")
	ErrAddonReplyTooLarge = fault.NewKnownCause(fault.PayloadTooLarge, "local.addon-reply-too-large",
		"The engine's answer body is larger than addons.callPayloadLimit.",
		"Ask for less, or ask the operator to raise addons.callPayloadLimit.")
	ErrAddonReplyCutOff = fault.NewKnownCause(fault.Unavailable, "local.addon-reply-cut-off",
		"The engine's answer ended before its body did.",
		"Retry.")
)

// Addon is the port both paths route a call through; the add-on provider's Calls satisfy it. As for SQL, the caller
// is the sandbox's fixed Ref on the facet, and the token's Function on the proxy, never the request's word.
type Addon interface {
	// Target makes every check before the dial, the binding, Cedar, the hold, the object's readiness and the path,
	// and returns where the call goes (ADR-0338 Decision 2).
	Target(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, alias, path string) (AddonTarget, error)
}

// AddonTarget is where a call goes: the engine's own address and the cleaned path to send.
type AddonTarget struct {
	Address string
	Path    string
}

// AddonLimits are addons.callTimeout and addons.callPayloadLimit; zero is the default.
type AddonLimits struct {
	Timeout time.Duration
	Payload int64
}

func (l AddonLimits) orDefaults() AddonLimits {
	if l.Timeout <= 0 {
		l.Timeout = DefaultAddonCallTimeout
	}
	if l.Payload <= 0 {
		l.Payload = DefaultAddonPayloadLimit
	}
	return l
}

// addonPatterns are the facet's patterns, any method: an alias with a path, and an alias alone, which calls "/".
const (
	addonPattern     = "/addon/{alias}/{path...}"
	addonRootPattern = "/addon/{alias}"
)

// AddonFacet serves the caller's calls of its bound add-on objects' engines at /addon/{alias}/{path...} (ADR-0338
// Decision 2).
func AddonFacet(a Addon, limits AddonLimits) Facet {
	return Facet{segment: "addon", register: func(mux facetMux, caller Ref, logger *slog.Logger) {
		c := newAddonCaller(a, limits, logger)
		serve := func(w http.ResponseWriter, r *http.Request) error {
			return c.serve(w, r, caller, r.PathValue("alias"), "/"+r.PathValue("path"))
		}
		handle(mux, logger, addonPattern, addonCauses(false), serve)
		handle(mux, logger, addonRootPattern, addonCauses(false), serve)
	}}
}

// NewAddonProxy is the node-private add-on proxy (ADR-0338 Decision 3), served at FUNCD_ADDON_<ALIAS>_URL: callerOf
// resolves the X-Funcd-Addon-Token to its Function or its Job (ADR-0351 Decision 4), then the call is the facet's.
func NewAddonProxy(a Addon, callerOf func(token string) (Ref, bool), limits AddonLimits, logger *slog.Logger) http.Handler {
	c := newAddonCaller(a, limits, logger)
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, r *http.Request) error {
		caller, ok := callerOf(r.Header.Get(AddonTokenHeader))
		if !ok {
			return ErrAddonTokenRefused.Errorf("workernode.local.addonProxy", "the request carries no token of a Function or a Job")
		}
		return c.serve(w, r, caller, r.PathValue("alias"), "/"+r.PathValue("path"))
	}
	causes := addonCauses(true)
	mux.HandleFunc(addonPattern, wrap(logger, addonPattern, causes, serve))
	mux.HandleFunc(addonRootPattern, wrap(logger, addonRootPattern, causes, serve))
	mux.HandleFunc("/", wrap(logger, "/", causes, func(http.ResponseWriter, *http.Request) error {
		return ErrAddonPathRefused.Errorf("workernode.local.addonProxy", "the path names no alias: call /addon/<alias>/<path>")
	}))
	return mux
}

// addonCauses are the causes a call answers; the proxy's adds its token's.
func addonCauses(proxy bool) []*fault.KnownCause {
	out := []*fault.KnownCause{ErrAddonNoBinding, ErrAddonForbidden, ErrAddonPathRefused, ErrAddonHeld,
		ErrAddonEngineUnavailable, ErrAddonTimeout, ErrBodyTooLarge, ErrBodyUnreadable, ErrAddonReplyTooLarge,
		ErrAddonReplyCutOff}
	if proxy {
		out = append(out, ErrAddonTokenRefused)
	}
	return out
}

// addonCaller makes the call of one path: its client follows no redirect, so a 3xx reaches the caller as the engine
// sent it.
type addonCaller struct {
	addon  Addon
	limits AddonLimits
	client *http.Client
	logger *slog.Logger
}

func newAddonCaller(a Addon, limits AddonLimits, logger *slog.Logger) addonCaller {
	return addonCaller{addon: a, limits: limits.orDefaults(), logger: logger, client: &http.Client{
		Transport:     httpx.NodeTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// serve checks the call through the port, then sends the request's method, query, filtered headers and bounded body
// to the engine, within addons.callTimeout, and writes the engine's status, filtered headers and bounded body with the
// reply header. No error of it names the engine's address.
func (c addonCaller) serve(w http.ResponseWriter, r *http.Request, caller Ref, alias, path string) error {
	const op = "workernode.local.addon"
	if c.addon == nil {
		return fault.Unavailablef(op, "add-on calls are not served here")
	}
	t, err := c.addon.Target(r.Context(), caller.Namespace, caller.Workload(), alias, path)
	if err != nil {
		if errors.Is(err, ErrAddonHeld) {
			w.Header().Set("Retry-After", addonHeldRetryAfter)
		}
		return err
	}
	body, err := readBody(w, r, op, c.limits.Payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), c.limits.Timeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, r.Method, "http://"+t.Address, bytes.NewReader(body))
	if err != nil {
		return ErrAddonEngineUnavailable.Errorf(op, "the call of %q could not be built", alias)
	}
	out.URL.Path, out.URL.RawQuery, out.Host = t.Path, r.URL.RawQuery, t.Address
	copyAddonHeaders(out.Header, r.Header)
	res, err := c.client.Do(out)
	if err != nil {
		if ctx.Err() != nil && r.Context().Err() == nil {
			return ErrAddonTimeout.Errorf(op, "the engine of %q did not answer within %s", alias, c.limits.Timeout)
		}
		c.logger.Warn("add-on call failed", "caller", caller.String(), "alias", alias)
		return ErrAddonEngineUnavailable.Errorf(op, "the engine of %q refused the connection", alias)
	}
	defer func() { _ = res.Body.Close() }()
	reply, err := io.ReadAll(io.LimitReader(res.Body, c.limits.Payload+1))
	switch {
	case err != nil && ctx.Err() != nil && r.Context().Err() == nil:
		return ErrAddonTimeout.Errorf(op, "the engine of %q did not answer within %s", alias, c.limits.Timeout)
	case err != nil:
		return ErrAddonReplyCutOff.Errorf(op, "the answer of the engine of %q was cut off", alias)
	case int64(len(reply)) > c.limits.Payload:
		return ErrAddonReplyTooLarge.Errorf(op, "the answer of the engine of %q is larger than %d bytes", alias, c.limits.Payload)
	}
	copyAddonHeaders(w.Header(), res.Header)
	w.Header().Del("Content-Length")
	w.Header().Set(AddonReplyHeader, "1")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(reply)
	return nil
}

// addonDropped are the headers neither way carries, besides the hop-by-hop ones and every X-Funcd-* header: the
// caller's address and the forwarding ones, which an engine could read as the caller's (ADR-0338 Decision 2).
var addonDropped = map[string]bool{ //nolint:gochecknoglobals // a fixed lookup table
	"X-Forwarded-For": true, "Forwarded": true, "X-Real-Ip": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true,
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// copyAddonHeaders copies from's headers to to, but the dropped ones, the X-Funcd-* ones and those its Connection
// header names.
func copyAddonHeaders(to, from http.Header) {
	named := map[string]bool{}
	for _, v := range from.Values("Connection") {
		for _, n := range strings.Split(v, ",") {
			named[textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(n))] = true
		}
	}
	for k, vs := range from {
		key := textproto.CanonicalMIMEHeaderKey(k)
		if addonDropped[key] || named[key] || strings.HasPrefix(key, "X-Funcd-") {
			continue
		}
		for _, v := range vs {
			to.Add(key, v)
		}
	}
}
