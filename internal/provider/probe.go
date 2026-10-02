package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// probeBodyMax bounds the body drained before close: a body read to EOF returns the connection to
// the keep-alive pool, so repeated probes do not churn ephemeral ports (ADR-0041).
const probeBodyMax = 4 << 10

// probeReady issues GET http://<ip>:<port><path> and reports whether the engine answered the
// expected status (ADR-0087). It is the engine's OWN HTTP readiness probe — a provider serves its
// protocol and defines its own health endpoint (Quack: GET / → 200), NOT the funcd shim's
// /health/readiness. Any transport error, an unresolved address, or a non-matching status ⇒ not
// ready (a pinned engine that never answers stays NotReady, so no route is programmed).
func (r *engineRuntime) probeReady(ctx context.Context, ip string, port int, probe ReadinessProbe) bool {
	host := ip
	if host == "" {
		return false // no resolved netns address yet — not reachable
	}
	path := probe.Path
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("http://%s:%d%s", host, port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBodyMax))
		_ = resp.Body.Close()
	}()
	return resp.StatusCode == probe.ExpectStatus
}
