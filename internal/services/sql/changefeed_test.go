package sql_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// captured is a delivery rqlite 10.5.3 posted (2026-10-12): two inserts in one commit, an update, a delete.
const captured = `{"service_id":"epoch-1","node_id":"todo-db","payload":[{"index":4,"events":[{"op":"INSERT","table":"todos","new_row_id":1,"after":{"data":"AP8=","done":0,"id":1,"score":1.5,"title":"milk"}},{"op":"INSERT","table":"todos","new_row_id":2,"after":{"data":null,"done":0,"id":2,"score":null,"title":"eggs"}}]},{"index":5,"events":[{"op":"UPDATE","table":"todos","new_row_id":1,"old_row_id":1,"before":{"data":"AP8=","done":0,"id":1,"score":1.5,"title":"milk"},"after":{"data":"AP8=","done":1,"id":1,"score":1.5,"title":"milk"}}]},{"index":6,"events":[{"op":"DELETE","table":"todos","old_row_id":2,"before":{"data":null,"done":0,"id":2,"score":null,"title":"eggs"}}]}]}`

type memMarks struct {
	mu sync.Mutex
	m  map[string]uint64
}

func (w *memMarks) Get(_ context.Context, ns v1.NamespaceName, db v1.ObjectName, epoch string) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.m[string(ns)+"/"+string(db)+"/"+epoch], nil
}

func (w *memMarks) Set(_ context.Context, ns v1.NamespaceName, db v1.ObjectName, epoch string, i uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[string(ns)+"/"+string(db)+"/"+epoch] = i
	return nil
}

func (w *memMarks) Delete(_ context.Context, ns v1.NamespaceName, db v1.ObjectName) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k := range w.m {
		if strings.HasPrefix(k, string(ns)+"/"+string(db)+"/") {
			delete(w.m, k)
		}
	}
	return nil
}

// published records the CloudEvents the feed publishes; slow makes each publish take that long.
type published struct {
	mu   sync.Mutex
	evs  []eventing.CloudEvent
	slow time.Duration
}

func (p *published) Publish(_ context.Context, ev eventing.CloudEvent) error {
	time.Sleep(p.slow)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
	return nil
}

func (p *published) all() []eventing.CloudEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]eventing.CloudEvent(nil), p.evs...)
}

type feedRig struct {
	st    store.Store
	pub   *published
	marks *memMarks
	srv   *httptest.Server
	feed  *sqlsvc.Feed
	held  heldIn
}

type heldIn struct {
	mu  sync.Mutex
	all bool
	ns  map[v1.NamespaceName]bool
}

func (h *heldIn) Held() bool          { h.mu.Lock(); defer h.mu.Unlock(); return h.all }
func (*heldIn) ReleasedAt() time.Time { return time.Time{} }
func (h *heldIn) HeldIn(ns v1.NamespaceName) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ns[ns]
}
func (*heldIn) ReleasedAtIn(v1.NamespaceName) time.Time { return time.Time{} }

