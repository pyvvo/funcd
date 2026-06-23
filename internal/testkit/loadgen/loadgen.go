// Package loadgen is a small, stdlib-only client-side HTTP load generator (ADR-0053). It drives a
// target URL with concurrent workers (closed model) and reports throughput + tail latency — the
// engine behind `funcdctl bench` (the funcd analogue of `nats bench`).
//
// It is deliberately HAND-ROLLED on the standard library — net/http + sort + sync — so the shipped
// funcdctl binary carries no benchmarking dependency: ADR-0051 confines gopsutil/fortio to the
// funcd-bench harness, and this package keeps that true (it must NEVER import them). It is a client
// tool, not the funcd-bench embed/memory harness (ADR-0040/0052).
package loadgen

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
)

const (
	defaultMethod      = http.MethodPost
	defaultContentType = "application/json"
	defaultConcurrency = 8
	defaultDuration    = 5 * time.Second
	requestTimeout     = 30 * time.Second
)

// Options configures a load run.
type Options struct {
	URL         string
	Method      string        // default POST
	Body        string        // request body, sent as-is each request
	ContentType string        // default application/json
	Concurrency int           // worker goroutines (default 8)
	Duration    time.Duration // closed-model run length (used when Requests == 0; default 5s)
	Requests    int           // total requests across workers; 0 ⇒ use Duration
}

// Result is the measured outcome of a run.
type Result struct {
	Total    int           `json:"total"`
	OK       int           `json:"ok"`
	Errors   int           `json:"errors"`
	Duration time.Duration `json:"duration"` // actual elapsed wall-clock of the run
	RPS      float64       `json:"rps"`      // OK / Duration.Seconds()
	P50      time.Duration `json:"p50"`
	P90      time.Duration `json:"p90"`
	P99      time.Duration `json:"p99"`
	Max      time.Duration `json:"max"`
}

// Run drives o.URL under load and returns the measured Result. It honors ctx cancellation. Transport
// failures (connection refused, timeout, non-2xx) are counted in Result.Errors — they are NOT
// returned; the only returned error is a usage problem (empty URL). The caller decides whether OK==0
// is fatal.
func Run(ctx context.Context, o Options) (Result, error) {
	const op = "loadgen.Run"
	if o.URL == "" {
		return Result{}, fault.Invalidf(op, "URL must not be empty")
	}
	o = withDefaults(o)

	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{MaxIdleConns: o.Concurrency * 2, MaxIdleConnsPerHost: o.Concurrency},
	}
	defer client.CloseIdleConnections()

	var consumed int64 // request-count mode: workers stop once this exceeds the budget
	budget := int64(o.Requests)
	var deadline time.Time
	if o.Requests <= 0 {
		deadline = time.Now().Add(o.Duration)
	}

	type out struct {
		lat        []time.Duration
		ok, errors int
	}
	outs := make([]out, o.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()
	for w := range outs {
		wg.Add(1)
		go func(wo *out) {
			defer wg.Done()
			for ctx.Err() == nil {
				if o.Requests > 0 {
					if atomic.AddInt64(&consumed, 1) > budget {
						return
					}
				} else if !time.Now().Before(deadline) {
					return
				}
				lat, ok := doOne(ctx, client, o)
				wo.lat = append(wo.lat, lat)
				if ok {
					wo.ok++
				} else {
					wo.errors++
				}
			}
		}(&outs[w])
	}
	wg.Wait()
	elapsed := time.Since(start)

	all := make([]time.Duration, 0, len(outs))
	res := Result{Duration: elapsed}
	for i := range outs {
		all = append(all, outs[i].lat...)
		res.OK += outs[i].ok
		res.Errors += outs[i].errors
	}
	res.Total = res.OK + res.Errors
	if elapsed > 0 {
		res.RPS = float64(res.OK) / elapsed.Seconds()
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	res.P50, res.P90, res.P99, res.Max = percentile(all, 50), percentile(all, 90), percentile(all, 99), percentile(all, 100)
	return res, nil
}

// doOne issues a single request and returns its latency + whether it was a 2xx. A transport error
// (refused/timeout) returns the elapsed time and false; it is never fatal.
func doOne(ctx context.Context, client *http.Client, o Options) (time.Duration, bool) {
	req, err := http.NewRequestWithContext(ctx, o.Method, o.URL, bytes.NewReader([]byte(o.Body)))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", o.ContentType)
	start := time.Now()
	resp, err := client.Do(req)
	lat := time.Since(start)
	if err != nil {
		return lat, false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return lat, resp.StatusCode >= 200 && resp.StatusCode < 300
}

// percentile returns the p-th (0..100) percentile of a sorted slice via nearest-rank; 0 if empty.
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(p/100*float64(n-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

func withDefaults(o Options) Options {
	if o.Method == "" {
		o.Method = defaultMethod
	}
	if o.ContentType == "" {
		o.ContentType = defaultContentType
	}
	if o.Concurrency < 1 {
		o.Concurrency = defaultConcurrency
	}
	if o.Requests <= 0 && o.Duration <= 0 {
		o.Duration = defaultDuration
	}
	return o
}
