package controlplane_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const statCause = "stat data/blob/k: permission denied"

// failingReads answers each read of a Function with err.
type failingReads struct {
	store.Store
	err error
}

func (s failingReads) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindFunction {
		return nil, s.err
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

func loggingServer(t *testing.T, st store.Store, logs *bytes.Buffer) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{Store: st, Authorizer: rbac.New(), Credentials: creds,
		Logger: slog.New(slog.NewTextHandler(logs, nil))})
	require.NoError(t, err)
	return h
}

// A 5xx's detail carries the operation and its message; the cause goes to the logger NewServer is given, once.
func TestServerFaultCauseGoesToTheLog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		status int
		typ    string
		detail string
	}{
		{"a fault with a message", fault.Wrapf(errors.New(statCause), fault.Internal, "store.Get", "read team-a/echo"),
			http.StatusInternalServerError, "urn:funcd:problem:internal", "store.Get: read team-a/echo"},
		{"a plain error", errors.New(statCause),
			http.StatusInternalServerError, "urn:funcd:problem:internal", "internal error: the cause is in funcd's log"},
		{"a 503", fault.Wrapf(errors.New(statCause), fault.Unavailable, "quota.Admit", "read the quota"),
			http.StatusServiceUnavailable, "urn:funcd:problem:unavailable", "quota.Admit: read the quota"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			srv := loggingServer(t, failingReads{store.New(memory.New()), tc.err}, &logs)
			rec := do(t, srv, http.MethodGet, fnBase+"/echo", devToken, nil)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			p := problem(t, rec.Body.Bytes())
			require.Equal(t, tc.typ, p.Type)
			require.Equal(t, tc.detail, p.Detail)
			require.Equal(t, 1, strings.Count(logs.String(), statCause), logs.String())
		})
	}
}

// A 4xx is the caller's to correct: it keeps its text, and nothing is left out for the log.
func TestCallerFaultLeavesNothingForTheLog(t *testing.T) {
	t.Parallel()
	cause := errors.New("line 3: unknown field")
	var logs bytes.Buffer
	srv := loggingServer(t, failingReads{store.New(memory.New()), fault.Wrapf(cause, fault.Invalid, "store.Get", "read")}, &logs)
	rec := do(t, srv, http.MethodGet, fnBase+"/echo", devToken, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "store.Get: read: line 3: unknown field", problem(t, rec.Body.Bytes()).Detail)
	require.NotContains(t, logs.String(), cause.Error())
}

// A body huma cannot read answers its fixed text, and the read's error goes to the log.
func TestUnreadableBodyCauseGoesToTheLog(t *testing.T) {
	t.Parallel()
	const reset = "read tcp 10.0.0.1:8080->10.0.0.2:40000: read: connection reset by peer"
	var logs bytes.Buffer
	srv := loggingServer(t, store.New(memory.New()), &logs)
	req := httptest.NewRequest(http.MethodPost, fnBase, iotest.ErrReader(errors.New(reset)))
	req.Header.Set("Authorization", "Bearer "+devToken)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.Equal(t, "cannot read request body", problem(t, rec.Body.Bytes()).Detail)
	require.Equal(t, 1, strings.Count(logs.String(), reset), logs.String())
}
