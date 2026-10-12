package sql

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
)

// emptyDatabase is an empty SQLite file, 4 KiB in DELETE mode: loading it replaces all of a database's data
// (ADR-0234 Decision 4).
//
//go:embed empty.sqlite
var emptyDatabase []byte

// streamErrorTrailer is the trailer rqlite sets when a backup stream broke after its status was sent.
const streamErrorTrailer = "X-Stream-Error"

// userObjectsQuery counts a database's tables, views, indexes and triggers (ADR-0234 Decision 3).
const userObjectsQuery = "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'"

// Backups copies and loads databases through their engines for workload backups (ADR-0234): Source is
// workload.Sources.SQL, and Backups implements the backup service's SQLLoader. Neither holds a whole database in
// memory.
type Backups struct {
	Engines *Engines
	Master  []byte
	Client  *http.Client // nil ⇒ a node client with no timeout, bounded by each call's context; share one
}

func (b Backups) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return httpx.NodeClient(0)
}

func (b Backups) engine(ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	addr, ok := b.Engines.Address(ns, name)
	if !ok {
		return "", fault.Wrapf(workload.ErrEngineNotReady, fault.Unavailable, "sql.backup", "the engine of SQLDatabase %s/%s is not Ready", ns, name)
	}
	return addr, nil
}

func (b Backups) request(ctx context.Context, method, addr, path string, query url.Values, body io.Reader, ns v1.NamespaceName, name v1.ObjectName) (*http.Response, error) {
	const op = "sql.backup"
	u := url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fault.Internalf(op, "build the engine request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.SetBasicAuth(engineargs.User, EnginePassword(b.Master, ns, name))
	// The transport error names the engine's address, so it is not wrapped (ADR-0233 Decision 4).
	resp, err := b.client().Do(req)
	if err != nil {
		return nil, fault.Unavailablef(op, "the engine of SQLDatabase %s/%s does not answer", ns, name)
	}
	if resp.StatusCode != http.StatusOK {
		httpx.CloseBody(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fault.Internalf(op, "the engine of SQLDatabase %s/%s refused funcd's credential", ns, name)
		}
		return nil, fault.Unavailablef(op, "the engine of SQLDatabase %s/%s answered %d", ns, name, resp.StatusCode)
	}
	return resp, nil
}

// Source streams a database's SQLite file in DELETE mode, one self-contained file (ADR-0234 Decision 2). Its reader
// fails, instead of reaching io.EOF, when the engine sets the stream-error trailer.
func (b Backups) Source(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (io.ReadCloser, error) {
	addr, err := b.engine(ns, name)
	if err != nil {
		return nil, err
	}
	resp, err := b.request(ctx, http.MethodGet, addr, "/db/backup", url.Values{"fmt": {"delete"}}, nil, ns, name)
	if err != nil {
		return nil, err
	}
	return &stream{resp: resp, db: string(ns) + "/" + string(name)}, nil
}

// stream is a backup's body that checks the engine's trailer at its end.
type stream struct {
	resp *http.Response
	db   string
}

// Read fails on a broken stream without the transport's text, which names the engine's address (ADR-0233 Decision 4).
func (s *stream) Read(p []byte) (int, error) {
	n, err := s.resp.Body.Read(p)
	switch {
	case err == io.EOF:
		if msg := s.resp.Trailer.Get(streamErrorTrailer); msg != "" {
			return n, fault.Unavailablef("sql.backup", "the backup stream of SQLDatabase %s broke: %s", s.db, msg)
		}
	case err != nil:
		return n, fault.Unavailablef("sql.backup", "the backup stream of SQLDatabase %s broke off", s.db)
	}
	return n, err
}

func (s *stream) Close() error { return s.resp.Body.Close() }

// Load replaces all of a database's data with the SQLite file read from file: Empty, then the file streamed to
// rqlite's /boot. /db/load was killed for its memory near 100 MB under the default 256Mi bound, where /boot stays near
// 12 MiB (rqlite 10.5.3, checked 2026-10-12), so this is ADR-0234 Decision 3's switch.
func (b Backups) Load(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, file io.Reader) error {
	if err := b.Empty(ctx, ns, name); err != nil {
		return err
	}
	addr, err := b.engine(ns, name)
	if err != nil {
		return err
	}
	src := &sourceReader{r: file}
	resp, err := b.request(ctx, http.MethodPost, addr, "/boot", url.Values{}, src, ns, name)
	if src.err != nil {
		return src.err
	}
	if err != nil {
		return err
	}
	httpx.CloseBody(resp.Body)
	return nil
}

// sourceReader keeps the error of the file a load streams, so a broken backup is not reported as an engine that does
// not answer.
type sourceReader struct {
	r   io.Reader
	err error
}

func (s *sourceReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

// loaded reads the answer of a load: rqlite reports a refused file in its results.
func (b Backups) loaded(resp *http.Response, ns v1.NamespaceName, name v1.ObjectName) error {
	defer httpx.CloseBody(resp.Body)
	var out struct {
		Results []engineResult `json:"results"`
		Error   string         `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEngineResponse)).Decode(&out); err != nil {
		return fault.Internalf("sql.backup", "decode the load answer of SQLDatabase %s/%s", ns, name)
	}
	if out.Error != "" {
		return fault.Invalidf("sql.backup", "SQLDatabase %s/%s refused the file: %s", ns, name, out.Error)
	}
	for _, r := range out.Results {
		if r.Error != "" {
			return fault.Invalidf("sql.backup", "SQLDatabase %s/%s refused the file: %s", ns, name, r.Error)
		}
	}
	return nil
}

// Empty leaves a database with no table, view, index or trigger, by loading an empty SQLite file through /db/load.
func (b Backups) Empty(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	addr, err := b.engine(ns, name)
	if err != nil {
		return err
	}
	resp, err := b.request(ctx, http.MethodPost, addr, "/db/load", url.Values{}, bytes.NewReader(emptyDatabase), ns, name)
	if err != nil {
		return err
	}
	return b.loaded(resp, ns, name)
}

// HasTables reports whether a database holds a table, view, index or trigger, the restore's emptiness rule.
func (b Backups) HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	const op = "sql.backup"
	addr, err := b.engine(ns, name)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stmt, _ := json.Marshal(userObjectsQuery)
	res, err := call(ctx, b.client(), addr, EnginePassword(b.Master, ns, name), "/db/query", url.Values{}, []json.RawMessage{stmt})
	if err != nil {
		return false, err
	}
	if len(res) != 1 || res[0].Error != "" || len(res[0].Values) != 1 || len(res[0].Values[0]) != 1 {
		return false, fault.Internalf(op, "unexpected answer counting the objects of %s/%s", ns, name)
	}
	var n int64
	if err := json.Unmarshal(res[0].Values[0][0], &n); err != nil {
		return false, fault.Internalf(op, "decode the object count of %s/%s", ns, name)
	}
	return n > 0, nil
}
