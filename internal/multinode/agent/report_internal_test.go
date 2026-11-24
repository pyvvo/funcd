package agent

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// A delete that the control plane did not take is sent again, until it does.
func TestADeleteTheControlPlaneDidNotTakeIsSentAgain(t *testing.T) {
	ctx := context.Background()
	away, asked := true, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, multinode.PathObjects, r.URL.Path)
		require.Equal(t, url.Values{"kind": {"JobRun"}, "namespace": {"e1"}, "name": {"report-1"}, "uid": {"u1"}}, r.URL.Query())
		if away {
			http.Error(w, "away", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	a := &Agent{cfg: Config{Store: store.New(memory.New())}, id: identity{name: "vm1", server: srv.URL, client: srv.Client()},
		log: slog.New(slog.DiscardHandler), mine: map[v1.NamespaceName]bool{"e1": true}, pushed: map[string]string{}, unsent: map[string]url.Values{}}
	run := &v1.JobRun{}
	run.Name, run.Namespace, run.UID = "report-1", "e1", "u1"
	a.push(ctx, store.Deleted, run)
	require.Equal(t, 1, asked)
	a.resendDeletes(ctx)
	require.Len(t, a.unsent, 1, "still away")
	away = false
	a.resendDeletes(ctx)
	a.resendDeletes(ctx)
	require.Equal(t, 3, asked, "once it was taken, it is not sent again")
}
