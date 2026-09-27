package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// fakeReplayer records the replays it was asked for and returns a scripted error (a real Replayer stub).
type fakeReplayer struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (f *fakeReplayer) Replay(_ context.Context, _ v1.NamespaceName, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	return f.err
}

func newDLQServer(t *testing.T, seed func(deadletter.Store), rp *fakeReplayer) http.Handler {
	t.Helper()
	dlq := dlmemory.New()
	if seed != nil {
		seed(dlq)
	}
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
		DeadLetters: dlq,
		Replayer:    rp,
	})
	require.NoError(t, err)
	return h
}

func seedDeadLetter(t *testing.T, s deadletter.Store, ns, id string) {
	t.Helper()
	require.NoError(t, s.Put(context.Background(), deadletter.DeadLetter{
		ID: id, Namespace: v1.NamespaceName(ns), Sensor: "s", Source: "git", Event: "push",
		Action: "notify", Payload: json.RawMessage(`{"specversion":"1.0"}`), Attempts: 3, Reason: "boom", FailedAt: time.Now().UTC(),
	}))
}

const dlqBase = "/apis/funcd.io/v1alpha1/namespaces/team-a/deadletters"

// scenario: dlq-routes-round-trip — list/describe/replay/discard all work end-to-end for an authorized
// caller over the control-plane routes.
func TestScenarioDeadLetterRoutesRoundTrip(t *testing.T) {
	rp := &fakeReplayer{}
	srv := newDLQServer(t, func(s deadletter.Store) { seedDeadLetter(t, s, "team-a", "01AAA") }, rp)

	// list
	rec := do(t, srv, http.MethodGet, dlqBase, devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Items []deadletter.DeadLetter `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	require.Equal(t, "01AAA", list.Items[0].ID)

	// describe
	rec = do(t, srv, http.MethodGet, dlqBase+"/01AAA", devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var dl deadletter.DeadLetter
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dl))
	require.Equal(t, "notify", dl.Action)

	// replay (imperative)
	rec = do(t, srv, http.MethodPost, dlqBase+"/01AAA/replay", devToken, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, []string{"01AAA"}, rp.ids, "the route delegated to the Replayer")

	// discard (CRUD delete)
	rec = do(t, srv, http.MethodDelete, dlqBase+"/01AAA", devToken, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// scenario: dlq-read-vs-write-authorized-distinctly — a viewer may LIST/describe but not replay/discard
// (read authorizes get/Sensor; replay+discard authorize a write verb).
func TestScenarioDeadLetterReadVsWriteAuthorized(t *testing.T) {
	srv := newDLQServer(t, func(s deadletter.Store) { seedDeadLetter(t, s, "team-a", "01BBB") }, &fakeReplayer{})

	require.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, dlqBase, viewToken, nil).Code, "viewer may list")
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, dlqBase+"/01BBB", viewToken, nil).Code, "viewer may describe")
	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodPost, dlqBase+"/01BBB/replay", viewToken, nil).Code, "viewer may NOT replay")
	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodDelete, dlqBase+"/01BBB", viewToken, nil).Code, "viewer may NOT discard")
}

// scenario: dlq-rbac-namespace-scoped — a caller not bound to the namespace gets 403.
func TestScenarioDeadLetterRBACScoped(t *testing.T) {
	srv := newDLQServer(t, nil, &fakeReplayer{})
	rec := do(t, srv, http.MethodGet, "/apis/funcd.io/v1alpha1/namespaces/team-b/deadletters", devToken, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String()) // dev is bound to team-a
}

// scenario: dlq-unauthenticated-rejected — no token ⇒ 401 before any store access.
func TestScenarioDeadLetterUnauthenticated(t *testing.T) {
	srv := newDLQServer(t, nil, &fakeReplayer{})
	require.Equal(t, http.StatusUnauthorized, do(t, srv, http.MethodGet, dlqBase, "", nil).Code)
}

// the DLQ routes are absent (404) when no store is configured.
func TestDeadLetterRoutesAbsentWhenUnset(t *testing.T) {
	srv := newServer(t) // no DeadLetters dep
	require.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, dlqBase, devToken, nil).Code)
}
