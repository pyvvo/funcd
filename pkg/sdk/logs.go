package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
)

// LogsOptions are the optional filters for Client.Logs / Client.RunLogs (ADR-0084/0106).
type LogsOptions struct {
	Since    string // logread.SinceForms: a duration ("15m") or a timestamp ("2026-10-07T22:00:00.000Z"); "" ⇒ no lower bound
	Severity string // minimum level (trace|debug|info|warn|error|fatal); "" ⇒ all
	Limit    int    // max records (most-recent); <= 0 ⇒ the server default
	Step     string // RunLogs only (ADR-0106): narrow to one step's function (the --step drill-down); "" ⇒ all
}

// Logs reads a function's logs from the control-plane logs route (ADR-0084). The result is tenant-scoped
// to the caller's authenticated identity by the server (admin sees any namespace, developer/viewer only a
// bound one) — the namespace is the authorized path segment, never a client-asserted filter.
func (c *Client) Logs(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, o LogsOptions) ([]logread.Line, error) {
	const op = "sdk.Logs"
	if ns == "" || fn == "" {
		return nil, fault.Invalidf(op, "namespace and function are required")
	}
	u, err := c.itemURL(v1.KindFunction, ns, fn)
	if err != nil {
		return nil, err
	}
	u += "/logs"
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

// RunLogs reads a whole workflow run's logs from the run-scoped control-plane route (ADR-0106): the server
// resolves the run's status.traceId and returns the namespace-wide, trace-filtered lines — every step
// function's output plus any sub-workflow child steps (one composition = one trace). Tenant-scoped by the
// caller's identity (get/WorkflowRun in the namespace). o.Step narrows to one step's function.
func (c *Client) RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, o LogsOptions) ([]logread.Line, error) {
	const op = "sdk.RunLogs"
	if ns == "" || run == "" {
		return nil, fault.Invalidf(op, "namespace and run are required")
	}
	u, err := c.itemURL(v1.KindWorkflowRun, ns, run)
	if err != nil {
		return nil, err
	}
	u += "/logs"
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
	if o.Step != "" {
		q.Set("step", o.Step)
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
		return nil, fault.Internalf(op, "decode run logs response: %v", err)
	}
	return out.Items, nil
}
