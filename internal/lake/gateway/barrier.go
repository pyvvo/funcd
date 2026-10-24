package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// errNotSaved fails an engine answer that no catalog save covered: the client gets 503 "lake catalog not saved", or a
// broken connection once the answer's headers went out (ADR-0279 Decision 3).
var errNotSaved = errors.New("lake catalog not saved")

// instanceKey carries, in a forwarded request's context, the engine instance the barrier knew when it forwarded it.
type instanceKey struct{}

// saveBarrier holds a Lake engine's answers until the engine's save endpoint reports a catalog save that covers them
// (ADR-0279 Decision 3). It knows one engine instance, learned with a save call before the first request it lets
// through and again after a save call names another; an answer is released only by the instance that gave it.
type saveBarrier struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	known  string
}

// saveReport is the save endpoint's answer: the engine instance, and the snapshot a good save covers or the error.
type saveReport struct {
	Engine string `json:"engine"`
	Saved  int64  `json:"saved"`
	Error  string `json:"error"`
}

// call makes one save call. It returns the instance the endpoint names, which it names even when the save failed.
func (b *saveBarrier) call(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNotSaved, err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNotSaved, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rep saveReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&rep); err != nil || rep.Engine == "" {
		return "", fmt.Errorf("%w: the save endpoint answered %s with no engine instance", errNotSaved, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return rep.Engine, fmt.Errorf("%w: %s: %s", errNotSaved, resp.Status, rep.Error)
	}
	return rep.Engine, nil
}

// instance is the engine instance the barrier knows, learned with a save call when it knows none.
func (b *saveBarrier) instance(ctx context.Context) (string, error) {
	b.mu.Lock()
	known := b.known
	b.mu.Unlock()
	if known != "" {
		return known, nil
	}
	engine, err := b.call(ctx)
	if engine != "" {
		b.learn(engine)
	}
	return engine, err
}

func (b *saveBarrier) learn(engine string) {
	b.mu.Lock()
	b.known = engine
	b.mu.Unlock()
}

// release makes the one save call an answer gets, never retried: nil only when the instance that gave the answer
// reports a good save. Another instance is learned, and fails the answer.
func (b *saveBarrier) release(ctx context.Context, gave string) error {
	engine, err := b.call(ctx)
	if engine != "" && engine != gave {
		b.learn(engine)
		return fmt.Errorf("%w: the engine restarted (instance %s, then %s)", errNotSaved, gave, engine)
	}
	return err
}

// hold is the proxy's ModifyResponse: an answer with no body waits here with its headers; any other goes out but its
// last byte, which waits for the save (heldTail).
func (b *saveBarrier) hold(resp *http.Response) error {
	ctx := resp.Request.Context()
	gave, _ := ctx.Value(instanceKey{}).(string)
	release := func() error { return b.release(ctx, gave) }
	if resp.ContentLength == 0 {
		return release()
	}
	resp.Body = &heldTail{body: resp.Body, release: release}
	return nil
}

// heldTail passes an engine answer through but its last byte, which it gives only once release succeeds, so a client
// completes no answer before a save covers it; a failed release ends the body with errNotSaved, which breaks the
// connection.
type heldTail struct {
	body     io.ReadCloser
	release  func() error
	hold     byte
	held     bool
	released bool
}

func (h *heldTail) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if h.released {
		if !h.held {
			return 0, io.EOF
		}
		p[0], h.held = h.hold, false
		return 1, io.EOF
	}
	k := 0
	if h.held {
		p[0], k = h.hold, 1
	}
	m, err := h.body.Read(p[k:])
	n := k + m
	h.held = n > 0
	if h.held {
		n--
		h.hold = p[n]
	}
	switch {
	case errors.Is(err, io.EOF):
		if rerr := h.release(); rerr != nil {
			return n, rerr
		}
		h.released = true
		return n, nil
	case err != nil:
		return n, err
	}
	return n, nil
}

func (h *heldTail) Close() error { return h.body.Close() }
