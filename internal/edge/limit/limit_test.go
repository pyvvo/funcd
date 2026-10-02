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

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge/limit"
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

// scenario: rate-key-function — a rest-varying flood at one function shares ONE bucket.
func TestScenarioRateKeyFunction(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 60, Burst: 2, Key: limit.KeyFunction})(next)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x/AAA", "1.1.1.1:1", 0).Code)
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x/BBB", "1.1.1.1:1", 0).Code)
	require.Equal(t, http.StatusTooManyRequests, do(h, "GET", "/function/x/CCC", "1.1.1.1:1", 0).Code,
		"rest-varying paths at /function/x share one bucket — no per-request bucket")
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/y", "1.1.1.1:1", 0).Code, "a different function has its own bucket")
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

// LRU eviction bound: the key map never exceeds MaxKeys, and a hot key survives eviction.
func TestRateLimiterLRUBound(t *testing.T) {
	next := &counter{}
	h := limit.Chain(limit.Config{RatePerMin: 60, Burst: 1, Key: limit.KeyClientIP, MaxKeys: 4})(next)
	// Keep "hot" busy, then push 8 cold keys through — hot must not be evicted.
	require.Equal(t, http.StatusOK, do(h, "GET", "/function/x", "hot:1", 0).Code)
	for i := 0; i < 8; i++ {
		do(h, "GET", "/function/x", "cold"+strings.Repeat("z", i)+":1", 0)
		do(h, "GET", "/function/x", "hot:1", 0) // touch hot each round → stays MRU
	}
	// hot has already spent its burst and stayed hot ⇒ still limited (bucket preserved, not reset by eviction).
	require.Equal(t, http.StatusTooManyRequests, do(h, "GET", "/function/x", "hot:1", 0).Code)
}

// Issue #89: under key: function a flood of distinct, very long path heads must not make the limiter
// retain MaxKeys × path length — the key map is bounded by MaxKeys alone, whatever the path size.
func TestIssue89_FunctionKeyMemoryBounded(t *testing.T) {
	const (
		maxKeys = 64
		segLen  = 64 << 10
	)
	for name, shape := range map[string]string{"single-segment": "/%s", "function-name": "/function/%s/x"} {
		t.Run(name, func(t *testing.T) {
			h := limit.Chain(limit.Config{RatePerMin: 60, Key: limit.KeyFunction, MaxKeys: maxKeys})(&counter{})
			before := liveHeap()
			for i := 0; i < maxKeys; i++ {
				do(h, "GET", fmt.Sprintf(shape, strconv.Itoa(i)+strings.Repeat("a", segLen)), "1.1.1.1:1", 0)
			}
			grown := liveHeap() - before
			runtime.KeepAlive(h)
			require.Less(t, grown, int64(maxKeys*segLen/8),
				"%d long-path keys left %d live heap bytes: the limiter holds the path itself as its key", maxKeys, grown)
		})
	}
}

func liveHeap() int64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}
