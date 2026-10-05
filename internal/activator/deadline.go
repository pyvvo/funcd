package activator

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// DeadlineOp is the op of the fault an external invoke gets when its response does not start in time (ADR-0151).
const DeadlineOp = "activator.response-deadline"

// TimeoutHeader carries the time left for a call to a worker, in whole milliseconds rounded up, so a pool can arm
// its own timer (ADR-0151). Relative, like grpc-timeout, so funcd and the worker need no shared clock.
const TimeoutHeader = "X-Funcd-Timeout-Ms"

// ResponseDeadline bounds an external invoke from its receipt until the upstream's headers reach the activator.
// Limit and Source name the bound in the 504's detail.
type ResponseDeadline struct {
	At     time.Time
	Limit  time.Duration
	Source string // "spec.timeout" or "invoke.defaultTimeout"
}

type deadlineKey struct{}

// WithResponseDeadline returns r carrying d for ServeHTTP and DeadlineTransport. Only the data plane's
// serveFunction sets it, for a request not marked internal.
func WithResponseDeadline(r *http.Request, d ResponseDeadline) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), deadlineKey{}, d))
}

func responseDeadlineFrom(ctx context.Context) (ResponseDeadline, bool) {
	d, ok := ctx.Value(deadlineKey{}).(ResponseDeadline)
	return d, ok
}

func deadlineFault(fn FunctionRef, d ResponseDeadline) *fault.Error {
	return fault.DeadlineExceededf(DeadlineOp, "%s/%s did not start its response within %s (%s)", fn.Namespace, fn.Name, d.Limit, d.Source)
}

// DeadlineTransport sets TimeoutHeader on a copy of each request to the earlier of its context deadline and its
// ResponseDeadline, and deletes it when there is neither, so a caller cannot set it. With a ResponseDeadline it
// cancels the inner request when d.At passes before the response headers, closes a response that arrives later,
// and returns the DeadlineOp fault; a response returned before d.At is never cancelled. The inner response is
// returned unwrapped, so an upgraded (101) body stays an io.ReadWriteCloser.
func DeadlineTransport(rt http.RoundTripper) http.RoundTripper {
	return deadlineTransport{rt: rt}
}

type deadlineTransport struct{ rt http.RoundTripper }

func (t deadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	d, bounded := responseDeadlineFrom(ctx)
	at, hasAt := ctx.Deadline()
	if bounded && (!hasAt || d.At.Before(at)) {
		at, hasAt = d.At, true
	}
	out := req.Clone(ctx)
	if hasAt {
		out.Header.Set(TimeoutHeader, strconv.FormatInt(timeoutMs(time.Until(at)), 10))
	} else {
		out.Header.Del(TimeoutHeader)
	}
	if !bounded {
		return t.rt.RoundTrip(out)
	}

	inner, cancel := context.WithCancel(ctx)
	var (
		mu       sync.Mutex
		returned bool
		expired  bool
	)
	timer := time.AfterFunc(time.Until(d.At), func() {
		mu.Lock()
		defer mu.Unlock()
		if !returned {
			expired = true
			cancel()
		}
	})
	resp, err := t.rt.RoundTrip(out.WithContext(inner))
	mu.Lock()
	returned = true
	late := expired
	mu.Unlock()
	timer.Stop()
	if late {
		if resp != nil {
			_ = resp.Body.Close()
		}
		fn, _ := functionFrom(ctx)
		return nil, deadlineFault(fn, d)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return resp, nil
}

// timeoutMs is left in whole milliseconds, rounded up and at least 1.
func timeoutMs(left time.Duration) int64 {
	ms := int64((left + time.Millisecond - 1) / time.Millisecond)
	return max(ms, 1)
}
