//go:build !e2e

package funcd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The answers of the described operations (ADR-0329): each test provokes every case that an operation lists, through
// the platform's own servers, so TestMain's comparison finds each listed status got.

const (
	answersAdmin = "answers-admin-token"
	answersDev   = "answers-dev-token"   // a developer of default
	answersOther = "answers-other-token" // a developer of another namespace only
)

// answersPlatform runs an in-memory platform whose credentials are an admin, a developer of default and one of other.
func answersPlatform(t *testing.T, opts ...funcd.Option) *funcd.Platform {
	t.Helper()
	p, _ := stoppablePlatform(t, opts...)
	return p
}

// stoppablePlatform is answersPlatform with the stop that its cleanup runs: it ends the platform's Run and waits for it.
func stoppablePlatform(t *testing.T, opts ...funcd.Option) (*funcd.Platform, func()) {
	t.Helper()
	p, err := funcd.New(append([]funcd.Option{funcd.InMemory(), funcd.WithCredentials(
		funcd.Credential{Token: answersAdmin, Role: "admin"},
		funcd.Credential{Token: answersDev, Role: "developer", Namespaces: []string{"default"}},
		funcd.Credential{Token: answersOther, Role: "developer", Namespaces: []string{"other"}},
	)}, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	stop := sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(stop)
	return p, stop
}

// ask is one request and the answer it gets: its status and a part of its body.
type ask struct {
	what         string // the case it provokes
	method, path string
	token        string
	header       []string // name, value pairs; a JSON body gets Content-Type application/json unless one is given
	body         string
	status       int
	kind         string // the problem type of the answer, checked when it is set
	detail       string
}

// answersClient sends the asks on connections of its own, so no other test's server shuts them.
func answersClient() *http.Client {
	return &http.Client{Transport: &http.Transport{}, Timeout: 30 * time.Second}
}

func (a ask) run(t *testing.T, c *http.Client, base string) {
	t.Helper()
	resp, err := a.do(c, base)
	require.NoError(t, err)
	a.check(t, resp)
}

// do sends a's request with c to base.
func (a ask) do(c *http.Client, base string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), a.method, base+a.path, strings.NewReader(a.body))
	if err != nil {
		return nil, err
	}
	if a.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(a.header); i += 2 {
		req.Header.Set(a.header[i], a.header[i+1])
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	return c.Do(req)
}

// check holds resp to a's status, its problem type when a names one, and its detail.
func (a ask) check(t *testing.T, resp *http.Response) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, a.status, resp.StatusCode, "%s %s, %s: %s", a.method, a.path, a.what, body)
	var problem struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &problem) // a problem's detail, unescaped; another body is matched as it is
	if a.kind != "" {
		require.Equal(t, a.kind, problem.Type, "%s %s, %s: the problem type of %s", a.method, a.path, a.what, body)
	}
	require.Contains(t, string(body)+"\n"+problem.Detail, a.detail, "%s %s, %s", a.method, a.path, a.what)
}

// create creates obj with the admin's token.
func create(t *testing.T, c *http.Client, base string, obj v1.Object) {
	t.Helper()
	kind := obj.GroupVersionKind().Kind
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	path := "/apis/funcd.io/v1alpha1/namespaces/" + string(obj.GetObjectMeta().Namespace) + "/" + plural(kind)
	if kind == v1.KindNamespace {
		path = "/apis/funcd.io/v1alpha1/namespaces"
	}
	ask{what: "the fixture " + string(obj.GetObjectMeta().Name), method: http.MethodPost, path: path,
		token: answersAdmin, body: string(raw), status: http.StatusOK}.run(t, c, base)
}

func plural(k v1.Kind) string {
	switch k {
	case v1.KindKVStore:
		return "kvstores"
	case v1.KindFunction:
		return "functions"
	case v1.KindWorkflow:
		return "workflows"
	case v1.KindPolicy:
		return "policies"
	}
	return strings.ToLower(string(k)) + "s"
}

func kvStore(name v1.ObjectName, tables ...string) *v1.KVStore {
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	for _, tb := range tables {
		ks.Spec.Tables = append(ks.Spec.Tables, v1.KVTable{Name: tb})
	}
	return ks
}

func jsonOf(t *testing.T, obj v1.Object) string {
	t.Helper()
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	return string(raw)
}

// unreclaimedKV is the in-memory KV driver without DropPrefix, so funcd reclaims no dropped table's data and the data
// of a table that a spec dropped stays until the test is done.
type unreclaimedKV struct{ kvstore.KV }

// tooLarge is a body over huma's 1 MiB cap on the control plane.
func tooLarge() string { return `{"metadata":{"name":"` + strings.Repeat("x", 1<<20) + `"}}` }

