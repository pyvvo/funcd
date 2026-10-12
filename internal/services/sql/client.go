package sql

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/services/sql/engineargs"
)

// maxEngineResponse bounds what funcd reads from an engine for one call.
const maxEngineResponse = 8 << 20

// engineResult is one statement's result in rqlite's array format.
type engineResult struct {
	Columns      []string            `json:"columns"`
	Values       [][]json.RawMessage `json:"values"`
	RowsAffected int64               `json:"rows_affected"`
	LastInsertID int64               `json:"last_insert_id"`
	Error        string              `json:"error"`
}

// call POSTs statements to an engine endpoint (/db/query or /db/execute) as user funcd and decodes rqlite's results.
// A refused connection or a timeout is Unavailable, a 401 Internal (the password never appears in the error).
func call(ctx context.Context, client *http.Client, addr, password, path string, query url.Values, stmts []json.RawMessage) ([]engineResult, error) {
	const op = "sql.engine"
	body, err := json.Marshal(stmts)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "encode statements")
	}
	u := url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "build the engine request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(engineargs.User, password)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "the database's engine does not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEngineResponse+1))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "read the engine's answer")
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fault.Internalf(op, "the engine refused funcd's credential")
	case resp.StatusCode != http.StatusOK:
		return nil, fault.Unavailablef(op, "the engine answered %d", resp.StatusCode)
	case len(raw) > maxEngineResponse:
		return nil, fault.PayloadTooLargef(op, "the engine's answer is over %d bytes", maxEngineResponse)
	}
	var out struct {
		Results []engineResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "decode the engine's answer")
	}
	return out.Results, nil
}
