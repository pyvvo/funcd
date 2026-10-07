package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const durationGrammarWords = "units h, m, s and ms"

func durationManifest(name, spec string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: Function\nmetadata:\n  name: " + name +
		"\n  namespace: team-a\n  resourceGroup: rg1\nspec:\n" + spec + "\n"
}

// postFunction sends a raw Function body with spec as given, as a client other than funcdctl would.
func postFunction(t *testing.T, url, name, spec string) (int, string) {
	t.Helper()
	body := `{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"` + name +
		`","namespace":"team-a","resourceGroup":"rg1"},"spec":` + spec + `}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		url+"/apis/funcd.io/v1alpha1/namespaces/team-a/functions", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+devToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func requireNotStored(t *testing.T, c *sdk.Client, name string) {
	t.Helper()
	_, err := c.Get(context.Background(), v1.KindFunction, "team-a", v1.ObjectName(name))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%s must not be stored: %v", name, err)
}

// getSpec reads an object back with funcdctl get -o json and returns its raw spec.
func getSpec(t *testing.T, out *bytes.Buffer, run func(...string) error, kind, name string) map[string]json.RawMessage {
	t.Helper()
	out.Reset()
	require.NoError(t, run("get", kind, name, "-n", "team-a", "-o", "json"))
	var obj struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &obj), out.String())
	return obj.Spec
}

// scenario: string-duration-applies — idleTimeout: 10m applies and reads back as "10m".
func TestScenarioStringDurationApplies(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	var out bytes.Buffer
	run := func(args ...string) error { return execCLI(&out, c, args...) }
	require.NoError(t, run("apply", "-f", writeManifest(t, durationManifest("idle", "  scaling:\n    idleTimeout: 10m"))))
	require.Contains(t, out.String(), "applied Function/idle")
	out.Reset()
	require.NoError(t, run("get", "function", "idle", "-n", "team-a", "-o", "json"))
	require.Contains(t, out.String(), `"idleTimeout": "10m"`)
}

// scenario: integer-duration-refused — timeout: 30 fails in funcdctl before any request, naming the grammar; a raw
// body gets 422 "expected string" at body.spec.timeout for 30 and a 422 naming the grammar for "30".
func TestScenarioIntegerDurationRefused(t *testing.T) {
	t.Parallel()
	c, url := newClientURL(t)
	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, durationManifest("int", "  timeout: 30")))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, durationGrammarWords)
	requireNotStored(t, c, "int")

	code, body := postFunction(t, url, "int", `{"timeout":30}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Contains(t, body, "expected string")
	require.Contains(t, body, "body.spec.timeout")
	code, body = postFunction(t, url, "int", `{"timeout":"30"}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	require.Contains(t, body, durationGrammarWords)
	requireNotStored(t, c, "int")
}

// scenario: sub-millisecond-refused — 500us and 1.5s are refused as an integer is.
func TestScenarioSubMillisecondRefused(t *testing.T) {
	t.Parallel()
	c, url := newClientURL(t)
	for _, v := range []string{"500us", "1.5s"} {
		var out bytes.Buffer
		err := execCLI(&out, c, "apply", "-f", writeManifest(t, durationManifest("sub", "  timeout: "+v)))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", v, err)
		require.ErrorContains(t, err, durationGrammarWords, v)
		code, body := postFunction(t, url, "sub", `{"timeout":"`+v+`"}`)
		require.Equal(t, http.StatusUnprocessableEntity, code, body)
		require.Contains(t, body, durationGrammarWords, v)
		requireNotStored(t, c, "sub")
	}
}

// scenario: shortest-form-output — spec.timeout 90m, 1500ms, 3600s and 90s read back as 1h30m, 1s500ms, 1h and 1m30s
// (a Workflow's: a Function's is capped at 1h); an optional field given 0s is omitted on read.
func TestScenarioShortestFormOutput(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	var out bytes.Buffer
	run := func(args ...string) error { return execCLI(&out, c, args...) }
	for in, want := range map[string]string{"90m": "1h30m", "1500ms": "1s500ms", "3600s": "1h", "90s": "1m30s"} {
		name := "t" + strings.ToLower(in)
		manifest := "apiVersion: funcd.io/v1alpha1\nkind: Workflow\nmetadata:\n  name: " + name +
			"\n  namespace: team-a\n  resourceGroup: rg1\nspec:\n  timeout: " + in +
			"\n  steps:\n    - name: pause\n      builtin:\n        wait: 0s\n"
		require.NoError(t, run("apply", "-f", writeManifest(t, manifest)))
		require.JSONEq(t, `"`+want+`"`, string(getSpec(t, &out, run, "workflow", name)["timeout"]), in)
	}
	require.NoError(t, run("apply", "-f", writeManifest(t, durationManifest("zero", "  timeout: 0s\n  scaling:\n    idleTimeout: 0s"))))
	spec := getSpec(t, &out, run, "function", "zero")
	require.NotContains(t, spec, "timeout")
	require.NotContains(t, string(spec["scaling"]), "idleTimeout")
}

// scenario: bounds-enforced-at-create — spec.timeout: 2h is refused by funcdctl's offline pre-flight naming the field,
// the value and [0s, 1h]; a raw body gets 400 urn:funcd:problem:invalid (a malformed -1s stays 422) with nothing
// stored; store.Create returns fault.Invalid; 1h is stored.
func TestScenarioBoundsEnforcedAtCreate(t *testing.T) {
	t.Parallel()
	c, url := newClientURL(t)
	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, durationManifest("long", "  timeout: 2h")))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "spec.timeout 2h is out of bounds: want [0s, 1h]")
	requireNotStored(t, c, "long")

	code, body := postFunction(t, url, "long", `{"timeout":"2h"}`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "urn:funcd:problem:invalid")
	require.Contains(t, body, "spec.timeout 2h is out of bounds: want [0s, 1h]")
	code, body = postFunction(t, url, "long", `{"timeout":"-1s"}`)
	require.Equal(t, http.StatusUnprocessableEntity, code, body)
	requireNotStored(t, c, "long")

	st := store.New(memory.New())
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "long", "team-a", "rg1"
	fn.Spec.Timeout = v1.Duration(2 * time.Hour)
	_, err = st.Create(context.Background(), fn)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "spec.timeout 2h")

	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, durationManifest("hour", "  timeout: 1h"))))
	require.JSONEq(t, `"1h"`, string(getSpec(t, &out, func(args ...string) error { return execCLI(&out, c, args...) }, "function", "hour")["timeout"]))
}
