package dataplane_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/testkit/plugintest"
)

func checkedBy(t *testing.T, input, output string) []invoke.Plugin {
	t.Helper()
	return contractcheck.Plugins(plugintest.CompileContract(t, input, output))
}

// contractAnswer decodes ADR-0058's {error, details} answer.
func contractAnswer(t *testing.T, body []byte) (string, []contract.Detail) {
	t.Helper()
	var a struct {
		Error   string            `json:"error"`
		Details []contract.Detail `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &a))
	return a.Error, a.Details
}

// scenario: a-bad-input-never-reaches-the-worker
func TestScenarioABadInputNeverReachesTheWorker(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the worker was called") })
	h, st, scaler := authDoorUpstream(t, nil, nil, creds{}, up, chainWith(checkedBy(t, plugintest.EmailInput, `{}`)...))
	seedFunction(t, st, "api")
	rec := callBody(h, false, `{"email": "a b@example.com"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	msg, details := contractAnswer(t, rec.Body.Bytes())
	require.Equal(t, "event data does not match the input contract", msg)
	require.Len(t, details, 1)
	require.Equal(t, "/email", details[0].Path)
	require.Zero(t, scaler.count(), "a refused call wakes nothing")
}

// scenario: every-path-checks-the-input (the data-plane entry: External and FnToFn)
func TestScenarioEveryPathChecksTheInput(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("the worker was called") })
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(checkedBy(t, plugintest.EmailInput, `{}`)...))
	seedFunction(t, st, "api")
	require.Equal(t, http.StatusUnprocessableEntity, callBody(h, false, `{"email": "a b@example.com"}`).Code)
	require.Equal(t, http.StatusUnprocessableEntity, callBody(h, true, `{"specversion": "1.0", "data": {"email": "a b@example.com"}}`).Code)
}

// scenario: a-bad-result-becomes-500
func TestScenarioABadResultBecomes500(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"message": 42}`) })
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(checkedBy(t, `{}`,
		`{"type": "object", "properties": {"message": {"type": "string"}}, "required": ["message"]}`)...))
	seedFunction(t, st, "api")
	rec := callBody(h, false, `{}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	msg, details := contractAnswer(t, rec.Body.Bytes())
	require.Equal(t, "handler result does not match the output contract", msg)
	require.Equal(t, "/message", details[0].Path)
}

// scenario: a-void-result-is-checked
func TestScenarioAVoidResultIsChecked(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Answer") == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, r.Header.Get("X-Test-Answer"))
	})
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(checkedBy(t, `{}`, `{"type": "null"}`)...))
	seedFunction(t, st, "api")
	require.Equal(t, http.StatusNoContent, call(h, false, nil).Code)
	require.Equal(t, http.StatusInternalServerError, call(h, false, map[string]string{"X-Test-Answer": `{"x": 1}`}).Code)
}

// A 204 is the null result, so an output contract that wants an object refuses it (ADR-0303 Decision 5).
func TestANullResultIsCheckedToo(t *testing.T) {
	t.Parallel()
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h, st, _ := authDoorUpstream(t, warmAPI(), nil, creds{}, up, chainWith(checkedBy(t, `{}`, `{"type": "object"}`)...))
	seedFunction(t, st, "api")
	require.Equal(t, http.StatusInternalServerError, call(h, false, nil).Code)
}
