// Package bench is the funcd benchmark & sustainability harness (ADR-0040): it embeds the
// platform, drives the public data plane, and samples process memory to answer whether the
// platform is sustainable on the target box (RAM-bound, scale-to-zero). It compares the
// memory and file substrate (blob + JetStream bus); the store is memory in both.
//
// Load generation + latency percentiles use fortio, RSS sampling uses gopsutil — both test-only
// libraries confined to this package (ADR-0051).
package bench

import (
	"context"
	"io"
	"time"

	"fortio.org/fortio/fhttp"
	"fortio.org/fortio/periodic"
	flog "fortio.org/log"
)

// Latency is the tail-latency distribution of a load phase.
type Latency struct {
	P50  time.Duration `json:"p50"`
	P90  time.Duration `json:"p90"`
	P99  time.Duration `json:"p99"`
	P999 time.Duration `json:"p999"`
	Max  time.Duration `json:"max"`
}

// loadResult is the outcome of a load phase: sustained throughput + latency.
type loadResult struct {
	rps     float64
	latency Latency
}

// runLoad drives closed-model load at url for dur — `workers` connections each POSTing as fast as
// they can (fortio `NumThreads` + `QPS:-1`), keep-alive — and returns the successful (200) req/s and
// the latency distribution from fortio's histogram (ADR-0051). ctx cancellation aborts the run.
// headers (e.g. X-Funcd-Namespace for the isolated-namespace containerd lane) are sent on every
// request; nil/empty sends none.
func runLoad(ctx context.Context, url, body string, workers int, dur time.Duration, headers map[string]string) loadResult {
	flog.SetLogLevel(flog.Error) // quiet fortio's logger (idempotent); its summary goes to opts.Out below

	opts := &fhttp.HTTPRunnerOptions{}
	opts.URL = url
	opts.Payload = []byte(body)
	opts.ContentType = "application/json"
	for k, v := range headers {
		// fortio takes extra headers as "Name: Value"; AddAndValidateExtraHeader normalizes them.
		_ = opts.AddAndValidateExtraHeader(k + ": " + v)
	}
	opts.NumThreads = workers
	opts.QPS = -1 // closed model: each connection fires as fast as it can
	opts.Duration = dur
	opts.Percentiles = []float64{50, 90, 99, 99.9}
	opts.Out = io.Discard // silence fortio's per-run summary; the bench prints its own lines
	// Hold the aborter in a local: fortio's RunHTTPTest mutates opts.Stop internally (and clears it
	// on teardown), so a goroutine reading opts.Stop races to a nil dereference when ctx is cancelled
	// mid-load — a SIGINT panic that, firing in a spawned goroutine, would crash the process and
	// bypass the bench's deferred graceful teardown. The local reference is stable.
	stop := periodic.NewAborter()
	opts.Stop = stop

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stop.Abort(false)
		case <-done:
		}
	}()

	res, err := fhttp.RunHTTPTest(opts)
	if err != nil || res == nil || res.DurationHistogram == nil {
		return loadResult{}
	}

	rps := 0.0
	if res.ActualDuration > 0 {
		rps = float64(res.RetCodes[200]) / res.ActualDuration.Seconds() // successful throughput
	}
	h := res.DurationHistogram
	sec := func(p float64) time.Duration { return time.Duration(h.CalcPercentile(p) * float64(time.Second)) }
	return loadResult{
		rps: rps,
		latency: Latency{
			P50: sec(50), P90: sec(90), P99: sec(99), P999: sec(99.9),
			Max: time.Duration(h.Max * float64(time.Second)),
		},
	}
}
