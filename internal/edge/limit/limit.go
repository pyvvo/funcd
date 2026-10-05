// Package limit is the ingress protection of the data plane (ADR-0112, F75): it refuses abusive traffic
// before the activator can wake a scaled-to-zero sandbox. Chain is the net/http middleware placed before
// dataplane.Handler: a token-bucket rate limit keyed by client IP (429), a Content-Length body-size cap
// (413) and an in-flight concurrency ceiling (503), all RFC 9457 problem+json. Under key: function the rate
// step leaves Chain for TargetLimiter, which dataplane.serveFunction calls once the target Function is
// resolved (ADR-0164), so every host, path and invoke form of one Function shares one bucket.
package limit

import (
	"container/heap"
	"crypto/sha256"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

const op = "edge.limit"

// Key selects the rate-limit bucket dimension.
type Key string

const (
	// KeyClientIP buckets per client IP: the RemoteAddr host; checked by Chain.
	KeyClientIP Key = "clientIP"
	// KeyFunction buckets per resolved (namespace, function); checked by TargetLimiter (ADR-0164).
	KeyFunction Key = "function"
)

const defaultMaxKeys = 4096

// Config configures the three limiters. A zero Config is a pass-through.
type Config struct {
	RatePerMin   int   // token-bucket refill (requests per minute); 0 ⇒ rate limit off
	Burst        int   // bucket depth; 0 ⇒ = RatePerMin
	Key          Key   // clientIP (default) | function
	MaxBodyBytes int64 // 413 over this (Content-Length, or a chunked body unless the upstream already answered); 0 ⇒ size cap off
	MaxInFlight  int   // 503 over this many concurrent; 0 ⇒ concurrency cap off
	MaxKeys      int   // buckets kept (ADR-0164 eviction); ≤ 0 ⇒ 4096
}

// Chain returns a single middleware composing rate → size → concurrency (cheapest reject first). Its rate
// step runs only when cfg.Key is not KeyFunction. A zero Config is a pass-through.
func Chain(cfg Config) func(http.Handler) http.Handler {
	return chain(cfg, clock.System())
}

func chain(cfg Config, clk clock.Clock) func(http.Handler) http.Handler {
	var rl *bucketTable
	if cfg.RatePerMin > 0 && cfg.Key != KeyFunction {
		rl = newBucketTable(cfg, clk)
	}
	var sem chan struct{}
	if cfg.MaxInFlight > 0 {
		sem = make(chan struct{}, cfg.MaxInFlight)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rl != nil {
				if retryAfter, err := rl.take(clientIP(r)); err != nil {
					writeTooMany(w, retryAfter, err)
					return
				}
			}
			if cfg.MaxBodyBytes > 0 {
				if r.ContentLength > cfg.MaxBodyBytes {
					fault.WriteProblem(w, fault.PayloadTooLargef(op, "request body exceeds %d bytes", cfg.MaxBodyBytes))
					return
				}
				// A lying or chunked length: the read past the cap answers 413 downstream, unless the
				// upstream already answered.
				r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)
			}
			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				default:
					fault.WriteProblem(w, fault.Unavailablef(op, "in-flight concurrency ceiling reached"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TargetLimiter is the key: function rate step (ADR-0164). dataplane.serveFunction calls it for an external
// call once the Function is found, so a request for an unknown name creates no bucket.
type TargetLimiter struct{ t *bucketTable }

// NewTargetLimiter returns nil unless RatePerMin > 0 and Key == KeyFunction.
func NewTargetLimiter(cfg Config) *TargetLimiter {
	return newTargetLimiter(cfg, clock.System())
}

func newTargetLimiter(cfg Config, clk clock.Clock) *TargetLimiter {
	if cfg.RatePerMin <= 0 || cfg.Key != KeyFunction {
		return nil
	}
	return &TargetLimiter{t: newBucketTable(cfg, clk)}
}

// Throttle takes one token from the ns/name bucket. When the bucket is drained, or the key is new and the
// table is full of buckets that have not refilled, it writes the 429 and returns true. A nil receiver
// returns false.
func (l *TargetLimiter) Throttle(w http.ResponseWriter, ns v1.NamespaceName, name v1.ObjectName) bool {
	if l == nil {
		return false
	}
	retryAfter, err := l.t.take(string(ns) + "/" + string(name))
	if err != nil {
		writeTooMany(w, retryAfter, err)
		return true
	}
	return false
}

// writeTooMany sets Retry-After before the problem body (ADR-0112 §2), rounded up to whole seconds.
func writeTooMany(w http.ResponseWriter, retryAfter time.Duration, err error) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	fault.WriteProblem(w, err)
}

// bucketTable holds at most maxKeys token buckets. A full table evicts only a bucket that has refilled to
// burst, which is equal to a new one, so no flood of new keys resets a drained bucket (ADR-0164).
type bucketTable struct {
	mu             sync.Mutex
	clk            clock.Clock
	limit          rate.Limit
	burst, maxKeys int
	byKey          map[bucketKey]*bucket
	byFull         fullHeap
}

// bucketKey is the SHA-256 of a bucket's key: a fixed size, so a long key cannot grow the table past
// maxKeys × a constant (the limiter must not itself be a memory-DoS, ADR-0112).
type bucketKey [sha256.Size]byte

type bucket struct {
	key    bucketKey
	lim    *rate.Limiter
	fullAt time.Time // when lim holds burst tokens again; fullAt ≤ now ⇒ equal to a new bucket
	index  int       // position in byFull
}

// fullHeap is a min-heap of buckets ordered by fullAt.
type fullHeap []*bucket

func (h fullHeap) Len() int           { return len(h) }
func (h fullHeap) Less(i, j int) bool { return h[i].fullAt.Before(h[j].fullAt) }
func (h fullHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}

func (h *fullHeap) Push(x any) { //nolint:forbidigo // container/heap.Interface fixes this signature
	b, _ := x.(*bucket)
	b.index = len(*h)
	*h = append(*h, b)
}

func (h *fullHeap) Pop() any { //nolint:forbidigo // container/heap.Interface fixes this signature
	old := *h
	n := len(old)
	b := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return b
}

func newBucketTable(cfg Config, clk clock.Clock) *bucketTable {
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.RatePerMin
	}
	maxKeys := cfg.MaxKeys
	if maxKeys <= 0 {
		maxKeys = defaultMaxKeys
	}
	return &bucketTable{
		clk:     clk,
		limit:   rate.Limit(float64(cfg.RatePerMin) / 60.0), // per second
		burst:   burst,
		maxKeys: maxKeys,
		byKey:   map[bucketKey]*bucket{},
	}
}

// take reserves one token for key at clk.Now(); err is a fault.ResourceExhausted naming why, and retryAfter
// the wait until a token (drained bucket) or a free bucket (full table).
func (t *bucketTable) take(key string) (time.Duration, error) {
	k := bucketKey(sha256.Sum256([]byte(key)))
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clk.Now()
	b, known := t.byKey[k]
	if !known {
		if len(t.byFull) >= t.maxKeys {
			root := t.byFull[0]
			if root.fullAt.After(now) {
				return root.fullAt.Sub(now), fault.ResourceExhaustedf(op, "rate limit: no free bucket for a new key")
			}
			heap.Pop(&t.byFull)
			delete(t.byKey, root.key)
		}
		b = &bucket{key: k, lim: rate.NewLimiter(t.limit, t.burst)}
	}
	res := b.lim.ReserveN(now, 1)
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return d, fault.ResourceExhaustedf(op, "rate limit exceeded")
	}
	missing := float64(t.burst) - b.lim.TokensAt(now)
	b.fullAt = now.Add(time.Duration(math.Ceil(missing / float64(t.limit) * float64(time.Second))))
	if known {
		heap.Fix(&t.byFull, b.index)
	} else {
		t.byKey[k] = b
		heap.Push(&t.byFull, b)
	}
	return 0, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
