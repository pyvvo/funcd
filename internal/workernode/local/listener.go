package local

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime"
)

// TokenHeader carries, on every request to a dev container worker's own local API listener, the token funcd issued
// the worker (ADR-0346 Decision 6). The listener strips it before any handler, so no pass-through forwards it.
const TokenHeader = "X-Funcd-Invoke-Token"

// listening is one dev container worker's local API listener on the host's 127.0.0.1 (ADR-0346 Decision 6).
type listening struct {
	ref    string // the key of the caller it serves, which Remove matches
	url    string
	token  string
	cancel context.CancelFunc // closes srv
	done   chan struct{}      // closed once srv.Serve returns
}

// ListenerFor ensures the local API of caller ref is served for the worker id on a listener of its own, on the host's
// 127.0.0.1, and returns its URL and the token every request must carry in TokenHeader (ADR-0346 Decision 6): the
// listener fixes its caller as a socket does, so ADR-0064's connection-scoped identity holds. Idempotent per id.
// ReleaseListener, Remove of ref and Close stop it.
func (m *Manager) ListenerFor(ref Ref, id runtime.InstanceID) (url, token string, err error) {
	const op = "workernode.local.ListenerFor"
	if !ref.Valid() {
		return "", "", fault.Invalidf(op, "caller %q of kind %q is no Function and no Job's run", ref, ref.Kind)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", "", fault.Unavailablef(op, "the local API manager is closed")
	}
	if l, ok := m.listeners[id]; ok {
		return l.url, l.token, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fault.Wrapf(err, fault.Internal, op, "make the token of worker %s", id)
	}
	token = hex.EncodeToString(b)
	sctx, cancel := context.WithCancel(m.ctx) // child of m.ctx: cancelled by ReleaseListener, Remove OR Close
	ln, srv, err := listen(sctx, op, "tcp", "127.0.0.1:0", tokenChecked(token, m.handlerFor(ref), m.logger), m.logger)
	if err != nil {
		cancel()
		return "", "", err
	}
	l := &listening{ref: ref.key(), url: "http://" + ln.Addr().String(), token: token, cancel: cancel, done: make(chan struct{})}
	m.listeners[id] = l
	m.serves.Add(1)
	go func() {
		defer m.serves.Done()
		_ = srv.Serve(ln)
		close(l.done)
	}()
	m.logger.Debug("serving a dev container worker's local API", "caller", ref.String(), "worker", string(id), "url", l.url)
	return l.url, token, nil
}

// ReleaseListener stops worker id's listener, if any, and returns once it is closed (ADR-0346 Decision 6).
func (m *Manager) ReleaseListener(id runtime.InstanceID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseLocked(id)
}

// releaseLocked stops id's listener; the caller holds mu.
func (m *Manager) releaseLocked(id runtime.InstanceID) {
	l, ok := m.listeners[id]
	if !ok {
		return
	}
	delete(m.listeners, id)
	l.cancel()
	<-l.done
}

// tokenChecked serves h only a request whose TokenHeader holds token, compared in constant time, and strips the
// header first; any other request gets 403 (ADR-0346 Decision 6), answered by the interceptor (ADR-0330 Decision 3)
// under the header's name, since no endpoint is chosen yet.
func tokenChecked(token string, h http.Handler, logger *slog.Logger) http.Handler {
	want := []byte(token)
	refuse := wrap(logger, TokenHeader, nil, func(http.ResponseWriter, *http.Request) error {
		return fault.Forbiddenf("workernode.local.listener", "a request to this worker's local API must carry its token in %s",
			TokenHeader)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Values(TokenHeader)
		if len(got) != 1 || subtle.ConstantTimeCompare([]byte(got[0]), want) != 1 {
			refuse(w, r)
			return
		}
		r.Header.Del(TokenHeader)
		h.ServeHTTP(w, r)
	})
}
