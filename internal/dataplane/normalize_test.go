package dataplane

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixedID() string { return "0011223344556677" }

// scenario: no-input-empty-body-invoke — an empty (or whitespace-only) body normalizes to an
// envelope carrying data:null, so a no-input function needs no body.
func TestScenarioNoInputEmptyBodyInvoke(t *testing.T) {
	t.Parallel()
	env, ok := normalizeInvokeBody("default", "fn", nil, fixedID)
	require.True(t, ok)
	var ce cloudEventEnvelope
	require.NoError(t, json.Unmarshal(env, &ce))
	require.Equal(t, "1.0", ce.SpecVersion)
	require.Equal(t, "null", string(ce.Data))

	envWS, okWS := normalizeInvokeBody("default", "fn", []byte("  \n\t"), fixedID)
	require.True(t, okWS)
	require.JSONEq(t, string(env), string(envWS), "whitespace-only body is empty too")
}

// scenario: plain-json-body-wrapped — a bare input value (object or scalar) is wrapped as event data.
func TestScenarioPlainJSONBodyWrapped(t *testing.T) {
	t.Parallel()
	env, ok := normalizeInvokeBody("default", "fn", []byte(`{"x":1}`), fixedID)
	require.True(t, ok)
	var ce cloudEventEnvelope
	require.NoError(t, json.Unmarshal(env, &ce))
	require.Equal(t, "1.0", ce.SpecVersion)
	require.Equal(t, "io.funcd.invoke", ce.Type)
	require.Equal(t, "funcd://default/function/fn", ce.Source)
	require.Equal(t, "0011223344556677", ce.ID)
	require.JSONEq(t, `{"x":1}`, string(ce.Data))

	for _, raw := range []string{`42`, `"s"`, `[1,2]`, `null`, `true`} {
		e, k := normalizeInvokeBody("default", "fn", []byte(raw), fixedID)
		require.True(t, k, raw)
		var c cloudEventEnvelope
		require.NoError(t, json.Unmarshal(e, &c))
		require.JSONEq(t, raw, string(c.Data), "non-object JSON %q wraps as data", raw)
	}
}

// scenario: cloudevent-passthrough — a body already carrying a top-level "data" or "specversion" is
// forwarded byte-for-byte (back-compat with the pre-0134 {"data":…} convention + advanced callers).
func TestScenarioCloudEventPassthrough(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"data":{"a":1}}`,
		`{"data":null}`,
		`{"specversion":"1.0","data":5}`,
		`{"specversion":"1.0"}`,
	} {
		env, ok := normalizeInvokeBody("default", "fn", []byte(raw), fixedID)
		require.True(t, ok, raw)
		require.Equal(t, raw, string(env), "passthrough is byte-for-byte")
	}
}

// scenario: sensor-and-gateway-agree — the wrapped invoke envelope has the shape a producer emits
// (specversion 1.0 + data == the payload), so the two entry paths normalize to the same envelope.
func TestScenarioSensorAndGatewayAgree(t *testing.T) {
	t.Parallel()
	payload := `{"amount":10}`
	env, ok := normalizeInvokeBody("default", "fn", []byte(payload), fixedID)
	require.True(t, ok)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(env, &m))
	require.Equal(t, `"1.0"`, string(m["specversion"]))
	require.JSONEq(t, payload, string(m["data"]))
}

// A body that is not valid JSON is NOT normalized (ok=false); the caller forwards it verbatim for the
// shim to reject with a clean 400 (the shim bug fix), keeping the edge from being a second validator.
func TestNonJSONBodyNotNormalized(t *testing.T) {
	t.Parallel()
	env, ok := normalizeInvokeBody("default", "fn", []byte(`abc{`), fixedID)
	require.False(t, ok)
	require.Nil(t, env)
}