// Each case of KVStore's six operations, through the control plane; the quota's case has a platform of its own.
func TestAnswersOfKVStoreOperations(t *testing.T) {
	t.Parallel()
	kv := unreclaimedKV{kvmemory.New()}
	p := answersPlatform(t, funcd.WithKVStore(kv))
	c, base := answersClient(), "http://"+p.Addr()
	const stores = "/apis/funcd.io/v1alpha1/namespaces/default/kvstores"

	for _, ks := range []*v1.KVStore{kvStore("s", "t"), kvStore("doomed", "t"), kvStore("bound", "t"),
		kvStore("data", "t1"), kvStore("full", "t"), kvStore("kept", "t")} {
		create(t, c, base, ks)
	}
	binder := function("binder")
	binder.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "b", Store: "bound", Table: "t"}}
	binder.Spec.KVStores = []v1.KVStoreEntry{{Ref: "bound"}}
	create(t, c, base, binder)
	maker := function("maker")
	maker.Spec.KVStores = []v1.KVStoreEntry{{Name: "owned", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}}}
	create(t, c, base, maker)
	create(t, c, base, workflow("w", "kept", "owned"))
	create(t, c, base, workflow("w2"))
	require.Eventually(t, func() bool {
		return statusOf(c, http.MethodGet, base+stores+"/owned", answersDev) == http.StatusOK
	}, 10*time.Second, 20*time.Millisecond, "the Function maker makes the store owned")
	ctx := context.Background()
	require.NoError(t, kv.Put(ctx, "default/data/t2/k", []byte("left by a dropped table t2")))
	require.NoError(t, kv.Put(ctx, "default/full/t/k", []byte("v")))

	s, nameless := jsonOf(t, kvStore("s", "t")), jsonOf(t, kvStore("x", "t"))
	twoQuotas := kvStore("s", "t")
	twoQuotas.Spec.Quota.SizeQuota = v1.SizeQuota{MaxSize: "1Gi", MaxBytes: 1}
	otherNS := kvStore("c2", "t")
	otherNS.Namespace = "other"
	wideKey := strings.Replace(s, `"spec":{`, `"spec":{"quota":{"maxKeyBytes":99999999},`, 1)
	dropBound, addBack := jsonOf(t, kvStore("bound")), jsonOf(t, kvStore("data", "t1", "t2"))
	bad := `{`
	for _, a := range []ask{
		{what: "the list", method: http.MethodGet, path: stores, token: answersDev, status: 200, detail: `"name":"s"`},
		{what: "no credential", method: http.MethodGet, path: stores, status: 401, detail: "missing bearer token"},
		{what: "a credential that funcd does not know", method: http.MethodGet, path: stores, token: "unknown-token",
			status: 401, detail: "invalid credential"},
		{what: "another namespace's developer", method: http.MethodGet, path: stores, token: answersOther, status: 403,
			detail: `developer not scoped to namespace "default"`},
		{what: "a namespace that is no DNS-1123 label", method: http.MethodGet,
			path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/kvstores", token: answersDev, status: 422,
			detail: "path.namespace"},

		{what: "a new store", method: http.MethodPost, path: stores, token: answersDev,
			body: jsonOf(t, kvStore("c1", "t")), status: 200, detail: `"name":"c1"`},
		{what: "both quota forms", method: http.MethodPost, path: stores, token: answersDev,
			body: strings.Replace(jsonOf(t, twoQuotas), `"name":"s"`, `"name":"c3"`, 1), status: 400,
			detail: "set maxSize or maxBytes, not both"},
		{what: "a body in another namespace", method: http.MethodPost, path: stores, token: answersDev,
			body: jsonOf(t, otherNS), status: 400, detail: `body namespace "other" does not match path "default"`},
		{what: "a body that is not JSON", method: http.MethodPost, path: stores, token: answersDev, body: bad,
			status: 400, detail: "unexpected end of JSON input"},
		{what: "no credential", method: http.MethodPost, path: stores, body: s, status: 401,
			detail: "missing bearer token"},
		{what: "another namespace's developer", method: http.MethodPost, path: stores, token: answersOther, body: s,
			status: 403, detail: "denied"},
		{what: "a taken name", method: http.MethodPost, path: stores, token: answersDev, body: s, status: 409,
			detail: "already exists"},
		{what: "a body over 1 MiB", method: http.MethodPost, path: stores, token: answersDev, body: tooLarge(),
			status: 413, detail: "request body is too large limit=1048576 bytes"},
		{what: "a body that is not JSON by its Content-Type", method: http.MethodPost, path: stores,
			token: answersDev, header: []string{"Content-Type", "text/plain"}, body: s, status: 415,
			detail: "unknown content type"},
		{what: "no body", method: http.MethodPost, path: stores, token: answersDev,
			header: []string{"Content-Type", "application/json"}, status: 400, detail: "request body is required"},
		{what: "an unknown query parameter", method: http.MethodPost, path: stores + "?force=true", token: answersDev,
			body: s, status: 422, detail: "unknown query parameter"},
		{what: "a dryRun that is no boolean", method: http.MethodPost, path: stores + "?dryRun=maybe",
			token: answersDev, body: s, status: 422, detail: "invalid boolean"},
		{what: "a field the schema does not know", method: http.MethodPost, path: stores, token: answersDev,
			body: strings.Replace(s, `"spec":{`, `"spec":{"bogus":1,`, 1), status: 422, detail: "unexpected property"},
		{what: "a field of the wrong type", method: http.MethodPost, path: stores, token: answersDev,
			body: strings.Replace(s, `"tables":[{"name":"t"}]`, `"tables":"t"`, 1), status: 422,
			detail: "body.spec.tables"},
		{what: "a namespace that is no DNS-1123 label", method: http.MethodPost,
			path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/kvstores", token: answersDev, body: s, status: 422,
			detail: "path.namespace"},
		{what: "a value past its limit", method: http.MethodPost, path: stores, token: answersDev, body: wideKey,
			status: 422, detail: "body.spec.quota.maxKeyBytes"},

		{what: "a store", method: http.MethodGet, path: stores + "/s", token: answersDev, status: 200,
			detail: `"name":"s"`},
		{what: "no credential", method: http.MethodGet, path: stores + "/s", status: 401,
			detail: "missing bearer token"},
		{what: "another namespace's developer", method: http.MethodGet, path: stores + "/s", token: answersOther,
			status: 403, detail: "denied"},
		{what: "a missing store", method: http.MethodGet, path: stores + "/nope", token: answersDev, status: 404,
			detail: "not found"},
		{what: "a name that is no DNS-1123 label", method: http.MethodGet, path: stores + "/Not_A_Label",
			token: answersDev, status: 422, detail: "path.name"},

		{what: "the same spec again", method: http.MethodPut, path: stores + "/s", token: answersDev, body: s,
			status: 200, detail: `"name":"s"`},
		{what: "a weak If-Match", method: http.MethodPut, path: stores + "/s", token: answersDev,
			header: []string{"If-Match", `W/"1"`}, body: s, status: 400, detail: "is a weak entity-tag"},
		{what: "both quota forms", method: http.MethodPut, path: stores + "/s", token: answersDev,
			body: jsonOf(t, twoQuotas), status: 400, detail: "set maxSize or maxBytes, not both"},
		{what: "a body of another name", method: http.MethodPut, path: stores + "/s", token: answersDev,
			body: nameless, status: 400, detail: `body name "x" does not match path "s"`},
		{what: "a body in another namespace", method: http.MethodPut, path: stores + "/s", token: answersDev,
			body: strings.Replace(jsonOf(t, otherNS), `"name":"c2"`, `"name":"s"`, 1), status: 400,
			detail: `body namespace "other" does not match path "default"`},
		{what: "two versions", method: http.MethodPut, path: stores + "/s", token: answersDev,
			header: []string{"If-Match", `"1"`}, body: strings.Replace(s, `"metadata":{`, `"metadata":{"resourceVersion":"2",`, 1),
			status: 400, detail: "differ; send one version"},
		{what: "a body that is not JSON", method: http.MethodPut, path: stores + "/s", token: answersDev, body: bad,
			status: 400, detail: "unexpected end of JSON input"},
		{what: "no credential", method: http.MethodPut, path: stores + "/s", body: s, status: 401,
			detail: "missing bearer token"},
		{what: "another namespace's developer", method: http.MethodPut, path: stores + "/s", token: answersOther,
			body: s, status: 403, detail: "denied"},
		{what: "a missing store", method: http.MethodPut, path: stores + "/nope", token: answersDev,
			body: strings.Replace(s, `"name":"s"`, `"name":"nope"`, 1), status: 404, detail: "not found"},
		{what: "a stale If-Match", method: http.MethodPut, path: stores + "/s", token: answersDev,
			header: []string{"If-Match", `"1"`}, body: s, status: 409, detail: "has changed since resourceVersion"},
		{what: "a store that a live Function made", method: http.MethodPut, path: stores + "/owned",
			token: answersDev, body: strings.Replace(s, `"name":"s"`, `"name":"owned"`, 1), status: 409,
			detail: "is managed by Function/maker"},
		{what: "a dropped table that a binding names", method: http.MethodPut, path: stores + "/bound",
			token: answersDev, body: dropBound, status: 409, detail: `is still bound by function "binder"`},
		{what: "a table added back over its data", method: http.MethodPut, path: stores + "/data",
			token: answersDev, body: addBack, status: 409, detail: "still holds the data of its removed namesake"},
		{what: "a body over 1 MiB", method: http.MethodPut, path: stores + "/s", token: answersDev, body: tooLarge(),
			status: 413, detail: "request body is too large limit=1048576 bytes"},
		{what: "a body that is not JSON by its Content-Type", method: http.MethodPut, path: stores + "/s",
			token: answersDev, header: []string{"Content-Type", "text/plain"}, body: s, status: 415,
			detail: "unknown content type"},
		{what: "a name that is no DNS-1123 label", method: http.MethodPut, path: stores + "/Not_A_Label",
			token: answersDev, body: s, status: 422, detail: "path.name"},
		{what: "an unknown query parameter", method: http.MethodPut, path: stores + "/s?force=true",
			token: answersDev, body: s, status: 422, detail: "unknown query parameter"},
		{what: "a dryRun that is no boolean", method: http.MethodPut, path: stores + "/s?dryRun=maybe",
			token: answersDev, body: s, status: 422, detail: "invalid boolean"},
		{what: "a value past its limit", method: http.MethodPut, path: stores + "/s", token: answersDev,
			body: wideKey, status: 422, detail: "body.spec.quota.maxKeyBytes"},

		{what: "a store", method: http.MethodDelete, path: stores + "/doomed", token: answersDev, status: 204},
		{what: "a weak If-Match", method: http.MethodDelete, path: stores + "/s", token: answersDev,
			header: []string{"If-Match", `W/"1"`}, status: 400, detail: "is a weak entity-tag"},
		{what: "no credential", method: http.MethodDelete, path: stores + "/s", status: 401,
			detail: "missing bearer token"},
		{what: "another namespace's developer", method: http.MethodDelete, path: stores + "/s", token: answersOther,
			status: 403, detail: "denied"},
		{what: "a missing store", method: http.MethodDelete, path: stores + "/nope", token: answersDev, status: 404,
			detail: "not found"},
		{what: "a stale If-Match", method: http.MethodDelete, path: stores + "/s", token: answersDev,
			header: []string{"If-Match", `"1"`}, status: 409, detail: "has changed since resourceVersion"},
		{what: "a store that a binding names", method: http.MethodDelete, path: stores + "/bound", token: answersDev,
			status: 409, detail: `is bound by function "binder"`},
		{what: "a store that holds keys", method: http.MethodDelete, path: stores + "/full", token: answersDev,
			status: 409, detail: "still holds keys"},
		{what: "a name that is no DNS-1123 label", method: http.MethodDelete, path: stores + "/Not_A_Label",
			token: answersDev, status: 422, detail: "path.name"},
		{what: "an unknown query parameter", method: http.MethodDelete, path: stores + "/s?force=true",
			token: answersDev, status: 422, detail: "unknown query parameter"},

		{what: "a store the Workflow declares", method: http.MethodPost, path: stores + "/kept/handover",
			token: answersDev, body: `{"workflow":"w"}`, status: 200, detail: `"name":"kept"`},
		{what: "a body that is not JSON", method: http.MethodPost, path: stores + "/s/handover", token: answersDev,
			body: bad, status: 400, detail: "unexpected end of JSON input"},
		{what: "no credential", method: http.MethodPost, path: stores + "/s/handover", body: `{"workflow":"w"}`,
			status: 401, detail: "missing bearer token"},
		{what: "another namespace's developer", method: http.MethodPost, path: stores + "/s/handover",
			token: answersOther, body: `{"workflow":"w"}`, status: 403, detail: "denied"},
		{what: "a missing Workflow", method: http.MethodPost, path: stores + "/s/handover", token: answersDev,
			body: `{"workflow":"nope"}`, status: 404, detail: "not found"},
		{what: "a missing store", method: http.MethodPost, path: stores + "/nope/handover", token: answersDev,
			body: `{"workflow":"w"}`, status: 404, detail: "not found"},
		{what: "a Workflow that does not declare the store", method: http.MethodPost, path: stores + "/s/handover",
			token: answersDev, body: `{"workflow":"w2"}`, status: 409, detail: `does not declare kvstore "s"`},
		{what: "a store that a live Function made", method: http.MethodPost, path: stores + "/owned/handover",
			token: answersDev, body: `{"workflow":"w"}`, status: 409, detail: "which still exists"},
		{what: "a body over 1 MiB", method: http.MethodPost, path: stores + "/s/handover", token: answersDev,
			body: tooLarge(), status: 413, detail: "request body is too large limit=1048576 bytes"},
		{what: "a body that is not JSON by its Content-Type", method: http.MethodPost, path: stores + "/s/handover",
			token: answersDev, header: []string{"Content-Type", "text/plain"}, body: `{"workflow":"w"}`, status: 415,
			detail: "unknown content type"},
		{what: "a name that is no DNS-1123 label", method: http.MethodPost, path: stores + "/Not_A_Label/handover",
			token: answersDev, body: `{"workflow":"w"}`, status: 422, detail: "path.name"},
		{what: "an unknown query parameter", method: http.MethodPost, path: stores + "/s/handover?force=true",
			token: answersDev, body: `{"workflow":"w"}`, status: 422, detail: "unknown query parameter"},
		{what: "no workflow", method: http.MethodPost, path: stores + "/s/handover", token: answersDev, body: `{}`,
			status: 422, detail: "expected required property workflow"},
	} {
		a.run(t, c, base)
	}
}

