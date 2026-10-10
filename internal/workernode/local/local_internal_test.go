package local

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// scenario: local-api-body-over-limit (ADR-0148)
// Issue #172: a body over a local-API MaxBytesReader cap is 413 payload-too-large (as on the data plane,
// ADR-0134), not 400 invalid. The cap fires before the resolver, invoker or port is reached.
func TestIssue172_OverCapBodyIs413(t *testing.T) {
	h := NewHandler(Ref{Namespace: "default", Function: "fn"}, nil, nil, nil, capFakeBlob{}, capFakeBlob{}, nil, nil)
	for _, tc := range []struct {
		name, method, path string
		limit              int64
	}{
		{"invoke", http.MethodPost, "/invoke/sink", maxInvokeBytes},
		{"kv put", http.MethodPut, "/kv/b/k", maxKVBytes},
		{"blob put", http.MethodPut, "/blob/b/obj", maxBlobBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, io.LimitReader(zeroReader{}, tc.limit+1)))
			require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
			var p fault.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			require.Equal(t, "urn:funcd:problem:payload-too-large", p.Type)
		})
	}
}
