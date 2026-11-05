//go:build e2e

package funcd_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// callChunked sends a call whose body has no known length, so the client frames it in chunks.
func (h *shimRig) callChunked(t *testing.T, method, name, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, h.dp+"/function/"+name, io.MultiReader(strings.NewReader(body)))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(got)
}

// A worker reads a call's body whatever its method and framing, so a POST on the kept-alive connection that follows
// answers as it does alone, on the embedded shims, solo and pooled.
func TestWorkerReadsEveryCallsBody(t *testing.T) {
	t.Parallel()
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		echo := "export function handle(ctx, event) { return { data: event.data ?? null }; }\n"
		if l.python != "" {
			echo = "def handle(ctx, event):\n    return {\"data\": event.get(\"data\")}\n"
		}
		h := newShimRig(t, l.python)
		h.deploy(t, "solo", l.fn(echo))
		h.deploy(t, "member", l.fn(echo).pooled("bodies"))
		waitReady(t, h.c, "solo", "member")
		want := reply{status: http.StatusOK, ctype: "application/json", body: `{"data":"next"}`}
		for _, name := range []string{"solo", "member"} {
			for range 3 {
				req, err := http.NewRequest(http.MethodGet, h.dp+"/function/"+name, strings.NewReader(`{"data":"first"}`))
				require.NoError(t, err)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, want, h.post(name, `{"data":"next"}`), "%s: a POST after a GET with a body", name)

				_, _ = h.callChunked(t, http.MethodGet, name, `{"data":"first"}`)
				require.Equal(t, want, h.post(name, `{"data":"next"}`), "%s: a POST after a chunked GET", name)

				status, got := h.callChunked(t, http.MethodPost, name, `{"data":"chunked"}`)
				require.Equal(t, http.StatusOK, status, name)
				require.JSONEq(t, `{"data":"chunked"}`, got, "%s: a chunked POST keeps its data", name)
				require.Equal(t, want, h.post(name, `{"data":"next"}`), "%s: a POST after a chunked POST", name)
			}
		}
	})
}