// The quota of KVStores per namespace refuses one more (kvstore.maxStoresPerNamespace).
func TestAnswersOfKVStoreQuota(t *testing.T) {
	t.Parallel()
	p := answersPlatform(t, funcd.WithKVStoreQuota(1))
	c, base := answersClient(), "http://"+p.Addr()
	const stores = "/apis/funcd.io/v1alpha1/namespaces/default/kvstores"
	create(t, c, base, kvStore("first", "t"))
	ask{what: "a store past the quota", method: http.MethodPost, path: stores, token: answersDev,
		body: jsonOf(t, kvStore("second", "t")), status: 400, detail: "already holds the maximum 1 KVStores"}.run(t, c, base)
}

// function is a Function of default whose artifact is a file that no runtime reads in these tests.
func function(name v1.ObjectName) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///nonexistent/"+string(name)+".mjs"
	return fn
}

// workflow is a Workflow of default with one wait step that declares stores, each with a table t.
func workflow(name v1.ObjectName, stores ...v1.ObjectName) *v1.Workflow {
	wf := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	wf.Spec.Steps = []v1.WorkflowStep{{Name: "s", Builtin: &v1.BuiltinStep{Wait: "1s"}}}
	for _, s := range stores {
		wf.Spec.KVStores = append(wf.Spec.KVStores,
			v1.KVStoreEntry{Name: s, KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}})
	}
	return wf
}

// statusOf is the status a request with token gets, 0 when it gets none.
func statusOf(c *http.Client, method, url, token string) int {
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		return 0
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// fakeWorkers is a runtime whose workers are the test's handlers: the workers of a Function listen on its own
// httptest server, which answers funcd's health reads and hands every other request to the Function's handler.
type fakeWorkers struct {
	mu    sync.Mutex
	insts map[runtime.InstanceID]runtime.Instance
	ports map[v1.ObjectName]int
}

func newFakeWorkers() *fakeWorkers {
	return &fakeWorkers{insts: map[runtime.InstanceID]runtime.Instance{}, ports: map[v1.ObjectName]int{}}
}

// serve makes h the handler of name's workers.
func (f *fakeWorkers) serve(t *testing.T, name v1.ObjectName, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/health/") {
			h(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ports[name] = srv.Listener.Addr().(*net.TCPAddr).Port
}

func (f *fakeWorkers) Create(_ context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := runtime.Instance{
		ID:        runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica),
		Namespace: spec.Namespace, Name: spec.Name, OwnerKind: spec.OwnerKind, Revision: spec.Revision,
		Replica: spec.Replica, State: runtime.StateCreated, CreatedAt: time.Now(),
		IP: "127.0.0.1", Port: f.ports[spec.Name], Listened: true,
	}
	f.insts[in.ID] = in
	return in, nil
}

func (f *fakeWorkers) set(id runtime.InstanceID, s runtime.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.insts[id]
	if !ok {
		return fault.NotFoundf("fakeWorkers", "instance %s", id)
	}
	in.State = s
	if s == runtime.StateRunning {
		in.StartedAt = time.Now()
	}
	f.insts[id] = in
	return nil
}

func (f *fakeWorkers) Start(_ context.Context, id runtime.InstanceID) error {
	return f.set(id, runtime.StateRunning)
}

func (f *fakeWorkers) Stop(_ context.Context, id runtime.InstanceID) error {
	_ = f.set(id, runtime.StateStopped)
	return nil
}

func (f *fakeWorkers) Status(_ context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.insts[id]
	if !ok {
		return runtime.Instance{}, fault.NotFoundf("fakeWorkers", "instance %s", id)
	}
	return in, nil
}

func (f *fakeWorkers) Logs(context.Context, runtime.InstanceID) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeWorkers) Exec(context.Context, runtime.InstanceID, []string) error { return nil }

func (f *fakeWorkers) List(_ context.Context, ns v1.NamespaceName) ([]runtime.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runtime.Instance
	for _, in := range f.insts {
		if in.Namespace == ns {
			out = append(out, in)
		}
	}
	return out, nil
}

func (f *fakeWorkers) Remove(_ context.Context, id runtime.InstanceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.insts, id)
	return nil
}

func (f *fakeWorkers) Close() error { return nil }

// brokenNamespaceReads fails each read of the Namespace broken, as a store that cannot read it does.
type brokenNamespaceReads struct{ store.Store }

func (s brokenNamespaceReads) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName,
	name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindNamespace && name == "broken" {
		return nil, errors.New("the read of Namespace broken failed")
	}
	return s.Store.Get(ctx, gvk, ns, name)
}

// artifact writes a handler, and contract when it is not empty, into a directory of its own, and returns the directory.
func artifact(t *testing.T, contract string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.mjs"), []byte("export function handle() {}\n"), 0o600))
	if contract != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"), []byte(contract), 0o600))
	}
	return dir
}

