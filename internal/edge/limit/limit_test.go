package limit_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// counter is a next-handler that records how many times it was called (200 OK).
type counter struct{ n atomic.Int64 }

func (c *counter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.n.Add(1)
	w.WriteHeader(http.StatusOK)
}

func do(h http.Handler, method, path, remote string, contentLength int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://x"+path, nil)
	req.RemoteAddr = remote
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// scenario: limits-disabled-passthrough
func TestScenarioLimitsDisabledPassthrough(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{})(next)
	for i := 0; i < 100; i++ {
		require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)
	}
	require.Equal(t, int64(100), next.n.Load())
}

// scenario: under-limit-passes
func TestScenarioUnderLimitPasses(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 6000, Burst: 10, MaxBodyBytes: 1024, MaxInFlight: 10})(next)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "1.1.1.1:1", 100).Code)
	}
	require.Equal(t, int64(5), next.n.Load())
}

// scenario: over-rate-429-no-wake — the reject never calls next (the activator-proxy stand-in).
func TestScenarioOverRate429NoWake(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 60, Burst: 2, Key: limit.KeyClientIP})(next)
	codes := map[int]int{}
	for i := 0; i < 6; i++ {
		codes[do(h, "GET", "/function/x", "9.9.9.9:1", 0).Code]++
	}
	require.Equal(t, 2, codes[http.StatusOK], "burst of 2 passes")
	require.Equal(t, 4, codes[http.StatusTooManyRequests], "the rest are 429")
	require.Equal(t, int64(2), next.n.Load(), "rejected requests NEVER reach next (no wake)")
	// 429 carries Retry-After.
	rec := do(h, "GET", "/function/x", "9.9.9.9:1", 0)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// scenario: rate-key-client-ip — different IPs get independent buckets.
func TestScenarioRateKeyClientIP(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 60, Burst: 1, Key: limit.KeyClientIP})(next)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)
	require.Equal(t, http.StatusTooManyRequests, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "2.2.2.2:1", 0).Code, "a different IP has its own bucket")
}

// Under key: function the rate step is not Chain's (ADR-0164): Chain passes every call, and only a
// function-keyed config builds a TargetLimiter; a nil one checks nothing.
func TestChainSkipsRateUnderFunctionKey(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 60, Burst: 1, Key: limit.KeyFunction})(next)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)
	}
	require.Nil(t, limit.NewTargetLimiter(limit.Config{RatePerMin: 60, Key: limit.KeyClientIP}))
	require.Nil(t, limit.NewTargetLimiter(limit.Config{Key: limit.KeyFunction}))
	var none *limit.TargetLimiter
	require.False(t, none.Throttle(httptest.NewRecorder(), "default", "x"))
}

// scenario: over-size-413 — Content-Length over the cap is rejected before next.
func TestScenarioOverSize413(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{MaxBodyBytes: 1024})(next)
	require.Equal(t, http.StatusOK, do(h, "POST", "/function/x", "1.1.1.1:1", 1024).Code)
	rec := do(h, "POST", "/function/x", "1.1.1.1:1", 2048)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, int64(1), next.n.Load(), "the oversized request never reached next")
}

// scenario: over-concurrency-503 — the M+1th concurrent request gets 503.
func TestScenarioOverConcurrency503(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8) // non-blocking entry signal (never full for this test)
	blocking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release // returns immediately once release is closed
		w.WriteHeader(http.StatusOK)
	})
	h := limit.Chain(limit.Config{MaxInFlight: 2})(blocking)

	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code }()
	}
	<-entered // both slots held
	<-entered

	// The 3rd request finds the ceiling full → 503 (does not block, never enters the handler).
	require.Equal(t, http.StatusServiceUnavailable, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)

	close(release)
	require.Equal(t, http.StatusOK, <-codes)
	require.Equal(t, http.StatusOK, <-codes)
	// After the held requests complete, a slot frees and a new request passes.
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "1.1.1.1:1", 0).Code)
}

// scenario: full-map-refuses-new-key
func TestScenarioFullMapRefusesNewKey(t *testing.T) {
	clk := clock.NewManual(time.Unix(1_700_000_000, 0))
	next := &counter{}
	h := limit.ChainAt(limit.Config{RatePerMin: 60, Burst: 1, Key: limit.KeyClientIP, MaxKeys: 2}, clk)(next)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "10.0.0.1:1", 0).Code, "client A")
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "10.0.0.2:1", 0).Code, "client B")

	clk.Advance(500 * time.Millisecond)
	rec := do(h, "GET", "/function/x", "10.0.0.3:1", 0)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "client C finds no free bucket")
	require.Equal(t, "1", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "rate limit: no free bucket for a new key")
	require.Contains(t, rec.Body.String(), "urn:funcd:problem:resource-exhausted")

	clk.Advance(time.Second)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "10.0.0.3:1", 0).Code, "a refilled bucket frees its slot")
	require.Equal(t, int64(3), next.n.Load())
}

