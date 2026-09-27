package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

// scenario: bench-reports-throughput-and-latency — `funcdctl bench --url <live> -d ...` drives the
// endpoint and prints throughput + latency.
func TestScenarioBenchReportsThroughputAndLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--url", srv.URL, "-d", "200ms", "-c", "2"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bench: %v", err)
	}
	if !strings.Contains(out.String(), "req/s") {
		t.Errorf("output missing throughput line:\n%s", out.String())
	}
}

// scenario: bench-target-unreachable (usage half) — no target → fault.Invalid.
func TestScenarioBenchNoTarget(t *testing.T) {
	root := newRootCmd(&bytes.Buffer{})
	root.SetArgs([]string{"bench"})
	if err := root.Execute(); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("no target: want fault.Invalid, got %v", err)
	}
}

// scenario: bench-target-unreachable — a fully-unreachable target (OK==0) → fault.Unavailable.
func TestScenarioBenchUnreachable(t *testing.T) {
	root := newRootCmd(&bytes.Buffer{})
	root.SetArgs([]string{"bench", "--url", "http://127.0.0.1:1/x", "-n", "2", "-c", "1"})
	if err := root.Execute(); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("unreachable: want fault.Unavailable, got %v", err)
	}
}

// scenario: bench-json-output — `--json` emits a parseable Result with the numeric fields.
func TestScenarioBenchJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--url", srv.URL, "-n", "5", "-c", "2", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bench --json: %v", err)
	}
	var res struct {
		Total int     `json:"total"`
		OK    int     `json:"ok"`
		RPS   float64 `json:"rps"`
		P99   int64   `json:"p99"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("json output did not parse: %v\n%s", err, out.String())
	}
	if res.Total == 0 || res.OK == 0 {
		t.Errorf("json result has no requests: %s", out.String())
	}
}