// callable applies Function name, always on, whose artifact holds contract when it is not empty, and waits for it to
// be Ready; it returns the artifact's directory.
func callable(t *testing.T, c *http.Client, base string, name v1.ObjectName, contract string, edit func(*v1.Function)) string {
	t.Helper()
	dir := artifact(t, contract)
	fn := function(name)
	fn.Spec.Image = "file://" + filepath.Join(dir, "handler.mjs")
	fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
	if edit != nil {
		edit(fn)
	}
	create(t, c, base, fn)
	require.Eventually(t, func() bool { return phaseOf(c, base, fn.Namespace, name) == v1.PhaseReady },
		20*time.Second, 20*time.Millisecond, "Function %s is Ready", name)
	return dir
}

// readFunction reads Function ns/name with the admin's token.
func readFunction(c *http.Client, base string, ns v1.NamespaceName, name v1.ObjectName) (v1.Function, error) {
	var fn v1.Function
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/apis/funcd.io/v1alpha1/namespaces/"+string(ns)+"/functions/"+string(name), nil)
	if err != nil {
		return fn, err
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return fn, err
	}
	defer func() { _ = resp.Body.Close() }()
	return fn, json.NewDecoder(resp.Body).Decode(&fn)
}

// phaseOf is the phase of Function ns/name, "" when it cannot be read.
func phaseOf(c *http.Client, base string, ns v1.NamespaceName, name v1.ObjectName) v1.Phase {
	fn, err := readFunction(c, base, ns, name)
	if err != nil {
		return ""
	}
	return fn.Status.Phase
}

// namespace is Namespace name, with no spec.
func namespace(name v1.ObjectName) *v1.Namespace {
	return &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace},
		ObjectMeta: v1.ObjectMeta{Name: name}}
}

// lockedNamespace applies Namespace locked, whose stance asks every caller for a credential.
func lockedNamespace(t *testing.T, c *http.Client, base string) {
	t.Helper()
	ns := namespace("locked")
	ns.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	create(t, c, base, ns)
}

// answerEnv is a platform that rows of a matrix share: its client, its two servers, its workers and its stop.
type answerEnv struct {
	c       *http.Client
	api, gw string
	workers *fakeWorkers
	stop    func()
}

// newAnswerEnv runs a platform like answersPlatform, whose workers are the test's handlers.
func newAnswerEnv(t *testing.T, opts ...funcd.Option) *answerEnv {
	t.Helper()
	w := newFakeWorkers()
	p, stop := stoppablePlatform(t, append([]funcd.Option{funcd.WithRuntime(w), funcd.WithRuntimeShim("node", "shim.mjs")},
		opts...)...)
	c := answersClient()
	// Before the stop: Shutdown waits 5 s for a connection that the client dialed for parallel rows and never used.
	t.Cleanup(c.CloseIdleConnections)
	return &answerEnv{c: c, api: "http://" + p.Addr(), gw: "http://" + p.DataPlaneAddr(), workers: w, stop: stop}
}

// answerRow provokes one case of an operation on its platform: after its setup, it sends its ask, whose status and
// problem type are the case's. A request that the client cannot make, such as one on a connection of its own, has a
// send that makes it and returns its answer.
type answerRow struct {
	op, when string // the operation, and a part of its case's When that names the case alone; "" for its success
	on       *answerEnv
	setup    func(t *testing.T, e *answerEnv)
	send     func(t *testing.T, e *answerEnv, a ask) *http.Response
	ask      ask
}

// caseKey names a case of an operation: its status and its place among that status's cases, -1 for the success.
type caseKey struct {
	op     string
	status int
	index  int
}

// documentedCase is what the document says of a case: when it happens, and the problem type of its answer, "" for a
// success and for a case whose body is another schema.
type documentedCase struct{ when, kind string }

// documentedCases are the cases of every operation of doc.
func documentedCases(t *testing.T, doc *huma.OpenAPI) map[caseKey]documentedCase {
	t.Helper()
	out := map[caseKey]documentedCase{}
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			for code, resp := range op.Responses {
				status, err := strconv.Atoi(code)
				if err != nil {
					continue // the default of an operation not described yet
				}
				if status < http.StatusMultipleChoices {
					out[caseKey{op.OperationID, status, -1}] = documentedCase{}
					continue
				}
				raw, err := json.Marshal(resp.Extensions["x-funcd-cases"])
				require.NoError(t, err)
				var cases []struct{ Type, When string }
				require.NoError(t, json.Unmarshal(raw, &cases))
				for i, c := range cases {
					out[caseKey{op.OperationID, status, i}] = documentedCase{when: c.When, kind: c.Type}
				}
			}
		}
	}
	return out
}

// caseOf is the one case of r's operation that r provokes.
func (r answerRow) caseOf(t *testing.T, cases map[caseKey]documentedCase) caseKey {
	t.Helper()
	var found []caseKey
	for k, c := range cases {
		// a success row names its status when the operation has two (an add-on object's delete: 202 held, 204 gone)
		if k.op == r.op && (r.when == "") == (k.index < 0) && strings.Contains(c.when, r.when) && (r.ask.status == 0 || r.ask.status == k.status) {
			found = append(found, k)
		}
	}
	require.Len(t, found, 1, "%s: one case holds %q", r.op, r.when)
	return found[0]
}

// runMatrix runs each row as a parallel subtest once it holds each row to one case and each case of the rows'
// operations to one row.
func runMatrix(t *testing.T, rows []answerRow) {
	t.Helper()
	cases := documentedCases(t, controlplane.SpecAPI().OpenAPI())
	keys := make([]caseKey, len(rows))
	byCase := map[caseKey]string{}
	for i, r := range rows {
		keys[i] = r.caseOf(t, cases)
		name := r.op + "/" + r.ask.what
		prev, taken := byCase[keys[i]]
		require.False(t, taken, "%s and %s provoke the same case", prev, name)
		byCase[keys[i]] = name
	}
	var missing []string
	for k, c := range cases {
		_, tabled := byCase[k]
		named := slices.ContainsFunc(rows, func(r answerRow) bool { return r.op == k.op })
		if named && !tabled {
			missing = append(missing, fmt.Sprintf("%s %d %q", k.op, k.status, c.when))
		}
	}
	slices.Sort(missing)
	require.Empty(t, missing, "cases with no row")
	for i, r := range rows {
		k := keys[i]
		t.Run(r.op+"/"+r.ask.what, func(t *testing.T) {
			t.Parallel()
			if r.setup != nil {
				r.setup(t, r.on)
			}
			a := r.ask
			a.status, a.kind = k.status, cases[k].kind
			if r.send != nil {
				a.check(t, r.send(t, r.on, a))
				return
			}
			base := r.on.api
			if strings.HasPrefix(a.path, "/function/") {
				base = r.on.gw
			}
			a.run(t, r.on.c, base)
		})
	}
}

// kindFixture is an object of a namespaced kind in default, for the rows of the cases its kind's operations share: json
// is its body, empty for a read-only kind, and broken that body against a rule the schema cannot hold, which detail
// names.
type kindFixture struct {
	kind, kinds, path    string // such as Function, Functions and functions
	name                 v1.ObjectName
	json, broken, detail string
}

// renamed is body with the fixture's name replaced by name.
func (k kindFixture) renamed(body string, name v1.ObjectName) string {
	return strings.Replace(body, `"name":"`+string(k.name)+`"`, `"name":"`+string(name)+`"`, 1)
}

