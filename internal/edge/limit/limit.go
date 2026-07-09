// Package limit is the ingress-protection middleware (ADR-0112, F75): it refuses abusive traffic on
// the data-plane chain BEFORE dataplane.Handler — and therefore before the activator can wake a
// scaled-to-zero sandbox (the reject returns without ever calling activator.ServeHTTP). Three
// composable net/http middlewares: a token-bucket rate limit (429), a Content-Length body-size cap
// (413), and an in-flight concurrency ceiling (503), all RFC 9457 problem+json. It is inserted as the
// innermost middleware (Recover → RequestID → limit → handler) so rejects stay panic-guarded and
// X-Request-Id-correlated while still preceding the activator.
package limit

import (
	"container/list"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/green-0-rabbit/funcd/api/fault"
)

const op = "edge.limit"

// Key selects the rate-limit bucket dimension.
type Key string

const (
	// KeyClientIP buckets per client IP (anti-abuse).
	KeyClientIP Key = "clientIP"
	// KeyFunction buckets per /function/<name> head (the first two path segments) — NOT the raw
	// path, so a rest-varying flood shares one bucket and cannot churn the key map.
	KeyFunction Key = "function"
)

const defaultMaxKeys = 4096

// Config configures the three limiters. A zero Config is a pass-through.
type Config struct {
	RatePerMin   int   // token-bucket refill (requests per minute); 0 ⇒ rate limit off
	Burst        int   // bucket depth; 0 ⇒ = RatePerMin
	Key          Key   // clientIP (default) | function
	MaxBodyBytes int64 // 413 over this (Content-Length); 0 ⇒ size cap off
	MaxInFlight  int   // 503 over this many concurrent; 0 ⇒ concurrency cap off
	MaxKeys      int   // LRU cap on the rate-limiter key map; 0 ⇒ defaultMaxKeys
}

// Chain returns a single middleware composing rate → size → concurrency (cheapest reject first). A
// zero Config is a pass-through.
func Chain(cfg Config) func(http.Handler) http.Handler {
	var rl *rateLimiter
	if cfg.RatePerMin > 0 {
		rl = newRateLimiter(cfg)
	}
	var sem chan struct{}
	if cfg.MaxInFlight > 0 {
		sem = make(chan struct{}, cfg.MaxInFlight)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// rate → size → concurrency
			if rl != nil {
				if delay, ok := rl.allow(r); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
					fault.WriteProblem(w, fault.ResourceExhaustedf(op, "rate limit exceeded"))
					return
				}
			}
			if cfg.MaxBodyBytes > 0 {
				if r.ContentLength > cfg.MaxBodyBytes {
					fault.WriteProblem(w, fault.PayloadTooLargef(op, "request body exceeds %d bytes", cfg.MaxBodyBytes))
					return
				}
				// Defense-in-depth for a lying/chunked length (best-effort; may trip downstream).
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

// rateLimiter holds per-key token buckets in a true LRU bounded by maxKeys.
type rateLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	key     Key
	maxKeys int
	ll      *list.List               // front = most-recently-used
	entries map[string]*list.Element // key → element holding *bucket
}

type bucket struct {
	key string
	lim *rate.Limiter
}

func newRateLimiter(cfg Config) *rateLimiter {
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.RatePerMin
	}
	maxKeys := cfg.MaxKeys
	if maxKeys <= 0 {
		maxKeys = defaultMaxKeys
	}
	key := cfg.Key
	if key == "" {
		key = KeyClientIP
	}
	return &rateLimiter{
		limit:   rate.Limit(float64(cfg.RatePerMin) / 60.0), // per second
		burst:   burst,
		key:     key,
		maxKeys: maxKeys,
		ll:      list.New(),
		entries: map[string]*list.Element{},
	}
}

// allow reserves a token for the request's key; ok=false ⇒ over limit, with the delay until the next.
func (rl *rateLimiter) allow(r *http.Request) (time.Duration, bool) {
	k := rl.keyFor(r)
	rl.mu.Lock()
	lim := rl.getLocked(k)
	rl.mu.Unlock()
	res := lim.Reserve()
	if !res.OK() {
		return 0, false
	}
	if d := res.Delay(); d > 0 {
		res.Cancel() // don't consume a future token; reject now
		return d, false
	}
	return 0, true
}

// getLocked returns the key's bucket, creating it (and evicting the LRU tail past maxKeys) as needed.
func (rl *rateLimiter) getLocked(k string) *rate.Limiter {
	if el, ok := rl.entries[k]; ok {
		rl.ll.MoveToFront(el)
		return el.Value.(*bucket).lim
	}
	b := &bucket{key: k, lim: rate.NewLimiter(rl.limit, rl.burst)}
	rl.entries[k] = rl.ll.PushFront(b)
	if rl.ll.Len() > rl.maxKeys {
		tail := rl.ll.Back()
		if tail != nil {
			rl.ll.Remove(tail)
			delete(rl.entries, tail.Value.(*bucket).key)
		}
	}
	return b.lim
}

func (rl *rateLimiter) keyFor(r *http.Request) string {
	if rl.key == KeyFunction {
		return functionHead(r.URL.Path)
	}
	return clientIP(r)
}

// functionHead returns "/function/<name>" (first two path segments) or the cleaned first segment.
func functionHead(path string) string {
	segs := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(segs) >= 2 {
		return "/" + segs[0] + "/" + segs[1]
	}
	return path
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
