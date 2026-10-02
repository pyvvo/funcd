package local

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/dataplane"
)

// namespaceHeader matches the data-plane's target-namespace header (internal/dataplane).
const namespaceHeader = "X-Funcd-Namespace"

// proxyInvoker forwards the call through the IN-PROCESS data-plane handler (ADR-0033/0016): it
// synthesizes a POST /function/<target> request carrying the input as a CloudEvents v1.0 envelope (the
// edge never normalizes internal traffic, ADR-0134), captures the response, and propagates the target
// shim's status verbatim — it does NOT re-validate the contract (the daemon holds no per-function
// validator; the target's shim returns 422 input / 500 output per ADR-0058). A cold target is woken
// by the activator inside the data-plane handler; the timeout bounds the wait.
type proxyInvoker struct{ dataPlane http.Handler }

// NewInvoker returns an Invoker that forwards through the given in-process data-plane handler.
func NewInvoker(dataPlane http.Handler) Invoker { return proxyInvoker{dataPlane: dataPlane} }

func (p proxyInvoker) Invoke(ctx context.Context, target Ref, input []byte, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Mark the request internal so the data-plane exposure gate (ADR-0110) never blocks fn-to-fn:
	// internal invoke is by name and is not subject to a namespace's exposure mode. Spoof-proof — the
	// value is set here in-process; an external request on the public listener can't carry it.
	cctx = dataplane.WithInternal(cctx)
	req := httptest.NewRequest(http.MethodPost, "/function/"+string(target.Function), bytes.NewReader(dataplane.InvokeEnvelope(target.Namespace, target.Function, input))).WithContext(cctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(namespaceHeader, string(target.Namespace))

	rec := &cappedRecorder{ResponseRecorder: httptest.NewRecorder()}
	p.dataPlane.ServeHTTP(rec, req)
	if rec.over {
		return nil, fault.PayloadTooLargef("workernode.local.invoke", "the response of %s exceeds the %d-byte invoke limit", target, maxInvokeBytes)
	}
	res := rec.Result()
	body := rec.Body.Bytes()

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return body, nil
	}
	// Propagate the target's failure (e.g. the shim's 422/500, or the activator's 503) verbatim.
	return nil, &UpstreamError{Status: res.StatusCode, Body: body}
}

// cappedRecorder captures the target's response holding at most maxInvokeBytes of its body, the
// local API's DoS guard applied to the output as to the input. Past the cap it drops the bytes
// instead of failing the write: a failed write makes httputil.ReverseProxy abort the whole handler.
type cappedRecorder struct {
	*httptest.ResponseRecorder
	over bool
}

func (c *cappedRecorder) Write(p []byte) (int, error) {
	if c.over || c.Body.Len()+len(p) > maxInvokeBytes {
		c.over = true
		return len(p), nil
	}
	return c.ResponseRecorder.Write(p)
}

func (c *cappedRecorder) WriteString(s string) (int, error) { return c.Write([]byte(s)) }