// sharedRows are the rows of the cases that each verb of a described kind shares with every other kind's (ADR-0329
// Decision 1), on e: list and get, then create, replace and delete for a writable kind.
func (k kindFixture) sharedRows(e *answerEnv) []answerRow {
	coll := "/apis/funcd.io/v1alpha1/namespaces/default/" + k.path
	bad := "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/" + k.path
	item, named := coll+"/"+string(k.name), `"name":"`+string(k.name)+`"`
	list, get := "list"+k.kinds, "get"+k.kind
	row := func(op, when string, a ask) answerRow { return answerRow{op: op, when: when, on: e, ask: a} }
	rows := []answerRow{
		row(list, "", ask{what: "the list", method: http.MethodGet, path: coll, token: answersDev, detail: named}),
		row(list, "carries no bearer token", ask{what: "no credential", method: http.MethodGet, path: coll,
			detail: "missing bearer token"}),
		row(list, "do not allow it", ask{what: "another namespace's developer", method: http.MethodGet, path: coll,
			token: answersOther, detail: "denied"}),
		row(list, "in the path is not", ask{what: "a namespace that is no DNS-1123 label", method: http.MethodGet,
			path: bad, token: answersDev, detail: "path.namespace"}),
		row(get, "", ask{what: "an object", method: http.MethodGet, path: item, token: answersDev, detail: named}),
		row(get, "carries no bearer token", ask{what: "no credential", method: http.MethodGet, path: item,
			detail: "missing bearer token"}),
		row(get, "do not allow it", ask{what: "another namespace's developer", method: http.MethodGet, path: item,
			token: answersOther, detail: "denied"}),
		row(get, "of that name exists in the namespace", ask{what: "a missing object", method: http.MethodGet,
			path: coll + "/nope", token: answersDev, detail: "not found"}),
		row(get, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodGet,
			path: coll + "/Not_A_Label", token: answersDev, detail: "path.name"}),
	}
	if k.json == "" {
		return rows
	}
	s, plain := k.json, []string{"Content-Type", "text/plain"}
	otherNS := strings.Replace(s, `"namespace":"default"`, `"namespace":"other"`, 1)
	unknown := `{"bogus":1,` + strings.TrimPrefix(s, "{")
	create, replace, remove := "create"+k.kind, "replace"+k.kind, "delete"+k.kind
	doomed := k.name + "-doomed"
	return append(rows,
		row(create, "", ask{what: "a new object", method: http.MethodPost, path: coll, token: answersDev,
			body: k.renamed(s, k.name+"-new"), detail: `"name":"` + string(k.name) + `-new"`}),
		row(create, "carries no bearer token", ask{what: "no credential", method: http.MethodPost, path: coll, body: s,
			detail: "missing bearer token"}),
		row(create, "do not allow it", ask{what: "another namespace's developer", method: http.MethodPost, path: coll,
			token: answersOther, body: s, detail: "denied"}),
		row(create, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodPost,
			path: coll + "?force=true", token: answersDev, body: s, detail: "unknown query parameter"}),
		row(create, "The dryRun parameter", ask{what: "a dryRun that is no boolean", method: http.MethodPost,
			path: coll + "?dryRun=maybe", token: answersDev, body: s, detail: "invalid boolean"}),
		row(create, "breaks a rule that the schema cannot hold", ask{what: "a broken rule", method: http.MethodPost,
			path: coll, token: answersDev, body: k.renamed(k.broken, k.name+"-broken"), detail: k.detail}),
		row(create, "already exists", ask{what: "a taken name", method: http.MethodPost, path: coll, token: answersDev,
			body: s, detail: "already exists"}),
		row(create, "in the path is not", ask{what: "a namespace that is no DNS-1123 label", method: http.MethodPost,
			path: bad, token: answersDev, body: s, detail: "path.namespace"}),
		row(create, "is not the namespace of the path", ask{what: "a body in another namespace",
			method: http.MethodPost, path: coll, token: answersDev, body: otherNS,
			detail: `body namespace "other" does not match path "default"`}),
		row(create, "The body is missing or is not JSON", ask{what: "a body that is not JSON", method: http.MethodPost,
			path: coll, token: answersDev, body: "{", detail: "unexpected end of JSON input"}),
		row(create, "larger than 1 MiB", ask{what: "a body over 1 MiB", method: http.MethodPost, path: coll,
			token: answersDev, body: tooLarge(), detail: "request body is too large limit=1048576 bytes"}),
		row(create, "The Content-Type of the body", ask{what: "a body that is not JSON by its Content-Type",
			method: http.MethodPost, path: coll, token: answersDev, header: plain, body: s, detail: "unknown content type"}),
		row(create, "The body breaks the schema", ask{what: "a field the schema does not know", method: http.MethodPost,
			path: coll, token: answersDev, body: unknown, detail: "unexpected property"}),

		row(replace, "", ask{what: "the same object again", method: http.MethodPut, path: item, token: answersDev, body: s,
			detail: named}),
		row(replace, "carries no bearer token", ask{what: "no credential", method: http.MethodPut, path: item, body: s,
			detail: "missing bearer token"}),
		row(replace, "do not allow it", ask{what: "another namespace's developer", method: http.MethodPut, path: item,
			token: answersOther, body: s, detail: "denied"}),
		row(replace, "of that name exists in the namespace", ask{what: "a missing object", method: http.MethodPut,
			path: coll + "/nope", token: answersDev, body: k.renamed(s, "nope"), detail: "not found"}),
		row(replace, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodPut,
			path: coll + "/Not_A_Label", token: answersDev, body: s, detail: "path.name"}),
		row(replace, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodPut,
			path: item + "?force=true", token: answersDev, body: s, detail: "unknown query parameter"}),
		row(replace, "The dryRun parameter", ask{what: "a dryRun that is no boolean", method: http.MethodPut,
			path: item + "?dryRun=maybe", token: answersDev, body: s, detail: "invalid boolean"}),
		row(replace, "The If-Match header is not one strong", ask{what: "a weak If-Match", method: http.MethodPut,
			path: item, token: answersDev, header: []string{"If-Match", `W/"1"`}, body: s, detail: "is a weak entity-tag"}),
		row(replace, "breaks a rule that the schema cannot hold", ask{what: "a broken rule", method: http.MethodPut,
			path: item, token: answersDev, body: k.broken, detail: k.detail}),
		row(replace, "has changed since", ask{what: "a stale If-Match", method: http.MethodPut, path: item,
			token: answersDev, header: []string{"If-Match", `"1"`}, body: s, detail: "has changed since resourceVersion"}),
		row(replace, "is not the path's", ask{what: "a body of another name", method: http.MethodPut, path: item,
			token: answersDev, body: k.renamed(s, "x"), detail: `body name "x" does not match path`}),
		row(replace, "The body is missing or is not JSON", ask{what: "a body that is not JSON", method: http.MethodPut,
			path: item, token: answersDev, body: "{", detail: "unexpected end of JSON input"}),
		row(replace, "larger than 1 MiB", ask{what: "a body over 1 MiB", method: http.MethodPut, path: item,
			token: answersDev, body: tooLarge(), detail: "request body is too large limit=1048576 bytes"}),
		row(replace, "The Content-Type of the body", ask{what: "a body that is not JSON by its Content-Type",
			method: http.MethodPut, path: item, token: answersDev, header: plain, body: s, detail: "unknown content type"}),
		row(replace, "The body breaks the schema", ask{what: "a field the schema does not know", method: http.MethodPut,
			path: item, token: answersDev, body: unknown, detail: "unexpected property"}),

		answerRow{op: remove, on: e, setup: func(t *testing.T, e *answerEnv) {
			ask{what: "the fixture " + string(doomed), method: http.MethodPost, path: coll, token: answersAdmin,
				body: k.renamed(s, doomed), status: http.StatusOK}.run(t, e.c, e.api)
		}, ask: ask{what: "an object", method: http.MethodDelete, path: coll + "/" + string(doomed), token: answersDev}},
		row(remove, "carries no bearer token", ask{what: "no credential", method: http.MethodDelete, path: item,
			detail: "missing bearer token"}),
		row(remove, "do not allow it", ask{what: "another namespace's developer", method: http.MethodDelete, path: item,
			token: answersOther, detail: "denied"}),
		row(remove, "of that name exists in the namespace", ask{what: "a missing object", method: http.MethodDelete,
			path: coll + "/nope", token: answersDev, detail: "not found"}),
		row(remove, "in the path is not", ask{what: "a name that is no DNS-1123 label", method: http.MethodDelete,
			path: coll + "/Not_A_Label", token: answersDev, detail: "path.name"}),
		row(remove, "The query holds a parameter", ask{what: "an unknown query parameter", method: http.MethodDelete,
			path: item + "?force=true", token: answersDev, detail: "unknown query parameter"}),
		row(remove, "The If-Match header is not one strong", ask{what: "a weak If-Match", method: http.MethodDelete,
			path: item, token: answersDev, header: []string{"If-Match", `W/"1"`}, detail: "is a weak entity-tag"}),
		row(remove, "has changed since", ask{what: "a stale If-Match", method: http.MethodDelete, path: item,
			token: answersDev, header: []string{"If-Match", `"1"`}, detail: "resourceVersion"}),
	)
}

