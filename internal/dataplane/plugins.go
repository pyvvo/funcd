package dataplane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/edge/authn"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// callOp names the data plane in a refusal's problem detail, as it did before the steps became plugins.
const callOp = "dataplane.ServeHTTP"

// Plugins returns the data plane's built-in invokePlugins in ADR-0302 Decision 2's order: authn, hold, rate,
// credentials, prefix, envelope, deadline. enf may be nil (an authenticated stance then fails closed), lim nil (no
// per-target rate), held nil (no hold); defaultTimeout ≤ 0 is v1.DefaultInvokeTimeout.
func Plugins(enf *authn.Enforcer, lim *limit.TargetLimiter, held hold.CallGate, defaultTimeout time.Duration) []invoke.Plugin {
	if held == nil {
		held = hold.NeverCall
	}
	if defaultTimeout <= 0 {
		defaultTimeout = v1.DefaultInvokeTimeout
	}
	return []invoke.Plugin{
		authnPlugin{enf: enf},
		holdPlugin{held: held},
		ratePlugin{lim: lim},
		credentialsPlugin{},
		prefixPlugin{},
		envelopePlugin{},
		deadlinePlugin{defaultTimeout: defaultTimeout},
	}
}

// authnPlugin is the F77 edge PEP (ADR-0113): it runs before any answer that depends on the Function, so an
// anonymous caller cannot tell names apart and nothing wakes.
type authnPlugin struct{ enf *authn.Enforcer }

func (authnPlugin) Name() string                { return "authn" }
func (authnPlugin) Applies(c *invoke.Call) bool { return c.Caller == invoke.External }

func (p authnPlugin) Admit(ctx context.Context, c *invoke.Call) *invoke.Refusal {
	if p.enf != nil {
		ref := activator.FunctionRef{Namespace: c.Target.Namespace, Name: c.Target.Name}
		if err := p.enf.Enforce(ctx, c.Request, ref, c.Stance); err != nil {
			return &invoke.Refusal{Err: err}
		}
	} else if c.Stance == v1.AuthAuthenticated {
		// Fail-closed: an authenticated stance with no PEP wired cannot authenticate ⇒ 401.
		return &invoke.Refusal{Err: fault.Unauthorizedf(callOp, "authentication required but no authenticator is configured")}
	}
	return nil
}

// holdPlugin refuses a call into a namespace held for its data (ADR-0290), after authentication.
type holdPlugin struct{ held hold.CallGate }

func (holdPlugin) Name() string { return "hold" }
func (holdPlugin) Applies(c *invoke.Call) bool {
	return c.Caller == invoke.External || c.Caller == invoke.FnToFn
}

func (p holdPlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	if m, held := p.held.NamespaceMarker(c.Target.Namespace); held {
		return &invoke.Refusal{Err: heldError(callOp, c.Target.Namespace, m),
			Header: http.Header{"Retry-After": {heldRetryAfter}}}
	}
	return nil
}

// ratePlugin takes a token from the Function's bucket once the name resolved (ADR-0164), so a name that does not
// resolve creates no bucket and a 429 never wakes a sandbox.
type ratePlugin struct{ lim *limit.TargetLimiter }

func (ratePlugin) Name() string                { return "rate" }
func (ratePlugin) Applies(c *invoke.Call) bool { return c.Caller == invoke.External }

func (p ratePlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	if retryAfter, err := p.lim.Take(c.Target.Namespace, c.Target.Name); err != nil {
		return &invoke.Refusal{Err: err, Header: http.Header{"Retry-After": {retryAfter}}}
	}
	return nil
}

// credentialsPlugin keeps the platform credential the PEP consumed from the Function (ADR-0171 Decision 5).
type credentialsPlugin struct{}

func (credentialsPlugin) Name() string { return "credentials" }
func (credentialsPlugin) Applies(c *invoke.Call) bool {
	return c.Caller == invoke.External && c.Stance == v1.AuthAuthenticated
}

func (credentialsPlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	c.Request.Header.Del("Authorization")
	c.Request.Header.Del("X-Api-Key")
	return nil
}

// prefixPlugin tells an http Function the prefix the edge stripped, a Route's or /function/<name>, so its redirects
// and links point back through the edge (ADR-0243 Decision 3).
type prefixPlugin struct{}

func (prefixPlugin) Name() string { return "prefix" }
func (prefixPlugin) Applies(c *invoke.Call) bool {
	return c.Caller == invoke.External && serveHTTP(c.Function)
}

func (prefixPlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	c.Request.Header.Del("X-Forwarded-Prefix")
	if prefix := strings.TrimSuffix(strings.TrimSuffix(c.Request.URL.Path, c.Remainder), "/"); prefix != "" {
		c.Request.Header.Set("X-Forwarded-Prefix", prefix)
	}
	return nil
}

// envelopePlugin builds the CloudEvent envelope from an external body, so a caller sends plain data and never
// hand-writes {"data":…} (ADR-0134). The read is bounded because it buffers. A bodiless upgrade is streamed untouched,
// so the tunnel after the 101 carries no envelope bytes (ADR-0181). Internal producers already emit an envelope.
type envelopePlugin struct{}

func (envelopePlugin) Name() string { return "envelope" }
func (envelopePlugin) Applies(c *invoke.Call) bool {
	return c.Caller == invoke.External && !bodilessUpgrade(c.Request)
}

func (envelopePlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	// No ResponseWriter: the edge chain's writers hide the one net/http closes the connection through, so a nil
	// writer behaves as the wrapped one did.
	body, err := io.ReadAll(http.MaxBytesReader(nil, c.Request.Body, maxNormalizeBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &invoke.Refusal{Err: fault.PayloadTooLargef(callOp, "request body exceeds %d bytes", tooLarge.Limit)}
		}
		return &invoke.Refusal{Err: fault.Invalidf(callOp, "reading request body: %v", err)}
	}
	ns, name := c.Target.Namespace, c.Target.Name
	if serveHTTP(c.Function) {
		body = HTTPEnvelope(ns, name, body)
	} else {
		body = InvokeEnvelope(ns, name, body)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

// deadlinePlugin gives an external invoke its response deadline, counted from receipt: the Function's
// spec.timeout, else invoke.defaultTimeout (ADR-0151).
type deadlinePlugin struct{ defaultTimeout time.Duration }

func (deadlinePlugin) Name() string                { return "deadline" }
func (deadlinePlugin) Applies(c *invoke.Call) bool { return c.Caller == invoke.External }

func (p deadlinePlugin) Before(_ context.Context, c *invoke.Call) *invoke.Refusal {
	limit, source := p.defaultTimeout, "invoke.defaultTimeout"
	if c.Function != nil && c.Function.Spec.Timeout > 0 {
		limit, source = time.Duration(c.Function.Spec.Timeout), "spec.timeout"
	}
	c.Request = activator.WithResponseDeadline(c.Request,
		activator.ResponseDeadline{At: c.Received.Add(limit), Limit: limit, Source: source})
	return nil
}

func serveHTTP(fn *v1.Function) bool {
	return fn != nil && fn.Spec.Protocol.Normalized() == v1.ProtocolHTTP
}
