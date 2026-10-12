package funcd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/platform/hold"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/store"
	storememory "github.com/pyvvo/funcd/internal/store/memory"
)

// changesEnv is a platform with todo-db, an EventSource on it and a subscriber that records what it fires.
type changesEnv struct {
	*sqlEnv
	feed *httptest.Server
	mu   sync.Mutex
	got  []eventing.CloudEvent
}

func todoChanges(events ...v1.SQLEvent) *v1.EventSource {
	es := &v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), Spec: v1.EventSourceSpec{SQL: &v1.SQLSource{Database: "todo-db", Events: events}}}
	es.Name, es.Namespace, es.ResourceGroup = "todo-changes", "default", "rg1"
	return es
}

// feedMasterKey is the node master of the change feed tests.
const feedMasterKey = "0123456789abcdef0123456789abcdef"

func startChanges(t *testing.T, opts []Option, events ...v1.SQLEvent) *changesEnv {
	t.Helper()
	e := &changesEnv{sqlEnv: startSQL(t, newSQLRuntime(t), append([]Option{WithMasterSecret([]byte(feedMasterKey))}, opts...)...)}
	e.init(t, events...)
	return e
}

// init seeds todo-db and the EventSource, subscribes to its events and serves the platform's feed.
func (e *changesEnv) init(t *testing.T, events ...v1.SQLEvent) {
	t.Helper()
	if _, ok := e.get(t, "todo-db"); !ok {
		seedObjects(t, e.p.cfg.store, sqlDatabase("todo-db"), todoChanges(events...))
	}
	for _, ev := range events {
		cancel := e.p.eventFanout.Subscribe("default", "todo-changes", ev.Name, func(_ context.Context, ce eventing.CloudEvent) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.got = append(e.got, ce)
		})
		t.Cleanup(cancel)
	}
	e.feed = httptest.NewServer(e.p.sqlFeed.Handler())
	t.Cleanup(e.feed.Close)
	// The boot gate opens once the Sensors present at the start subscribed: probe it with a database no EventSource
	// names, which answers 200, and fires nothing, once it is open.
	if _, ok := e.get(t, "gate-db"); !ok {
		seedObjects(t, e.p.cfg.store, sqlDatabase("gate-db"))
	}
	gate := sqlsvc.ChangeToken([]byte(feedMasterKey), "default", "gate-db")
	require.Eventually(t, func() bool { return e.post(t, gate, delivery("g", 1, insertMilk)) == http.StatusOK },
		5*time.Second, 10*time.Millisecond, "the change feed's boot gate opens")
}

func (e *changesEnv) fired() []eventing.CloudEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]eventing.CloudEvent(nil), e.got...)
}

// post delivers payload as todo-db's engine would, under token (the database's own when empty).
func (e *changesEnv) post(t *testing.T, token, payload string) int {
	t.Helper()
	if token == "" {
		token = sqlsvc.ChangeToken([]byte(feedMasterKey), "default", "todo-db")
	}
	resp, err := http.Post(e.feed.URL+"/v1/changes/"+token, "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// delivery is an engine's post of one commit per change.
func delivery(epoch string, firstIndex int, changes ...string) string {
	groups := make([]string, len(changes))
	for i, c := range changes {
		groups[i] = fmt.Sprintf(`{"index":%d,"events":[%s]}`, firstIndex+i, c)
	}
	return fmt.Sprintf(`{"service_id":%q,"node_id":"todo-db","payload":[%s]}`, epoch, strings.Join(groups, ","))
}

const (
	insertMilk = `{"op":"INSERT","table":"todos","new_row_id":1,"after":{"id":1,"title":"milk","done":0}}`
	updateMilk = `{"op":"UPDATE","table":"todos","new_row_id":1,"old_row_id":1,"before":{"id":1,"title":"milk","done":0},"after":{"id":1,"title":"milk","done":1}}`
	deleteMilk = `{"op":"DELETE","table":"todos","old_row_id":1,"before":{"id":1,"title":"milk","done":1}}`
	insertNote = `{"op":"INSERT","table":"notes","new_row_id":1,"after":{"id":1}}`
)

// added is the event most scenarios watch: an insert into todos.
func added() v1.SQLEvent {
	return v1.SQLEvent{Name: "todo-added", Table: "todos", On: []v1.SQLChangeType{v1.SQLInsert}}
}

// scenario: insert-starts-action — an insert into todos fires todo-added once, with the row in data.after.
func TestScenarioInsertStartsAction(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, added())
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk)))
	got := e.fired()
	require.Len(t, got, 1)
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got[0].Data, &data))
	require.JSONEq(t, `{"id":1,"title":"milk","done":0}`, string(data["after"]))
	require.Equal(t, "todos", got[0].Subject)
}

