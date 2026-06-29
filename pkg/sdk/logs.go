package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/funclog/logread"
)

// LogsOptions are the optional filters for Client.Logs (ADR-0084).
type LogsOptions struct {
	Since    string // RFC3339 time or a Go duration ("15m"); "" ⇒ no lower bound
	Severity string // minimum level (trace|debug|info|warn|error|fatal); "" ⇒ all
	Limit    int    // max records (most-recent); <= 0 ⇒ the server default
}

// Logs reads a function's logs from the control-plane logs route (ADR-0084). The result is tenant-scoped
// to the caller's authenticated identity by the server (admin sees any namespace, developer/viewer only a
// bound one) — the namespace is the authorized path segment, never a client-asserted filter.
func (c *Client) Logs(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, o LogsOptions) ([]logread.Line, error) {
	const op = "sdk.Logs"
	if ns == "" || fn == "" {
		return nil, fault.Invalidf(op, "namespace and function are required")
	}
	u := c.baseURL + apiPrefix + "/namespaces/" + string(ns) + "/functions/" + string(fn) + "/logs"
	q := url.Values{}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	if o.Severity != "" {
		q.Set("severity", o.Severity)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []logread.Line `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fault.Internalf(op, "decode logs response: %v", err)
	}
	return out.Items, nil
}