// scenario: client-ip-drained-bucket-survives-ip-flood
func TestScenarioClientIPDrainedBucketSurvivesIPFlood(t *testing.T) {
	const maxKeys = 4096
	clk := clock.NewManual(time.Unix(1_700_000_000, 0))
	next := &counter{}
	h := limit.ChainAt(limit.Config{RatePerMin: 1, Burst: 5, Key: limit.KeyClientIP, MaxKeys: maxKeys}, clk)(next)
	served := func(remote string, n int) (ok, refused int) {
		for i := 0; i < n; i++ {
			switch do(h, "GET", "/function/x", remote, 0).Code {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				refused++
			}
		}
		return ok, refused
	}
	ok, _ := served("192.0.2.1:1", 10)
	floodOK, floodRefused := 0, 0
	for i := 0; i < maxKeys; i++ {
		o, r := served(fmt.Sprintf("10.%d.%d.1:1", i/256, i%256), 1)
		floodOK, floodRefused = floodOK+o, floodRefused+r
	}
	again, refused := served("192.0.2.1:1", 5)
	require.Equal(t, 5, ok+again, "X is served its burst once, never again after the flood")
	require.Equal(t, 5, refused, "X's last 5 calls get 429")
	require.Equal(t, maxKeys-1, floodOK, "the flood fills the free buckets")
	require.Equal(t, 1, floodRefused, "the flood IP beyond the free buckets gets 429")
}

// No bucket that has not refilled to burst is evicted for a new key; a refilled one is.
func TestBucketTableEvictsOnlyRefilledBuckets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		elapsed    time.Duration
		victimGets int
		newKeyOK   bool
	}{
		{name: "drained", elapsed: 0, victimGets: 0, newKeyOK: false},
		{name: "partly-refilled", elapsed: 2 * time.Minute, victimGets: 2, newKeyOK: false},
		{name: "refilled", elapsed: 3 * time.Minute, victimGets: 3, newKeyOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock.NewManual(time.Unix(1_700_000_000, 0))
			l := limit.NewTargetLimiterAt(limit.Config{RatePerMin: 1, Burst: 3, Key: limit.KeyFunction, MaxKeys: 1}, clk)
			for i := 0; i < 3; i++ {
				require.False(t, l.Throttle(httptest.NewRecorder(), "ns", "victim"))
			}
			clk.Advance(tc.elapsed)
			require.Equal(t, !tc.newKeyOK, l.Throttle(httptest.NewRecorder(), "ns", "other"))
			require.Equal(t, 1, l.Buckets(), "the table never exceeds maxKeys")
			if tc.newKeyOK {
				return
			}
			got := 0
			for i := 0; i < 5; i++ {
				if !l.Throttle(httptest.NewRecorder(), "ns", "victim") {
					got++
				}
			}
			require.Equal(t, tc.victimGets, got, "the victim keeps its own tokens, never a fresh bucket")
		})
	}
}

// A take moves its bucket's refill time later, so the table reorders: after A's second take B refills first,
// and a new key takes B's place.
func TestBucketTableReordersOnTake(t *testing.T) {
	clk := clock.NewManual(time.Unix(1_700_000_000, 0))
	l := limit.NewTargetLimiterAt(limit.Config{RatePerMin: 60, Burst: 2, Key: limit.KeyFunction, MaxKeys: 2}, clk)
	require.False(t, l.Throttle(httptest.NewRecorder(), "ns", "a"))
	clk.Advance(500 * time.Millisecond)
	require.False(t, l.Throttle(httptest.NewRecorder(), "ns", "b"))
	clk.Advance(100 * time.Millisecond)
	require.False(t, l.Throttle(httptest.NewRecorder(), "ns", "a"))
	clk.Advance(time.Second)
	require.False(t, l.Throttle(httptest.NewRecorder(), "ns", "c"), "b is full again, so the new key takes its place")
	require.Equal(t, 2, l.Buckets())
}

// Issue #89: under key: function a flood of distinct, very long names must not make the limiter retain
// MaxKeys × name length — the table is bounded by MaxKeys alone, whatever the key size.
func TestIssue89_FunctionKeyMemoryBounded(t *testing.T) {
	const (
		maxKeys = 64
		segLen  = 64 << 10
	)
	l := limit.NewTargetLimiter(limit.Config{RatePerMin: 60, Key: limit.KeyFunction, MaxKeys: maxKeys})
	before := liveHeap()
	for i := 0; i < maxKeys; i++ {
		name := v1.ObjectName(strconv.Itoa(i) + strings.Repeat("a", segLen))
		require.False(t, l.Throttle(httptest.NewRecorder(), "default", name))
	}
	grown := liveHeap() - before
	runtime.KeepAlive(l)
	require.Less(t, grown, int64(maxKeys*segLen/8),
		"%d long-name keys left %d live heap bytes: the limiter holds the name itself as its key", maxKeys, grown)
}

func liveHeap() int64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}
