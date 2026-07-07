package loadgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// scenario: bench-reports-throughput-and-latency (engine half) — load against a live server returns
// sane throughput + tail latency, all requests counted ok.
func TestRunReportsThroughputAndLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, err := Run(context.Background(), Options{URL: srv.URL, Concurrency: 4, Duration: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Total == 0 || res.OK != res.Total {
		t.Fatalf("total=%d ok=%d errors=%d, want total>0 and ok==total", res.Total, res.OK, res.Errors)
	}
	if res.RPS <= 0 {
		t.Errorf("rps=%v, want > 0", res.RPS)
	}
	if res.P99 <= 0 {
		t.Errorf("p99=%v, want > 0", res.P99)
	}
	if res.P50 > res.Max {
		t.Errorf("p50 (%v) must not exceed max (%v)", res.P50, res.Max)
	}
}

// scenario: bench-target-unreachable (engine half) — a refused address yields all errors, no panic,
// and Run does NOT return an error (transport failures are counted, not returned).
func TestRunUnreachableCountsErrors(t *testing.T) {
	res, err := Run(context.Background(), Options{URL: "http://127.0.0.1:1/x", Requests: 3, Concurrency: 1})
	if err != nil {
		t.Fatalf("Run returned %v; transport failures must be counted in Result.Errors, not returned", err)
	}
	if res.OK != 0 || res.Errors == 0 {
		t.Fatalf("ok=%d errors=%d, want ok==0 and errors>0", res.OK, res.Errors)
	}
}

// empty URL is a usage error (returned, not counted).
func TestRunEmptyURL(t *testing.T) {
	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("Run with empty URL: want an error")
	}
}

func TestPercentile(t *testing.T) {
	s := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(s, 100); got != 10 {
		t.Errorf("p100=%v, want 10", got)
	}
	if got := percentile(s, 0); got != 1 {
		t.Errorf("p0=%v, want 1", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("percentile(empty)=%v, want 0", got)
	}
}