func newFeedRig(t *testing.T, sources ...*v1.EventSource) *feedRig {
	t.Helper()
	r := &feedRig{st: store.New(memory.New()), pub: &published{}, marks: &memMarks{m: map[string]uint64{}}}
	r.held.ns = map[v1.NamespaceName]bool{}
	ctx := context.Background()
	for _, name := range []v1.ObjectName{"todo-db", "audit-db"} {
		d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
		d.Name, d.Namespace, d.ResourceGroup = name, "default", "rg1"
		_, err := r.st.Create(ctx, d)
		require.NoError(t, err)
	}
	for _, es := range sources {
		_, err := r.st.Create(ctx, es)
		require.NoError(t, err)
	}
	f, err := sqlsvc.NewFeed(sqlsvc.FeedDeps{Store: r.st, Publisher: r.pub, Watermarks: r.marks, Master: []byte(masterKey),
		Hold: &r.held, NamespaceHold: &r.held})
	require.NoError(t, err)
	r.feed = f
	f.Loaded()
	r.srv = httptest.NewServer(f.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func todoSource(name string, events ...v1.SQLEvent) *v1.EventSource {
	es := &v1.EventSource{TypeMeta: v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		Spec: v1.EventSourceSpec{SQL: &v1.SQLSource{Database: "todo-db", Events: events}}}
	es.Name, es.Namespace, es.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return es
}

func (r *feedRig) post(t *testing.T, db v1.ObjectName, body string) int {
	t.Helper()
	resp, err := http.Post(r.srv.URL+"/v1/changes/"+sqlsvc.ChangeToken([]byte(masterKey), "default", db), "application/json", strings.NewReader(body))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestChangeToken(t *testing.T) {
	a := sqlsvc.ChangeToken([]byte(masterKey), "default", "todo-db")
	require.Len(t, a, 43, "256 bits, base64url")
	require.NotEqual(t, a, sqlsvc.ChangeToken([]byte(masterKey), "default", "audit-db"))
	require.NotEqual(t, a, sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db"), "the token is not the engine password")
}

// The captured delivery: each change matching an event fires once with its row, id, subject and op.
func TestFeedDecodesAndMatches(t *testing.T) {
	r := newFeedRig(t,
		todoSource("todo-changes",
			v1.SQLEvent{Name: "todo-added", Table: "todos", On: []v1.SQLChangeType{v1.SQLInsert}},
			v1.SQLEvent{Name: "todo-changed", Table: "todos", On: []v1.SQLChangeType{v1.SQLUpdate, v1.SQLDelete}},
			v1.SQLEvent{Name: "other", Table: "notes"},
		))
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", captured))
	evs := r.pub.all()
	require.Len(t, evs, 4)
	require.Equal(t, "todo-added", evs[0].Type)
	require.Equal(t, "default/todo-db/epoch-1/4/todo-added/0", evs[0].ID)
	require.Equal(t, "default/todo-db/epoch-1/4/todo-added/1", evs[1].ID)
	require.Equal(t, "todos", evs[0].Subject)
	require.Equal(t, eventing.SourceURI("default", "todo-changes"), evs[0].Source)
	require.JSONEq(t, `{"op":"Insert","table":"todos","rowId":1,"after":{"data":"AP8=","done":0,"id":1,"score":1.5,"title":"milk"},"index":4}`, string(evs[0].Data))
	require.JSONEq(t, `{"op":"Update","table":"todos","rowId":1,"before":{"data":"AP8=","done":0,"id":1,"score":1.5,"title":"milk"},"after":{"data":"AP8=","done":1,"id":1,"score":1.5,"title":"milk"},"index":5}`, string(evs[2].Data))
	require.JSONEq(t, `{"op":"Delete","table":"todos","rowId":2,"before":{"data":null,"done":0,"id":2,"score":null,"title":"eggs"},"index":6}`, string(evs[3].Data))
	m, _ := r.marks.Get(context.Background(), "default", "todo-db", "epoch-1")
	require.Equal(t, uint64(6), m)
}

// A redelivered commit fires nothing, also while its first delivery is still being processed; a new epoch (a
// recreated or restored engine) starts again from its first index.
func TestFeedRedeliveryAndEpochs(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "todo-added", Table: "todos", On: []v1.SQLChangeType{v1.SQLInsert}}))
	r.pub.slow = 100 * time.Millisecond
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); require.Equal(t, http.StatusOK, r.post(t, "todo-db", captured)) }()
	}
	wg.Wait()
	require.Len(t, r.pub.all(), 2, "two overlapping deliveries of one commit fire once")
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", captured))
	require.Len(t, r.pub.all(), 2)
	r.pub.slow = 0
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", strings.Replace(captured, "epoch-1", "epoch-2", 1)))
	require.Len(t, r.pub.all(), 4, "a new epoch's low indexes are new changes")
}

// An unknown token is 404 with its body unread; a database no EventSource names is acknowledged and dropped.
func TestFeedTokensAndUnwatched(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/v1/changes/forged", strings.NewReader(captured))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, http.StatusOK, r.post(t, "audit-db", captured), "no EventSource names audit-db")
	require.Empty(t, r.pub.all())
}

// While the platform or the namespace is held, a database an EventSource names is 503 and its changes wait; one no
// EventSource names is dropped.
func TestFeedHolds(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	for _, set := range []func(bool){
		func(b bool) { r.held.mu.Lock(); r.held.all = b; r.held.mu.Unlock() },
		func(b bool) { r.held.mu.Lock(); r.held.ns["default"] = b; r.held.mu.Unlock() },
	} {
		set(true)
		require.Equal(t, http.StatusServiceUnavailable, r.post(t, "todo-db", captured))
		require.Equal(t, http.StatusOK, r.post(t, "audit-db", captured))
		require.Empty(t, r.pub.all())
		set(false)
	}
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", captured))
	require.Len(t, r.pub.all(), 4, "after the release the changes fire once")
}

