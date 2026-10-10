package sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// requestLog records each request a server receives as "<method> <path>?<query>".
type requestLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *requestLog) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.seen = append(l.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		l.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (l *requestLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.seen
	l.seen = nil
	return out
}

const fnPath = "/apis/funcd.io/v1alpha1/namespaces/team-a/functions"

// ADR-0220 Decision 6: DryRun puts dryRun=true on the PUT, on the POST it falls back to and on a generateName POST; the
// OpenAPI document is read once; nothing is stored.
func TestDryRunApplySendsTheFlag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var log requestLog
	c := newClientVia(t, log.wrap)

	got, err := c.Apply(ctx, newFunction("f", "h1"), sdk.DryRun())
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("f"), got.GetName())
	require.Empty(t, got.GetObjectMeta().UID, "a dry-run create answers no uid")
	require.Equal(t, []string{"GET /openapi.json?", "PUT " + fnPath + "/f?dryRun=true", "POST " + fnPath + "?dryRun=true"}, log.take())
	_, err = c.Get(ctx, v1.KindFunction, "team-a", "f")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a dry run stores nothing")
	log.take()

	gen := newFunction("", "h1")
	gen.GenerateName = "gen-"
	got, err = c.Apply(ctx, gen, sdk.DryRun())
	require.NoError(t, err)
	require.Contains(t, string(got.GetName()), "gen-")
	require.Equal(t, []string{"POST " + fnPath + "?dryRun=true"}, log.take(), "the document is read once")
	listed, err := c.List(ctx, v1.KindFunction, "team-a")
	require.NoError(t, err)
	require.Empty(t, listed)

	stored, err := c.Apply(ctx, newFunction("g", "h1"))
	require.NoError(t, err)
	got, err = c.Apply(ctx, newFunction("g", "h2"), sdk.DryRun())
	require.NoError(t, err)
	require.Equal(t, "h2", got.(*v1.Function).Spec.Handler)
	require.Equal(t, stored.GetObjectMeta().UID, got.GetObjectMeta().UID)
	cur, err := c.Get(ctx, v1.KindFunction, "team-a", "g")
	require.NoError(t, err)
	require.Equal(t, "h1", cur.(*v1.Function).Spec.Handler)
	require.Equal(t, stored.GetObjectMeta().ResourceVersion, cur.GetObjectMeta().ResourceVersion)
}

// ADR-0220 Decision 6: against a server whose OpenAPI document declares no dryRun, or that serves none, a dry-run
// Apply sends no write and fails.
func TestDryRunUnsupportedServerWritesNothing(t *testing.T) {
	t.Parallel()
	api := controlplane.NewAPI(chi.NewRouter(), controlplane.NewStubHandlers())
	for _, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Put, item.Post} {
			if op != nil {
				op.Parameters = slices.DeleteFunc(op.Parameters, func(p *huma.Param) bool { return p.Name == "dryRun" })
			}
		}
	}
	older, err := json.Marshal(api.OpenAPI())
	require.NoError(t, err)
	for name, doc := range map[string][]byte{"no dryRun": older, "no document": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var writes int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mu.Lock()
					writes++
					mu.Unlock()
				}
				if r.URL.Path != "/openapi.json" || doc == nil {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(doc)
			}))
			t.Cleanup(srv.Close)
			c, err := sdk.New(srv.URL)
			require.NoError(t, err)
			_, err = c.Apply(context.Background(), newFunction("f", "h1"), sdk.DryRun())
			require.EqualError(t, err, "sdk.Apply: the server does not support dryRun")
			mu.Lock()
			defer mu.Unlock()
			require.Zero(t, writes, "no write is sent")
		})
	}
}