// invocation is an Invocation record of default.
func invocation(name v1.ObjectName) *v1.Invocation {
	return &v1.Invocation{TypeMeta: v1.TypeMeta{APIVersion: v1.KindInvocation.GVK().APIVersion(), Kind: v1.KindInvocation},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
}

// linking is Function name, whose one link targets target.
func linking(name, target v1.ObjectName) *v1.Function {
	fn := function(name)
	fn.Spec.Bindings.Links = []v1.FunctionLink{{Alias: "l", Target: target}}
	return fn
}

// withVolumes is fn with a local volume of each size: state, then extra-1, extra-2 and so on.
func withVolumes(fn *v1.Function, sizes ...int64) *v1.Function {
	fn.Spec.Volumes = nil
	for i, size := range sizes {
		name := v1.ObjectName("state")
		if i > 0 {
			name = v1.ObjectName(fmt.Sprintf("extra-%d", i))
		}
		fn.Spec.Volumes = append(fn.Spec.Volumes, v1.VolumeEntry{Name: name, Type: v1.VolumeLocal, SizeBytes: size})
	}
	return fn
}

// inNamespace is fn in namespace ns.
func inNamespace(ns v1.NamespaceName, fn *v1.Function) *v1.Function {
	fn.Namespace = ns
	return fn
}

// stopOnWake stops e once its workers hold one of Function name, so the stop releases the call that woke it; it stops
// e after 20 s at the latest.
func stopOnWake(e *answerEnv, name v1.ObjectName) {
	defer e.stop()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		workers, _ := e.workers.List(context.Background(), "default")
		if slices.ContainsFunc(workers, func(in runtime.Instance) bool { return in.Name == name }) {
			return
		}
	}
}

// stalledBody sends a's call on a connection of its own, with a body that stops after its first byte, and returns the
// gateway's answer once its read timeout ends the wait for the rest.
func stalledBody(t *testing.T, e *answerEnv, a ask) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.gw, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = io.WriteString(conn, a.method+" "+a.path+" HTTP/1.1\r\nHost: funcd\r\nContent-Length: 100\r\n\r\n{")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	return resp
}