// A change whose rows pass 256 KiB fires without them, truncated; a 9 MiB row never sits whole in memory beyond its
// one value, and the next commit still fires.
func TestFeedTruncatesLargeRows(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	big := strings.Repeat("x", 9<<20)
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"service_id":"e","node_id":"n","payload":[{"index":1,"events":[{"op":"INSERT","table":"todos","new_row_id":1,"after":{"id":1,"v":%q}}]},`, big)
	b.WriteString(`{"index":2,"events":[{"op":"INSERT","table":"todos","new_row_id":2,"after":{"id":2,"v":"small"}}]}]}`)
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", b.String()))
	evs := r.pub.all()
	require.Len(t, evs, 2)
	require.JSONEq(t, `{"op":"Insert","table":"todos","rowId":1,"index":1,"truncated":true}`, string(evs[0].Data))
	require.JSONEq(t, `{"op":"Insert","table":"todos","rowId":2,"after":{"id":2,"v":"small"},"index":2}`, string(evs[1].Data))
}

// A malformed body is 400 and raises nothing.
func TestFeedRefusesMalformed(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	require.Equal(t, http.StatusBadRequest, r.post(t, "todo-db", `{"payload":[]}`), "the payload before service_id")
	require.Equal(t, http.StatusBadRequest, r.post(t, "todo-db", `not json`))
}

// A large delivery for a database no EventSource names is read to its end before its 200, so the engine sees the
// batch delivered and does not retry it.
func TestFeedDrainsUnwatched(t *testing.T) {
	r := newFeedRig(t)
	body := `{"service_id":"e","node_id":"n","payload":[{"index":1,"events":[{"op":"INSERT","table":"blobs","new_row_id":1,"after":{"v":"` +
		strings.Repeat("A", 20<<20) + `"}}]}]}`
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", body))
}

// ADR-0235 Decision 4: until the feed is loaded every valid token is a 503 and nothing fires; an unknown token is still
// a 404.
func TestFeedBootGate(t *testing.T) {
	r := &feedRig{st: store.New(memory.New()), pub: &published{}, marks: &memMarks{m: map[string]uint64{}}}
	ctx := context.Background()
	d := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}}
	d.Name, d.Namespace, d.ResourceGroup = "todo-db", "default", "rg1"
	_, err := r.st.Create(ctx, d)
	require.NoError(t, err)
	_, err = r.st.Create(ctx, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	require.NoError(t, err)
	f, err := sqlsvc.NewFeed(sqlsvc.FeedDeps{Store: r.st, Publisher: r.pub, Watermarks: r.marks, Master: []byte(masterKey)})
	require.NoError(t, err)
	r.srv = httptest.NewServer(f.Handler())
	t.Cleanup(r.srv.Close)
	require.Equal(t, http.StatusServiceUnavailable, r.post(t, "todo-db", captured))
	resp, err := http.Post(r.srv.URL+"/v1/changes/forged", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Empty(t, r.pub.all())
	f.Loaded()
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", captured))
	require.Len(t, r.pub.all(), 4)
}

// The rows of a table no event watches are skipped while decoding, and the watched change beside them still fires.
func TestFeedSkipsUnwatchedTables(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	body := `{"service_id":"e","node_id":"n","payload":[{"index":1,"events":[` +
		`{"op":"INSERT","table":"blobs","new_row_id":1,"after":{"v":"` + strings.Repeat("A", 1<<20) + `"}},` +
		`{"op":"INSERT","table":"todos","new_row_id":7,"after":{"id":7}}]}]}`
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", body))
	evs := r.pub.all()
	require.Len(t, evs, 1)
	require.Equal(t, "default/todo-db/e/1/any/1", evs[0].ID, "n counts every change of the group")
}

// A delivery that breaks mid-group leaves its watermark; its resend fires only the changes not published yet.
func TestFeedBrokenGroupResendFiresOnce(t *testing.T) {
	r := newFeedRig(t, todoSource("todo-changes", v1.SQLEvent{Name: "any", Table: "todos"}))
	whole := `{"service_id":"e","node_id":"n","payload":[{"index":1,"events":[` +
		`{"op":"INSERT","table":"todos","new_row_id":1,"after":{"id":1}},` +
		`{"op":"INSERT","table":"todos","new_row_id":2,"after":{"id":2}}]}]}`
	cut := whole[:strings.Index(whole, `{"op":"INSERT","table":"todos","new_row_id":2`)+20]
	require.Equal(t, http.StatusBadRequest, r.post(t, "todo-db", cut))
	require.Len(t, r.pub.all(), 1, "the first change fired before the break")
	require.Equal(t, http.StatusOK, r.post(t, "todo-db", whole))
	evs := r.pub.all()
	require.Len(t, evs, 2, "the resend fires the second change only")
	require.Equal(t, "default/todo-db/e/1/any/1", evs[1].ID)
}
