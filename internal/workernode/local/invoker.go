package local

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/green-0-rabbit/funcd/internal/dataplane"
)

// namespaceHeader matches the data-plane's target-namespace header (internal/dataplane).
const namespaceHeader = "X-Funcd-Namespace"

// proxyInvoker forwards the call through the IN-PROCESS data-plane handler (ADR-0033/0016): it
// synthesizes a POST /function/<target> request, captures the response, and propagates the target
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
	req := httptest.NewRequest(http.MethodPost, "/function/"+string(target.Function), bytes.NewReader(input)).WithContext(cctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(namespaceHeader, string(target.Namespace))

	rec := httptest.NewRecorder()
	p.dataPlane.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return body, nil
	}
	// Propagate the target's failure (e.g. the shim's 422/500, or the activator's 503) verbatim.
	return nil, &UpstreamError{Status: res.StatusCode, Body: body}
}