// The answers of the Function feature's operations, one row per case of each: Function, Revision, Invocation and the
// call of a Function.
func TestFunctionAnswersMatrix(t *testing.T) {
	t.Parallel()
	hd, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	sh := newAnswerEnv(t, funcd.WithHold(hd), asCluster(t))
	placeOnOfflineNode(t, sh)
	sh.workers.serve(t, "echo", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	callable(t, sh.c, sh.api, "echo", "", nil)
	for _, obj := range []v1.Object{function("plain"), function("target"), linking("linker", "target"),
		invocation("inv"), namespace("held"), namespace("loading"), inNamespace("held", function("api")),
		inNamespace("loading", function("api"))} {
		create(t, sh.c, sh.api, obj)
	}
	since := v1.NewTimestamp(time.Now())
	require.NoError(t, hd.HoldNamespace("held", hold.Marker{Reason: hold.ReasonData, Since: since}))
	require.NoError(t, hd.HoldNamespace("loading", hold.Marker{Reason: hold.ReasonRestore, Since: since, Restore: "nightly"}))
	echo, err := readFunction(sh.c, sh.api, "default", "echo")
	require.NoError(t, err)
	require.NotEmpty(t, echo.Status.CurrentRevision)

	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	vol := newAnswerEnv(t, funcd.WithFunctionVolumes(volume.Options{Root: root, CapacityBytes: 64 << 20}, true))
	tight := namespace("tight")
	tight.Spec.VolumeBudget = "1Mi"
	create(t, vol.c, vol.api, tight)
	slow := newAnswerEnv(t, funcd.WithPacing(funcd.Pacing{ActivationTimeout: 500 * time.Millisecond}))
	stopping := newAnswerEnv(t)
	lockedNamespace(t, sh.c, sh.api)

	calls := newAnswerEnv(t, funcd.WithStore(brokenNamespaceReads{store.New(memory.New())}), funcd.WithEdgeAuth(),
		funcd.WithGatewayReadTimeoutForTest(300*time.Millisecond))
	lockedNamespace(t, calls.c, calls.api)
	calls.workers.serve(t, "echo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	calls.workers.serve(t, "strict", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	calls.workers.serve(t, "crash", func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	calls.workers.serve(t, "slow", func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // a read body lets the server see the cut connection
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	calls.workers.serve(t, "unread", func(http.ResponseWriter, *http.Request) {})
	callable(t, calls.c, calls.api, "echo", "", nil)
	callable(t, calls.c, calls.api, "strict",
		`{"input": {"type": "object", "required": ["a"]}, "output": {"type": "object", "required": ["b"]}}`, nil)
	callable(t, calls.c, calls.api, "crash", "", nil)
	callable(t, calls.c, calls.api, "slow", "", func(fn *v1.Function) { fn.Spec.Timeout = v1.Duration(time.Second) })
	contract := filepath.Join(callable(t, calls.c, calls.api, "unread", `{"input": {"type": "object"}, "output": {}}`, nil),
		"__funcd_contract.json")
	require.NoError(t, os.Remove(contract))
	require.NoError(t, os.Mkdir(contract, 0o700))
	rate := newAnswerEnv(t, funcd.WithLimits(limit.Config{RatePerMin: 1, Burst: 1}))
	inFlight := newAnswerEnv(t, funcd.WithLimits(limit.Config{MaxInFlight: 1}))
	holding, release := make(chan struct{}, 1), make(chan struct{})
	inFlight.workers.serve(t, "held", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case holding <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	callable(t, inFlight.c, inFlight.api, "held", "", nil)
	// pastInFlight sends a's call while a call to held, which its worker keeps until the row ends, takes the gateway's
	// one in-flight slot, and returns a's answer.
	pastInFlight := func(t *testing.T, e *answerEnv, a ask) *http.Response {
		t.Helper()
		t.Cleanup(func() { close(release) })
		go func() {
			if resp, err := (ask{method: http.MethodPost, path: "/function/held", body: `{}`}).do(e.c, e.gw); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		select {
		case <-holding:
		case <-time.After(10 * time.Second):
			t.Fatal("the call to held did not reach its worker")
		}
		resp, err := a.do(e.c, e.gw)
		require.NoError(t, err)
		return resp
	}

	const fns = "/apis/funcd.io/v1alpha1/namespaces/default/functions"
	const vols = "/apis/funcd.io/v1alpha1/namespaces/default/volumes"
	broken := function("plain")
	broken.Spec.Protocol, broken.Spec.Runtime = v1.ProtocolHTTP, "python314"
	unbound := func(name v1.ObjectName) string {
		fn := function(name)
		fn.Spec.Bindings.KV = []v1.FunctionKV{{Alias: "b", Store: "ghost", Table: "t"}}
		return jsonOf(t, fn)
	}
	// ready applies Function name of e with a local volume of 1 MiB, its workers the test's, and waits for it to be
	// Ready, so its volume exists.
	ready := func(t *testing.T, e *answerEnv, name v1.ObjectName) {
		e.workers.serve(t, name, func(http.ResponseWriter, *http.Request) {})
		callable(t, e.c, e.api, name, "", func(fn *v1.Function) { withVolumes(fn, 1<<20) })
	}
	gone := func(t *testing.T, e *answerEnv, name v1.ObjectName) {
		ask{what: "the delete of " + string(name), method: http.MethodDelete, path: fns + "/" + string(name),
			token: answersAdmin, status: http.StatusNoContent}.run(t, e.c, e.api)
	}
	applied := func(fn *v1.Function) func(*testing.T, *answerEnv) {
		return func(t *testing.T, e *answerEnv) { create(t, e.c, e.api, fn) }
	}
	inv := kindFixture{kind: "Invocation", kinds: "Invocations", path: "invocations", name: "inv",
		json: jsonOf(t, invocation("inv")), detail: `resourceGroup is required for kind "Invocation"`}
	inv.broken = strings.Replace(inv.json, `,"resourceGroup":"rg1"`, "", 1)

	rows := slices.Concat(
		kindFixture{kind: "Function", kinds: "Functions", path: "functions", name: "plain",
			json: jsonOf(t, function("plain")), broken: jsonOf(t, broken),
			detail: `spec.protocol http needs a node runtime, not "python314"`}.sharedRows(sh),
		kindFixture{kind: "Revision", kinds: "Revisions", path: "revisions",
			name: v1.ObjectName(echo.Status.CurrentRevision)}.sharedRows(sh),
		inv.sharedRows(sh),
		[]answerRow{
			{op: "createFunction", when: "names a Function that does not exist", on: sh, ask: ask{
				what: "a link to a missing Function", method: http.MethodPost, path: fns, token: answersDev,
				body:   jsonOf(t, linking("lonely", "ghost")),
				detail: `spec.bindings.links[l].target "ghost" does not exist in namespace "default"`}},
			{op: "createFunction", when: "would close a cycle", on: sh, ask: ask{what: "a link to itself",
				method: http.MethodPost, path: fns, token: answersDev, body: jsonOf(t, linking("selfish", "selfish")),
				detail: "would create a dependency cycle (selfish → selfish)"}},
			{op: "createFunction", when: "does not declare", on: sh, ask: ask{what: "a binding to an undeclared store",
				method: http.MethodPost, path: fns, token: answersDev, body: unbound("unbound"),
				detail: `KVStore "ghost" is not declared in spec.kv`}},
			{op: "createFunction", when: "volumes are off", on: sh, ask: ask{what: "a volume while volumes are off",
				method: http.MethodPost, path: fns, token: answersDev, body: jsonOf(t, withVolumes(function("vol"), 1<<20)),
				detail: "VolumesOff"}},
			{op: "createFunction", when: "differs from the size it was applied with", on: vol,
				setup: func(t *testing.T, e *answerEnv) { ready(t, e, "resized"); gone(t, e, "resized") },
				ask: ask{what: "another size for a retained volume", method: http.MethodPost, path: fns, token: answersDev,
					body: jsonOf(t, withVolumes(function("resized"), 2<<20)), detail: "a volume is not resized"}},
			{op: "createFunction", when: "does not fit in the capacity", on: vol, ask: ask{what: "a volume over the capacity",
				method: http.MethodPost, path: fns, token: answersDev,
				body: jsonOf(t, withVolumes(function("huge"), 128<<20)), detail: "volumes.capacityBytes"}},
			{op: "createFunction", when: "spec.volumeBudget", on: vol, ask: ask{what: "a volume over the budget",
				method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/namespaces/tight/functions", token: answersAdmin,
				body: jsonOf(t, inNamespace("tight", withVolumes(function("spender"), 2<<20))), detail: "volumeBudgetBytes"}},

			{op: "replaceFunction", when: "names a Function that does not exist", on: sh, ask: ask{
				what: "a link to a missing Function", method: http.MethodPut, path: fns + "/plain", token: answersDev,
				body: jsonOf(t, linking("plain", "ghost")), detail: `target "ghost" does not exist in namespace "default"`}},
			{op: "replaceFunction", when: "would close a cycle", on: sh, ask: ask{what: "a link that closes a cycle",
				method: http.MethodPut, path: fns + "/target", token: answersDev, body: jsonOf(t, linking("target", "linker")),
				detail: "would create a dependency cycle"}},
			{op: "replaceFunction", when: "does not declare", on: sh, ask: ask{what: "a binding to an undeclared store",
				method: http.MethodPut, path: fns + "/plain", token: answersDev, body: unbound("plain"),
				detail: `KVStore "ghost" is not declared in spec.kv`}},
			{op: "replaceFunction", when: "volumes are off", on: sh, ask: ask{what: "a volume while volumes are off",
				method: http.MethodPut, path: fns + "/plain", token: answersDev,
				body: jsonOf(t, withVolumes(function("plain"), 1<<20)), detail: "VolumesOff"}},
			{op: "replaceFunction", when: "differs from the size it was applied with", on: vol,
				setup: applied(withVolumes(function("grower"), 1<<20)),
				ask: ask{what: "another size for its volume", method: http.MethodPut, path: fns + "/grower", token: answersDev,
					body: jsonOf(t, withVolumes(function("grower"), 2<<20)), detail: "a volume is not resized"}},
			{op: "replaceFunction", when: "does not fit in the capacity", on: vol,
				setup: applied(withVolumes(function("widener"), 1<<20)),
				ask: ask{what: "a volume over the capacity", method: http.MethodPut, path: fns + "/widener",
					token: answersDev, body: jsonOf(t, withVolumes(function("widener"), 1<<20, 128<<20)),
					detail: "volumes.capacityBytes"}},
			{op: "replaceFunction", when: "spec.volumeBudget", on: vol,
				setup: applied(inNamespace("tight", withVolumes(function("saver"), 512<<10))),
				ask: ask{what: "a volume over the budget", method: http.MethodPut,
					path: "/apis/funcd.io/v1alpha1/namespaces/tight/functions/saver", token: answersAdmin,
					body: jsonOf(t, inNamespace("tight", withVolumes(function("saver"), 512<<10, 1<<20))), detail: "volumeBudgetBytes"}},

			{op: "deleteFunction", when: "links names the Function", on: sh, ask: ask{what: "a link's target",
				method: http.MethodDelete, path: fns + "/target", token: answersDev, detail: `is linked by "linker"`}},

			{op: "getFunctionLogs", on: sh, ask: ask{what: "the logs", method: http.MethodGet, path: fns + "/echo/logs",
				token: answersDev, detail: `"items"`}},
			{op: "getFunctionLogs", when: "carries no bearer token", on: sh, ask: ask{what: "no credential",
				method: http.MethodGet, path: fns + "/echo/logs", detail: "missing bearer token"}},
			{op: "getFunctionLogs", when: "do not allow it", on: sh, ask: ask{what: "another namespace's developer",
				method: http.MethodGet, path: fns + "/echo/logs", token: answersOther, detail: `read logs in "default" denied`}},
			{op: "getFunctionLogs", when: "The since parameter", on: sh, ask: ask{what: "a since of neither form",
				method: http.MethodGet, path: fns + "/echo/logs?since=yesterday", token: answersDev,
				detail: `since "yesterday" is not`}},
			{op: "getFunctionLogs", when: "The severity parameter", on: sh, ask: ask{what: "an unknown severity",
				method: http.MethodGet, path: fns + "/echo/logs?severity=loud", token: answersDev,
				detail: `unknown severity "loud"`}},
			{op: "getFunctionLogs", when: "in the path is not", on: sh, ask: ask{what: "a name that is no DNS-1123 label",
				method: http.MethodGet, path: fns + "/Not_A_Label/logs", token: answersDev, detail: "path.name"}},
			{op: "getFunctionLogs", when: "The limit parameter", on: sh, ask: ask{what: "a limit that is no integer",
				method: http.MethodGet, path: fns + "/echo/logs?limit=many", token: answersDev, detail: "query.limit"}},

			{op: "deleteVolume", on: vol, setup: func(t *testing.T, e *answerEnv) { ready(t, e, "gone"); gone(t, e, "gone") },
				ask: ask{what: "a retained volume", method: http.MethodDelete, path: vols + "/gone/state", token: answersDev}},
			{op: "deleteVolume", when: "carries no bearer token", on: vol, ask: ask{what: "no credential",
				method: http.MethodDelete, path: vols + "/nobody/state", detail: "missing bearer token"}},
			{op: "deleteVolume", when: "do not allow it", on: vol, ask: ask{what: "another namespace's developer",
				method: http.MethodDelete, path: vols + "/nobody/state", token: answersOther,
				detail: `volume delete in "default" denied`}},
			{op: "deleteVolume", when: "No volume of that name", on: vol, ask: ask{what: "a missing volume",
				method: http.MethodDelete, path: vols + "/nobody/state", token: answersDev, detail: "does not exist"}},
			{op: "deleteVolume", when: "in the path is not", on: vol, ask: ask{what: "a Function name that is no DNS-1123 label",
				method: http.MethodDelete, path: vols + "/Not_A_Label/state", token: answersDev, detail: "path.function"}},
			{op: "deleteVolume", when: "The query holds a parameter", on: vol, ask: ask{what: "an unknown query parameter",
				method: http.MethodDelete, path: vols + "/nobody/state?force=true", token: answersDev,
				detail: "unknown query parameter"}},
			{op: "deleteVolume", when: "still exists and declares the volume", on: vol,
				setup: func(t *testing.T, e *answerEnv) { ready(t, e, "holder") },
				ask: ask{what: "a volume that its Function holds", method: http.MethodDelete, path: vols + "/holder/state",
					token: answersDev, detail: "is held by Function default/holder"}},

			{op: "invokeFunction", on: calls, ask: ask{what: "the handler's result", method: http.MethodPost,
				path: "/function/echo", body: `{}`, detail: `{"ok":true}`}},
			{op: "invokeFunction", when: "did not arrive within the gateway's read timeout", on: calls, send: stalledBody,
				ask: ask{what: "a body that stops arriving", method: http.MethodPost, path: "/function/echo",
					detail: "could not read request body"}},
			{op: "invokeFunction", when: "and the call carries none", on: calls, ask: ask{
				what: "no credential where the stance asks for one", method: http.MethodPost, path: "/function/ghost",
				header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, detail: "missing bearer token"}},
			{op: "invokeFunction", when: "while server.auth.edge is off", on: sh, ask: ask{
				what: "a stance that server.auth.edge off cannot check", method: http.MethodPost, path: "/function/ghost",
				header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, detail: "no authenticator is configured"}},
			{op: "invokeFunction", when: "do not allow it to call Functions", on: calls, ask: ask{
				what: "a credential of another namespace", method: http.MethodPost, path: "/function/ghost",
				token: answersOther, header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`,
				detail: "not authorized to invoke"}},
			{op: "invokeFunction", when: "answers at its own address", on: calls, ask: ask{what: "a missing Function",
				method: http.MethodPost, path: "/function/nope", body: `{}`,
				detail: "function default/nope is not exposed by name"}},
			{op: "invokeFunction", when: "larger than 1 MiB", on: calls, ask: ask{what: "a body over 1 MiB",
				method: http.MethodPost, path: "/function/echo", body: `"` + strings.Repeat("x", 1<<20) + `"`,
				detail: "request body exceeds 1048576 bytes"}},
			{op: "invokeFunction", when: "does not match the Function's input contract", on: calls, ask: ask{
				what: "data that breaks the input contract", method: http.MethodPost, path: "/function/strict", body: `{}`,
				detail: "event data does not match the input contract"}},
			{op: "invokeFunction", when: "over the rate that server.limits sets", on: rate,
				setup: func(t *testing.T, e *answerEnv) {
					ask{what: "the call that takes the only token", method: http.MethodPost, path: "/function/ghost",
						body: `{}`, status: http.StatusNotFound}.run(t, e.c, e.gw)
				},
				ask: ask{what: "a call over the rate", method: http.MethodPost, path: "/function/ghost", body: `{}`,
					detail: "rate limit exceeded"}},
			{op: "invokeFunction", when: "does not match the Function's output contract", on: calls, ask: ask{
				what: "a result that breaks the output contract", method: http.MethodPost, path: "/function/strict",
				body: `{"a": 1}`, detail: "handler result does not match the output contract"}},
			{op: "invokeFunction", when: "server.limits.maxInFlight", on: inFlight, send: pastInFlight, ask: ask{
				what: "a call past the in-flight ceiling", method: http.MethodPost, path: "/function/ghost", body: `{}`,
				detail: "in-flight concurrency ceiling reached"}},
			{op: "invokeFunction", when: "The worker failed during the call", on: calls, ask: ask{
				what: "a worker that fails during the call", method: http.MethodPost, path: "/function/crash", body: `{}`,
				detail: "upstream call failed"}},
			{op: "invokeFunction", when: "could not read the namespace's settings", on: calls, ask: ask{
				what: "a namespace whose settings cannot be read", method: http.MethodPost, path: "/function/ghost",
				header: []string{"X-Funcd-Namespace", "broken"}, body: `{}`, detail: "edge settings unavailable"}},
			{op: "invokeFunction", when: "cannot read the Function's contract", on: calls, ask: ask{
				what: "a contract that cannot be read", method: http.MethodPost, path: "/function/unread", body: `{}`,
				detail: "the contract of default/unread cannot be read"}},
			{op: "invokeFunction", when: "within the call's deadline", on: calls, ask: ask{what: "a worker past the deadline",
				method: http.MethodPost, path: "/function/slow", body: `{}`,
				detail: "did not start its response within 1s (spec.timeout)"}},
			{op: "invokeFunction", when: "held for its data", on: sh, ask: ask{what: "a namespace held for its data",
				method: http.MethodPost, path: "/function/api", header: []string{"X-Funcd-Namespace", "held"}, body: `{}`,
				detail: "namespace held is held for its data since"}},
			{op: "invokeFunction", when: "held while a Restore loads it", on: sh, ask: ask{
				what: "a namespace that a Restore loads", method: http.MethodPost, path: "/function/api",
				header: []string{"X-Funcd-Namespace", "loading"}, body: `{}`,
				detail: "namespace loading is held while Restore nightly loads it since"}},
			{op: "invokeFunction", when: "invoke.activationTimeout", on: slow,
				setup: func(t *testing.T, e *answerEnv) {
					fn := function("never")
					fn.Spec.Image = "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
					create(t, e.c, e.api, fn)
				},
				ask: ask{what: "no worker ready in time", method: http.MethodPost, path: "/function/never", body: `{}`,
					detail: "function default/never did not become ready within 500ms"}},
			{op: "invokeFunction", when: "is Failed", on: sh,
				setup: func(t *testing.T, e *answerEnv) {
					fn := function("broken") // no handler: a pass of a Function that wants a worker fails it ShapeInvalid
					fn.Spec.Image = "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
					fn.Spec.Handler, fn.Spec.Replicas = "", 1
					create(t, e.c, e.api, fn)
					require.Eventually(t, func() bool { return phaseOf(e.c, e.api, "default", "broken") == v1.PhaseFailed },
						20*time.Second, 20*time.Millisecond, "Function broken is Failed")
				},
				ask: ask{what: "a Failed Function", method: http.MethodPost, path: "/function/broken", body: `{}`,
					detail: "function default/broken is Failed (ShapeInvalid)"}},
			{op: "invokeFunction", when: "funcd is stopping", on: stopping,
				setup: func(t *testing.T, e *answerEnv) {
					fn := function("cold")
					fn.Spec.Image = "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
					create(t, e.c, e.api, fn)
					go stopOnWake(e, "cold")
				},
				ask: ask{what: "a call waiting for a worker at a stop", method: http.MethodPost, path: "/function/cold",
					body: `{}`, detail: "activator stopped before default/cold became ready"}},
		},
	)
	const awayFns = "/apis/funcd.io/v1alpha1/namespaces/e1/functions"
	rows = append(rows, hostRows(t, sh, "Function", "functions", "plain", func(name v1.ObjectName, host string) string {
		fn := function(name)
		fn.Spec.Ingress = &v1.Ingress{Host: host}
		return jsonOf(t, fn)
	})...)
	rows = append(rows,
		awayRow("getFunctionLogs", sh, http.MethodGet, "functions/echo/logs"),
		awayRow("deleteVolume", sh, http.MethodDelete, "volumes/echo/state"),
		offlineRow("createFunction", sh, nil, ask{what: "a Function with a volume whose worker node is offline",
			method: http.MethodPost, path: awayFns, body: jsonOf(t, withVolume(inNamespace("e1", function("booked"))))}),
		offlineRow("replaceFunction", sh, func(t *testing.T, e *answerEnv) { create(t, e.c, e.api, inNamespace("e1", function("grows"))) },
			ask{what: "a replace that adds a volume while the worker node is offline", method: http.MethodPut,
				path: awayFns + "/grows", body: jsonOf(t, withVolume(inNamespace("e1", function("grows"))))}))
	runMatrix(t, rows)
}