// scenario: update-and-delete-events — an update and a delete fire their events with before and after.
func TestScenarioUpdateAndDeleteEvents(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, v1.SQLEvent{Name: "todo-changed", Table: "todos", On: []v1.SQLChangeType{v1.SQLUpdate, v1.SQLDelete}})
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk, updateMilk, deleteMilk)))
	got := e.fired()
	require.Len(t, got, 2, "the insert matches no event")
	require.Contains(t, string(got[0].Data), `"op":"Update"`)
	require.Contains(t, string(got[0].Data), `"before":{"id":1,"title":"milk","done":0}`)
	require.Contains(t, string(got[1].Data), `"op":"Delete"`)
	require.NotContains(t, string(got[1].Data), `"after"`)
}

// scenario: other-tables-ignored — a change of another table fires nothing.
func TestScenarioOtherTablesIgnored(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, added())
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertNote)))
	require.Empty(t, e.fired())
}

// scenario: redelivery-fires-once — a commit delivered again, also while its first delivery runs, fires once.
func TestScenarioRedeliveryFiresOnce(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, added())
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk))) }()
	}
	wg.Wait()
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk)))
	require.Len(t, e.fired(), 1)
}

// scenario: recreated-database-fires — after todo-db fired up to a high index, a recreated database (its watermarks
// deleted with it) and a restored engine (a new epoch) both fire from their first index.
func TestScenarioRecreatedDatabaseFires(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, added())
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 900, insertMilk)))
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e2", 3, insertMilk)), "a restored engine's new epoch")
	require.Len(t, e.fired(), 2)

	require.NoError(t, e.p.cfg.store.Delete(e.ctx, v1.KindSQLDatabase.GVK(), "default", "todo-db", ""))
	require.Eventually(t, func() bool {
		n, err := e.p.eventStore.ChangeWatermarks().Get(e.ctx, "default", "todo-db", "e1")
		return err == nil && n == 0
	}, 5*time.Second, 10*time.Millisecond, "the deleted database's watermarks go with it")
	seedObjects(t, e.p.cfg.store, sqlDatabase("todo-db"))
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 3, insertMilk)))
	require.Len(t, e.fired(), 3, "the recreated database's low index fires")
}

// scenario: changes-wait-for-release — while the platform or the namespace is held, a delivery is 503 and fires
// nothing; after the release it fires once.
func TestScenarioChangesWaitForRelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	e := startChanges(t, []Option{WithHold(h)}, added())
	require.Equal(t, http.StatusServiceUnavailable, e.post(t, "", delivery("e1", 4, insertMilk)))
	require.NoError(t, h.Release(time.Now()))
	require.NoError(t, h.HoldNamespace("default", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	require.Equal(t, http.StatusServiceUnavailable, e.post(t, "", delivery("e1", 4, insertMilk)), "a held namespace waits too")
	require.Empty(t, e.fired())
	require.NoError(t, h.ReleaseNamespace("default", hold.ReasonData))
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk)))
	require.Len(t, e.fired(), 1)
}

