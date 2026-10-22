package function

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0259 Decision 1: a 503 whose body does not arrive within probeTimeout is late, not an answer.
func TestReadiness503BodyPastDeadlineIsLate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	r := newShimReconciler(t, fakeResolver{})
	_, rep, late := r.probeReadiness(context.Background(), addr.IP.String(), addr.Port)
	require.True(t, late)
	require.Nil(t, rep)
}

// ADR-0259 Decision 2: the pool switch reads next itself, and that answer is recorded as next's last answer.
func TestSwitchDueRecordsItsAnswer(t *testing.T) {
	t.Parallel()
	addr, _ := countingServer(t, func(w http.ResponseWriter) { _, _ = w.Write([]byte(`[{"name":"m","state":"ready"}]`)) })
	r := newShimReconciler(t, fakeResolver{})
	now := r.clock.Now()
	from := runtime.Instance{ID: runtime.NewInstanceID("default", "pool", "a", 0), State: runtime.StateRunning, Listened: true, IP: "127.0.0.1", Port: 1}
	next := runtime.Instance{ID: runtime.NewInstanceID("default", "pool", "b", 0), CreatedAt: now, State: runtime.StateRunning,
		Listened: true, IP: addr.IP.String(), Port: addr.Port}
	d := poolDrain{next: next.ID, from: from.ID, since: now, carried: []v1.ObjectName{"m"}}

	require.True(t, r.switchDue(context.Background(), d, next, []runtime.Instance{from}, []v1.ObjectName{"m"}, now))
	r.readMu.Lock()
	rec := r.reads[next.ID]
	r.readMu.Unlock()
	require.NotNil(t, rec.last, "the switch's answer is next's last answer")
	require.True(t, rec.last.listed)
}
