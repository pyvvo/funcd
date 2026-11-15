//go:build !e2e

package funcd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
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
	p, err := funcd.New(append([]funcd.Option{funcd.InMemory(), funcd.WithCredentials(
		funcd.Credential{Token: answersAdmin, Role: "admin"},
		funcd.Credential{Token: answersDev, Role: "developer", Namespaces: []string{"default"}},
		funcd.Credential{Token: answersOther, Role: "developer", Namespaces: []string{"other"}},
	)}, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return p
}

// ask is one request and the answer it gets: its status and a part of its body.
type ask struct {
	what         string // the case it provokes
	method, path string
	token        string
	header       []string // name, value pairs; a JSON body gets Content-Type application/json unless one is given
	body         string
	status       int
	detail       string
}

// answersClient sends the asks on connections of its own, so no other test's server shuts them.
func answersClient() *http.Client {
	return &http.Client{Transport: &http.Transport{}, Timeout: 30 * time.Second}
}

func (a ask) run(t *testing.T, c *http.Client, base string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), a.method, base+a.path, strings.NewReader(a.body))
	require.NoError(t, err)
	if a.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(a.header); i += 2 {
		req.Header.Set(a.header[i], a.header[i+1])
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, a.status, resp.StatusCode, "%s %s, %s: %s", a.method, a.path, a.what, body)
	var problem struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &problem) // a problem's detail, unescaped; another body is matched as it is
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

// callable applies Function name, always on, whose artifact holds contract when it is not empty, and waits for it to
// be Ready; it returns the artifact's directory.
func callable(t *testing.T, c *http.Client, base string, name v1.ObjectName, contract string, edit func(*v1.Function)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.mjs"), []byte("export function handle() {}\n"), 0o600))
	if contract != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "__funcd_contract.json"), []byte(contract), 0o600))
	}
	fn := function(name)
	fn.Spec.Image = "file://" + filepath.Join(dir, "handler.mjs")
	fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
	if edit != nil {
		edit(fn)
	}
	create(t, c, base, fn)
	require.Eventually(t, func() bool {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			base+"/apis/funcd.io/v1alpha1/namespaces/default/functions/"+string(name), nil)
		req.Header.Set("Authorization", "Bearer "+answersAdmin)
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		var got v1.Function
		return json.NewDecoder(resp.Body).Decode(&got) == nil && got.Status.Phase == v1.PhaseReady
	}, 20*time.Second, 20*time.Millisecond, "Function %s is Ready", name)
	return dir
}

// lockedNamespace applies Namespace locked, whose stance asks every caller for a credential.
func lockedNamespace(t *testing.T, c *http.Client, base string) {
	t.Helper()
	ns := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace},
		ObjectMeta: v1.ObjectMeta{Name: "locked"}}
	ns.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	create(t, c, base, ns)
}