// scenario: changes-wait-for-sources-at-boot — the feed reads the EventSources from the metastore on each delivery,
// so after a restart a redelivery meets the loaded sources and the stored watermark: what fired before fires no
// more, and the next commit fires once.
func TestScenarioChangesWaitForSourcesAtBoot(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dlq := WithDeadLetterQueue(dir, 2, time.Hour, 0)
	st := nonClosing{store.New(storememory.New())}
	run := func() (*changesEnv, func()) {
		p, err := New(InMemory(), WithSQLProviderRuntime(newSQLRuntime(t)), WithMasterSecret([]byte(feedMasterKey)), WithStore(st), dlq,
			WithLogger(slog.New(slog.DiscardHandler)))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		e := &changesEnv{sqlEnv: &sqlEnv{ctx: context.Background(), p: p}}
		e.init(t, added())
		return e, func() { cancel(); <-done; require.NoError(t, p.Shutdown(context.Background())) }
	}
	first, stop := run()
	require.Equal(t, http.StatusOK, first.post(t, "", delivery("e1", 4, insertMilk)))
	require.Len(t, first.fired(), 1)
	stop()

	second, stop := run()
	t.Cleanup(stop)
	require.Equal(t, http.StatusOK, second.post(t, "", delivery("e1", 4, insertMilk)), "the redelivery after the restart")
	require.Empty(t, second.fired(), "fires nothing again")
	require.Equal(t, http.StatusOK, second.post(t, "", delivery("e1", 5, insertMilk)))
	require.Len(t, second.fired(), 1)
}

// nonClosing keeps a store open across a platform's Shutdown, for the next one.
type nonClosing struct{ store.Store }

func (nonClosing) Close() error { return nil }

// scenario: forged-delivery-refused — a post under any other token is 404 and fires nothing.
func TestScenarioForgedDeliveryRefused(t *testing.T) {
	t.Parallel()
	e := startChanges(t, nil, added())
	other := sqlsvc.ChangeToken([]byte("another master, thirty-two bytes"), "default", "todo-db")
	for _, token := range []string{"forged", other} {
		require.Equal(t, http.StatusNotFound, e.post(t, token, delivery("e1", 4, insertMilk)))
	}
	require.Empty(t, e.fired())
}

// scenario: changes-wait-for-sources-at-boot (the gate) — while a Sensor present at the start has not subscribed, a
// delivery is 503 and fires nothing; once it subscribes the gate opens and the redelivery fires once.
func TestScenarioChangesWaitForSensorsAtBoot(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithSQLProviderRuntime(newSQLRuntime(t)), WithMasterSecret([]byte(feedMasterKey)),
		WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)
	e := &changesEnv{sqlEnv: &sqlEnv{ctx: context.Background(), p: p}}
	se := &v1.Sensor{TypeMeta: tmeta(v1.KindSensor), Spec: v1.SensorSpec{
		On: []v1.Dependency{{Name: "added", Source: "todo-changes", Event: "todo-added"}},
		Do: []v1.Action{{Name: "notify", On: "added", Function: "todo-notify"}},
	}}
	se.Name, se.Namespace, se.ResourceGroup = "todo-notify", "default", "rg1"
	seedObjects(t, p.cfg.store, sqlDatabase("todo-db"), todoChanges(added()), se)
	e.feed = httptest.NewServer(p.sqlFeed.Handler())
	t.Cleanup(e.feed.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opened := make(chan struct{})
	go func() { p.openSQLFeed(ctx); close(opened) }()

	require.Equal(t, http.StatusServiceUnavailable, e.post(t, "", delivery("e1", 4, insertMilk)), "the Sensor has not subscribed")
	cancelSub := p.eventFanout.Subscribe("default", "todo-changes", "todo-added", func(_ context.Context, ce eventing.CloudEvent) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.got = append(e.got, ce)
	})
	t.Cleanup(cancelSub)
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the gate stays closed once the Sensor subscribed")
	}
	require.Equal(t, http.StatusOK, e.post(t, "", delivery("e1", 4, insertMilk)))
	require.Len(t, e.fired(), 1, "the redelivery fires once, to the subscribed Sensor")
}
