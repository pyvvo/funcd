package sql

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

// maxChangeData is the most of a change's rows an event carries; past it before and after are dropped (ADR-0235
// Decision 5).
const maxChangeData = 256 << 10

// ChangeToken is a database's change feed capability (ADR-0235 Decision 2): HMAC-SHA256 of the node master over
// "funcd-sql-cdc/<ns>/<name>", base64url. Nothing stores it.
func ChangeToken(master []byte, ns v1.NamespaceName, name v1.ObjectName) string {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("funcd-sql-cdc/" + string(ns) + "/" + string(name)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Watermarks keeps, per database and engine epoch, the highest Raft index whose events were published (ADR-0235
// Decision 3); the event store's ChangeWatermarks implements it.
type Watermarks interface {
	Get(ctx context.Context, ns v1.NamespaceName, database v1.ObjectName, epoch string) (uint64, error)
	Set(ctx context.Context, ns v1.NamespaceName, database v1.ObjectName, epoch string, index uint64) error
	Delete(ctx context.Context, ns v1.NamespaceName, database v1.ObjectName) error
}

// FeedDeps configures the change feed; Store, Publisher, Watermarks and Master are required.
type FeedDeps struct {
	Store         store.Store
	Publisher     eventing.Publisher
	Watermarks    Watermarks
	Master        []byte
	Hold          hold.Gate          // nil ⇒ never held
	NamespaceHold hold.NamespaceGate // nil ⇒ no namespace held
	Logger        *slog.Logger
}

// Feed receives the changes every engine posts and turns those an EventSource names into named CloudEvents
// (ADR-0235). It reads the databases and EventSources from the store on each delivery, so it holds no registry; its
// boot gate stays closed until Loaded, so no change is published before the Sensors that act on it subscribe.
type Feed struct {
	d      FeedDeps
	log    *slog.Logger
	loaded atomic.Bool // the boot gate: until set, every valid token is a 503 (ADR-0235 Decision 4)
	mu     sync.Mutex
	locks  map[string]*sync.Mutex // per database, from the watermark check to its raise
	// partial is, per database, the last change published of a group whose delivery broke before its watermark was
	// raised, so a resend does not fire it again; only a daemon crash can still repeat it (ADR-0235 Decision 3).
	partial map[string]partialMark
}

// partialMark is the last change n published of group index of epoch.
type partialMark struct {
	epoch string
	index uint64
	n     int
}

// Loaded opens the boot gate: the Sensors present at the start have subscribed, so an event the feed publishes
// reaches them. Until then every delivery with a valid token answers 503 and rqlite retries it.
func (f *Feed) Loaded() { f.loaded.Store(true) }

// NewFeed builds the change feed.
func NewFeed(d FeedDeps) (*Feed, error) {
	const op = "sql.NewFeed"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Publisher == nil:
		return nil, fault.Invalidf(op, "publisher is required")
	case d.Watermarks == nil:
		return nil, fault.Invalidf(op, "watermarks are required")
	case len(d.Master) == 0:
		return nil, fault.Invalidf(op, "node master is required")
	}
	if d.Hold == nil {
		d.Hold = hold.Never
	}
	if d.NamespaceHold == nil {
		d.NamespaceHold = hold.NeverNamespace
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Feed{d: d, log: d.Logger.With("component", "sql.changefeed"), locks: map[string]*sync.Mutex{}, partial: map[string]partialMark{}}, nil
}

// URL is the endpoint a database's engine posts its changes to, on host:port.
func (f *Feed) URL(host string, port int) func(ns v1.NamespaceName, name v1.ObjectName) string {
	return func(ns v1.NamespaceName, name v1.ObjectName) string {
		return fmt.Sprintf("http://%s/v1/changes/%s", net.JoinHostPort(host, fmt.Sprint(port)), ChangeToken(f.d.Master, ns, name))
	}
}

// Serve listens on host:port, retrying each second until the address exists (the bridge appears with the first
// worker), and serves the feed until ctx is done.
func (f *Feed) Serve(ctx context.Context, host string, port int) error {
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	var ln net.Listener
	for warned := false; ; {
		var err error
		if ln, err = (&net.ListenConfig{}).Listen(ctx, "tcp", addr); err == nil {
			break
		}
		if !warned {
			f.log.WarnContext(ctx, "the sql change feed cannot listen yet; retrying each second", "addr", addr, "err", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	srv := &http.Server{Handler: f.Handler(), ReadHeaderTimeout: 5 * time.Second, ErrorLog: slog.NewLogLogger(f.log.Handler(), slog.LevelWarn)}
	context.AfterFunc(ctx, func() { _ = srv.Close() })
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fault.Wrapf(err, fault.Unavailable, "sql.Feed", "serve the change feed")
	}
	return nil
}

// Handler is POST /v1/changes/{token}. The path, which carries the token, is never logged.
func (f *Feed) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/changes/{token}", f.serve)
	return mux
}

func (f *Feed) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	db, err := f.database(ctx, r.PathValue("token"))
	switch {
	case err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case db == nil:
		// an unknown token: its body is never read
		w.WriteHeader(http.StatusNotFound)
		return
	case !f.loaded.Load():
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	key := engineKey(db.Namespace, db.Name)
	lock := f.lock(key)
	lock.Lock()
	defer lock.Unlock()
	sources, err := f.sources(ctx, db)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if len(sources) == 0 {
		// No EventSource wants these changes: acknowledged and dropped. The body is drained, streamed, first: the
		// server would close a connection whose large body is unread, and rqlite would retry the batch for ever.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		return
	}
	if f.d.Hold.Held() || f.d.NamespaceHold.HeldIn(db.Namespace) {
		w.WriteHeader(http.StatusServiceUnavailable) // rqlite keeps them and retries after the release
		return
	}
	if err := f.deliver(ctx, db, sources, r.Body); err != nil {
		f.log.WarnContext(ctx, "sql change delivery failed", "database", key, "err", err)
		if fault.KindOf(err) == fault.Invalid {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *Feed) partialOf(key string) (partialMark, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pm, ok := f.partial[key]
	return pm, ok
}

func (f *Feed) setPartial(key string, pm partialMark) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.partial[key] = pm
}

func (f *Feed) clearPartial(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.partial, key)
}

func (f *Feed) lock(key string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.locks[key]
	if !ok {
		l = &sync.Mutex{}
		f.locks[key] = l
	}
	return l
}

// database finds the database whose token this is, comparing in constant time; nil when none.
func (f *Feed) database(ctx context.Context, token string) (*v1.SQLDatabase, error) {
	l, err := f.d.Store.List(ctx, v1.KindSQLDatabase.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found *v1.SQLDatabase
	for _, o := range l.Items {
		db, ok := o.(*v1.SQLDatabase)
		if ok && hmac.Equal([]byte(ChangeToken(f.d.Master, db.Namespace, db.Name)), []byte(token)) {
			found = db
		}
	}
	return found, nil
}

// sources are the database's EventSources, their events defaulted.
func (f *Feed) sources(ctx context.Context, db *v1.SQLDatabase) ([]*v1.EventSource, error) {
	l, err := f.d.Store.List(ctx, v1.KindEventSource.GVK(), store.ListOptions{Namespace: db.Namespace})
	if err != nil {
		return nil, err
	}
	var out []*v1.EventSource
	for _, o := range l.Items {
		if es, ok := o.(*v1.EventSource); ok && es.Spec.SQL != nil && es.Spec.SQL.Database == db.Name {
			view := &v1.EventSource{ObjectMeta: es.ObjectMeta, Spec: v1.EventSourceSpec{SQL: &v1.SQLSource{
				Database: es.Spec.SQL.Database, Events: slices.Clone(es.Spec.SQL.Events)}}}
			view.Normalize()
			out = append(out, view)
		}
	}
	return out, nil
}

// changeData is a SQL CloudEvent's data (ADR-0235 Decision 5).
type changeData struct {
	Op              v1.SQLChangeType `json:"op"`
	Table           string           `json:"table"`
	RowID           int64            `json:"rowId"`
	Before          json.RawMessage  `json:"before,omitempty"`
	After           json.RawMessage  `json:"after,omitempty"`
	Index           uint64           `json:"index"`
	CommitTimestamp int64            `json:"commitTimestamp,omitempty"`
	Truncated       bool             `json:"truncated,omitempty"`
}

// change is one row change of a group, as the stream decodes it.
type change struct {
	op, table         string
	newRowID, oldRow  int64
	before, after     []byte
	truncated, failed bool
}

// deliver walks rqlite's envelope {service_id, node_id, payload: [{index, commit_timestamp, events}]} as a token
// stream (service_id comes first), so a commit of any size never sits whole in memory: each group at or below the
// epoch's watermark is skipped, each other one's matching changes are published, then the watermark raised.
func (f *Feed) deliver(ctx context.Context, db *v1.SQLDatabase, sources []*v1.EventSource, body io.Reader) error {
	const op = "sql.changefeed"
	dec := json.NewDecoder(body)
	dec.UseNumber()
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	epoch := ""
	for dec.More() {
		k, err := key(dec)
		if err != nil {
			return err
		}
		switch k {
		case "service_id":
			if err := dec.Decode(&epoch); err != nil {
				return fault.Invalidf(op, "service_id: %v", err)
			}
		case "payload":
			if epoch == "" {
				return fault.Invalidf(op, "the payload comes before service_id")
			}
			if err := f.groups(ctx, dec, db, sources, epoch); err != nil {
				return err
			}
		default:
			if err := skip(dec); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *Feed) groups(ctx context.Context, dec *json.Decoder, db *v1.SQLDatabase, sources []*v1.EventSource, epoch string) error {
	const op = "sql.changefeed"
	mark, err := f.d.Watermarks.Get(ctx, db.Namespace, db.Name, epoch)
	if err != nil {
		return err
	}
	watched := func(table string) bool {
		for _, es := range sources {
			if slices.ContainsFunc(es.Spec.SQL.Events, func(ev v1.SQLEvent) bool { return ev.Table == table }) {
				return true
			}
		}
		return false
	}
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		if err := expectDelim(dec, '{'); err != nil {
			return err
		}
		var index uint64
		var ts int64
		for dec.More() {
			k, err := key(dec)
			if err != nil {
				return err
			}
			switch k {
			case "index":
				if err := dec.Decode(&index); err != nil {
					return fault.Invalidf(op, "index: %v", err)
				}
			case "commit_timestamp":
				if err := dec.Decode(&ts); err != nil {
					return fault.Invalidf(op, "commit_timestamp: %v", err)
				}
			case "events":
				// rqlite encodes index and commit_timestamp before events, so a group already published is skipped
				// unread, and each other change is published as soon as it is decoded.
				if index == 0 || index <= mark {
					if err := skip(dec); err != nil {
						return err
					}
					continue
				}
				key := engineKey(db.Namespace, db.Name)
				if err := changes(dec, watched, func(n int, c change) error {
					if pm, ok := f.partialOf(key); ok && pm.epoch == epoch && pm.index == index && n <= pm.n {
						return nil // published by a delivery of this group that broke
					}
					if err := f.publish(ctx, db, sources, epoch, index, ts, n, c); err != nil {
						return err
					}
					f.setPartial(key, partialMark{epoch: epoch, index: index, n: n})
					return nil
				}); err != nil {
					return err
				}
			default:
				if err := skip(dec); err != nil {
					return err
				}
			}
		}
		if _, err := dec.Token(); err != nil {
			return fault.Invalidf(op, "a torn group: %v", err)
		}
		if index <= mark {
			continue
		}
		if err := f.d.Watermarks.Set(ctx, db.Namespace, db.Name, epoch, index); err != nil {
			return err
		}
		f.clearPartial(engineKey(db.Namespace, db.Name))
		mark = index
	}
	_, err = dec.Token()
	return err
}

// changes decodes a group's events and hands each change to emit as it ends. rqlite encodes op and table before
// before and after, so the rows of a table no event watches are skipped, and a watched change's rows are copied only
// up to maxChangeData.
func changes(dec *json.Decoder, watched func(table string) bool, emit func(n int, c change) error) error {
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for n := 0; dec.More(); n++ {
		if err := expectDelim(dec, '{'); err != nil {
			return err
		}
		var c change
		budget := maxChangeData
		for dec.More() {
			k, err := key(dec)
			if err != nil {
				return err
			}
			switch k {
			case "op":
				err = dec.Decode(&c.op)
			case "table":
				err = dec.Decode(&c.table)
			case "new_row_id":
				err = dec.Decode(&c.newRowID)
			case "old_row_id":
				err = dec.Decode(&c.oldRow)
			case "error":
				var msg string
				err = dec.Decode(&msg)
				c.failed = msg != ""
			case "before", "after":
				if !watched(c.table) {
					err = skip(dec)
					break
				}
				var buf bytes.Buffer
				over, cerr := copyValue(dec, &buf, budget)
				if cerr != nil {
					return cerr
				}
				budget -= buf.Len()
				c.truncated = c.truncated || over
				if k == "before" {
					c.before = buf.Bytes()
				} else {
					c.after = buf.Bytes()
				}
			default:
				err = skip(dec)
			}
			if err != nil {
				return fault.Invalidf("sql.changefeed", "change field %s: %v", k, err)
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		if c.truncated {
			c.before, c.after = nil, nil
		}
		if watched(c.table) {
			if err := emit(n, c); err != nil {
				return err
			}
		}
	}
	_, err := dec.Token()
	return err
}

// publish fires, for one change of a group, every event of the sources whose table and changes match.
func (f *Feed) publish(ctx context.Context, db *v1.SQLDatabase, sources []*v1.EventSource, epoch string, index uint64, ts int64, n int, c change) error {
	kind, ok := map[string]v1.SQLChangeType{"INSERT": v1.SQLInsert, "UPDATE": v1.SQLUpdate, "DELETE": v1.SQLDelete}[c.op]
	if !ok || c.failed {
		return nil
	}
	rowID := c.newRowID
	if kind == v1.SQLDelete {
		rowID = c.oldRow
	}
	data, err := json.Marshal(changeData{Op: kind, Table: c.table, RowID: rowID, Before: c.before, After: c.after,
		Index: index, CommitTimestamp: ts, Truncated: c.truncated})
	if err != nil {
		return fault.Internalf("sql.changefeed", "encode a change: %v", err)
	}
	for _, es := range sources {
		for _, ev := range es.Spec.SQL.Events {
			if ev.Table != c.table || !slices.Contains(ev.On, kind) {
				continue
			}
			ce := eventing.CloudEvent{
				SpecVersion: "1.0", Source: eventing.SourceURI(es.Namespace, es.Name), Type: string(ev.Name), Subject: c.table,
				ID:   fmt.Sprintf("%s/%s/%s/%d/%s/%d", db.Namespace, db.Name, epoch, index, ev.Name, n),
				Time: v1.NewTimestamp(time.Now()), DataContentType: "application/json", Data: data,
			}
			if err := f.d.Publisher.Publish(ctx, ce); err != nil {
				return fault.Wrapf(err, fault.Unavailable, "sql.changefeed", "publish %s", ce.ID)
			}
		}
	}
	return nil
}

func expectDelim(dec *json.Decoder, d json.Delim) error {
	t, err := dec.Token()
	if err != nil {
		return fault.Invalidf("sql.changefeed", "decode: %v", err)
	}
	if t != d {
		return fault.Invalidf("sql.changefeed", "want %v, got %v", d, t)
	}
	return nil
}

func key(dec *json.Decoder) (string, error) {
	t, err := dec.Token()
	if err != nil {
		return "", fault.Invalidf("sql.changefeed", "decode: %v", err)
	}
	k, ok := t.(string)
	if !ok {
		return "", fault.Invalidf("sql.changefeed", "want a key, got %v", t)
	}
	return k, nil
}

// skip consumes one value token by token.
func skip(dec *json.Decoder) error {
	_, err := copyValue(dec, nil, 0)
	return err
}

// copyValue consumes one value and writes it to buf while it fits budget bytes; past that it writes nothing more and
// reports over. A nil buf only consumes. One token, at most one column's value, is in memory at a time.
func copyValue(dec *json.Decoder, buf *bytes.Buffer, budget int) (over bool, err error) {
	type frame struct {
		object bool
		n      int // the tokens written at this level: an object alternates keys and values
	}
	var stack []frame
	write := func(b []byte) {
		if buf == nil || over {
			return
		}
		if buf.Len()+len(b) > budget {
			over = true
			buf.Reset()
			return
		}
		buf.Write(b)
	}
	sep := func() {
		if len(stack) == 0 {
			return
		}
		f := &stack[len(stack)-1]
		switch {
		case f.object && f.n%2 == 1:
			write([]byte{':'})
		case f.n > 0:
			write([]byte{','})
		}
		f.n++
	}
	for {
		t, err := dec.Token()
		if err != nil {
			return false, fault.Invalidf("sql.changefeed", "decode: %v", err)
		}
		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				sep()
				write([]byte{byte(v)})
				stack = append(stack, frame{object: v == '{'})
			default:
				write([]byte{byte(v)})
				stack = stack[:len(stack)-1]
			}
		default:
			sep()
			b, merr := json.Marshal(v)
			if merr != nil {
				return false, fault.Invalidf("sql.changefeed", "re-encode a value: %v", merr)
			}
			write(b)
		}
		if len(stack) == 0 {
			return over, nil
		}
	}
}