// Each case of invokeFunction, through the gateway, on workers that are the test's handlers; the limits' cases have
// platforms of their own.
func TestAnswersOfInvokeFunction(t *testing.T) {
	t.Parallel()
	workers := newFakeWorkers()
	p := answersPlatform(t, funcd.WithStore(brokenNamespaceReads{store.New(memory.New())}), funcd.WithRuntime(workers),
		funcd.WithRuntimeShim("node", "shim.mjs"), funcd.WithEdgeAuth(), funcd.WithGatewayReadTimeoutForTest(300*time.Millisecond))
	c, api, gw := answersClient(), "http://"+p.Addr(), "http://"+p.DataPlaneAddr()
	lockedNamespace(t, c, api)
	workers.serve(t, "echo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	workers.serve(t, "strict", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	workers.serve(t, "crash", func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	workers.serve(t, "slow", func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // a read body lets the server see the cut connection
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	workers.serve(t, "unread", func(http.ResponseWriter, *http.Request) {})
	callable(t, c, api, "echo", "", nil)
	callable(t, c, api, "strict", `{"input": {"type": "object", "required": ["a"]}, "output": {"type": "object", "required": ["b"]}}`, nil)
	callable(t, c, api, "crash", "", nil)
	callable(t, c, api, "slow", "", func(fn *v1.Function) { fn.Spec.Timeout = v1.Duration(time.Second) })
	unread := callable(t, c, api, "unread", `{"input": {"type": "object"}, "output": {}}`, nil)
	off, routed := function("closed"), function("routed")
	off.Spec.Ingress = &v1.Ingress{Enabled: new(false)}
	routed.Spec.Ingress = &v1.Ingress{Path: "/routed"}
	create(t, c, api, off)
	create(t, c, api, routed)
	contract := filepath.Join(unread, "__funcd_contract.json")
	require.NoError(t, os.Remove(contract))
	require.NoError(t, os.Mkdir(contract, 0o700))

	const call = "/function/"
	for _, a := range []ask{
		{what: "the handler's result", method: http.MethodPost, path: call + "echo", body: `{}`, status: 200,
			detail: `{"ok":true}`},
		{what: "a missing Function", method: http.MethodPost, path: call + "nope", body: `{}`, status: 404,
			detail: "function default/nope is not exposed by name"},
		{what: "a Function whose ingress is off", method: http.MethodPost, path: call + "closed", body: `{}`,
			status: 404, detail: "function default/closed is not exposed by name"},
		{what: "a Function at a path of its own", method: http.MethodPost, path: call + "routed", body: `{}`,
			status: 404, detail: "function default/routed is not exposed by name"},
		{what: "no credential where the stance asks for one", method: http.MethodPost, path: call + "ghost",
			header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, status: 401, detail: "missing bearer token"},
		{what: "a credential that funcd does not know", method: http.MethodPost, path: call + "ghost",
			token: "unknown-token", header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, status: 401,
			detail: "invalid credential"},
		{what: "a credential of another namespace", method: http.MethodPost, path: call + "ghost", token: answersOther,
			header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, status: 403, detail: "not authorized to invoke"},
		{what: "a body over 1 MiB", method: http.MethodPost, path: call + "echo",
			body: `"` + strings.Repeat("x", 1<<20) + `"`, status: 413, detail: "request body exceeds 1048576 bytes"},
		{what: "data that breaks the input contract", method: http.MethodPost, path: call + "strict", body: `{}`,
			status: 422, detail: "event data does not match the input contract"},
		{what: "a result that breaks the output contract", method: http.MethodPost, path: call + "strict",
			body: `{"a": 1}`, status: 500, detail: "handler result does not match the output contract"},
		{what: "a worker that fails during the call", method: http.MethodPost, path: call + "crash", body: `{}`,
			status: 503, detail: "upstream call failed"},
		{what: "a namespace whose settings cannot be read", method: http.MethodPost, path: call + "ghost",
			header: []string{"X-Funcd-Namespace", "broken"}, body: `{}`, status: 503, detail: "edge settings unavailable"},
		{what: "a contract that cannot be read", method: http.MethodPost, path: call + "unread", body: `{}`,
			status: 503, detail: "the contract of default/unread cannot be read"},
		{what: "a worker past the deadline", method: http.MethodPost, path: call + "slow", body: `{}`, status: 504,
			detail: "did not start its response within 1s (spec.timeout)"},
	} {
		a.run(t, c, gw)
	}

	conn, err := net.Dial("tcp", p.DataPlaneAddr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = io.WriteString(conn, "POST /function/echo HTTP/1.1\r\nHost: funcd\r\nContent-Length: 100\r\n\r\n{")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "a body that stops arriving: %s", body)
	require.Contains(t, string(body), "could not read request body")
}

// The gateway's limits, and a stance that no authenticator can check: each needs a platform of its own.
func TestAnswersOfInvokeFunctionLimits(t *testing.T) {
	t.Parallel()
	t.Run("rate", func(t *testing.T) {
		t.Parallel()
		p := answersPlatform(t, funcd.WithLimits(limit.Config{RatePerMin: 1, Burst: 1}))
		c := answersClient()
		lockedNamespace(t, c, "http://"+p.Addr())
		gw := "http://" + p.DataPlaneAddr()
		ask{what: "a stance that server.auth.edge off cannot check", method: http.MethodPost, path: "/function/ghost",
			header: []string{"X-Funcd-Namespace", "locked"}, body: `{}`, status: 401,
			detail: "no authenticator is configured"}.run(t, c, gw)
		ask{what: "a call over the rate", method: http.MethodPost, path: "/function/ghost", body: `{}`, status: 429,
			detail: "rate limit exceeded"}.run(t, c, gw)
	})
	t.Run("in flight", func(t *testing.T) {
		t.Parallel()
		p := answersPlatform(t, funcd.WithLimits(limit.Config{MaxInFlight: 1}),
			funcd.WithGatewayReadTimeoutForTest(2*time.Second))
		c := answersClient()
		create(t, c, "http://"+p.Addr(), function("held"))
		conn, err := net.Dial("tcp", p.DataPlaneAddr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		_, err = io.WriteString(conn, "POST /function/held HTTP/1.1\r\nHost: funcd\r\nContent-Length: 100\r\n\r\n{")
		require.NoError(t, err)
		gw := "http://" + p.DataPlaneAddr() + "/function/ghost"
		require.Eventually(t, func() bool { return statusOf(c, http.MethodPost, gw, "") == http.StatusServiceUnavailable },
			5*time.Second, 20*time.Millisecond, "the held call takes the only slot")
	})
}
