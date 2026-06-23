package bench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestRSSMBSelf checks the gopsutil-backed RSS sampler (ADR-0051) returns a positive size for this
// very process — no external `ps`/`pgrep`, no other process needed.
func TestRSSMBSelf(t *testing.T) {
	if mb := rssMB(os.Getpid()); mb <= 0 {
		t.Fatalf("rssMB(self) = %.1f, want > 0", mb)
	}
	if mb := rssMB(-1); mb != 0 {
		t.Errorf("rssMB(invalid pid) = %.1f, want 0", mb)
	}
}

// TestRunLoadHTTPTest checks the fortio-backed runLoad (ADR-0051) drives a real local server and
// returns sane throughput + latency — the non-gated assertion for scenario load-via-fortio.
func TestRunLoadHTTPTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	res := runLoad(context.Background(), srv.URL+"/", `{}`, 4, 500*time.Millisecond, nil)
	if res.rps <= 0 {
		t.Errorf("runLoad rps = %.0f, want > 0 (load did not flow)", res.rps)
	}
	if res.latency.P99 <= 0 {
		t.Errorf("runLoad p99 = %s, want > 0", res.latency.P99)
	}
	if res.latency.P50 > res.latency.Max {
		t.Errorf("p50 (%s) must not exceed max (%s)", res.latency.P50, res.latency.Max)
	}
}

// TestRunLoadSendsHeaders is the regression for the "0 req/s" bug: the containerd footprint lane
// deploys into an isolated namespace, so its load MUST carry the X-Funcd-Namespace header — without
// it the data plane resolves to "default", every request 404s, and throughput reads 0. runLoad must
// apply the caller's headers to the requests it sends.
func TestRunLoadSendsHeaders(t *testing.T) {
	var sawNS, sawAny atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAny.Store(true)
		if r.Header.Get("X-Funcd-Namespace") == "funcd-bench" {
			sawNS.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := runLoad(context.Background(), srv.URL+"/", `{}`, 2, 300*time.Millisecond,
		map[string]string{"X-Funcd-Namespace": "funcd-bench"})
	if res.rps <= 0 || !sawAny.Load() {
		t.Fatalf("runLoad rps = %.0f, want > 0 (no load flowed)", res.rps)
	}
	if !sawNS.Load() {
		t.Error("server never saw X-Funcd-Namespace=funcd-bench — the load must carry the namespace header")
	}
}
