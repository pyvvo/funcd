package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// NodeOption picks the worker node a call is about; without one, it is about the funcd that serves the API.
type NodeOption func(url.Values)

// OnNode makes the call about the worker node named node (FEAT-0002 F250): no such WorkerNode ⇒ fault.NotFound, an
// offline one ⇒ fault.Unavailable.
func OnNode(node v1.ObjectName) NodeOption {
	return func(q url.Values) { q.Set("node", string(node)) }
}

// nodeQuery is the query opts make, with its question mark, or "".
func nodeQuery(opts []NodeOption) string {
	q := url.Values{}
	for _, o := range opts {
		o(q)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// HoldStatus reads the platform hold's evidence (ADR-0206 Decision 8); admin-only.
func (c *Client) HoldStatus(ctx context.Context, opts ...NodeOption) (hold.Evidence, error) {
	body, err := c.do(ctx, http.MethodGet, c.baseURL+apiPrefix+"/hold"+nodeQuery(opts), nil)
	if err != nil {
		return hold.Evidence{}, err
	}
	var st hold.Evidence
	if err := json.Unmarshal(body, &st); err != nil {
		return hold.Evidence{}, fault.Internalf("sdk.HoldStatus", "decode hold status: %v", err)
	}
	return st, nil
}

// ReleaseHold lifts the platform hold (ADR-0206 Decision 8). advance names blob EventSources, <ns>/<name>, whose
// listed keys are marked seen instead of replayed. Not held ⇒ fault.Conflict; an advance naming no blob event of an
// existing EventSource ⇒ fault.Invalid, both before any change.
func (c *Client) ReleaseHold(ctx context.Context, advance []string, opts ...NodeOption) error {
	body, err := json.Marshal(struct {
		Advance []string `json:"advance,omitempty"`
	}{advance})
	if err != nil {
		return fault.Internalf("sdk.ReleaseHold", "encode the release: %v", err)
	}
	_, err = c.do(ctx, http.MethodPost, c.baseURL+apiPrefix+"/hold/release"+nodeQuery(opts), body)
	return err
}

// ReturnQuarantined returns an object's quarantined data into it (ADR-0288 Decision 6); admin-only. The object must
// exist and its data be empty (fault.Conflict otherwise); no such entry ⇒ fault.NotFound.
func (c *Client) ReturnQuarantined(ctx context.Context, r hold.QuarantineReturn) error {
	body, err := json.Marshal(r)
	if err != nil {
		return fault.Internalf("sdk.ReturnQuarantined", "encode the return: %v", err)
	}
	_, err = c.do(ctx, http.MethodPost, c.baseURL+apiPrefix+"/quarantine/return", body)
	return err
}

// ReleaseNamespaceHold removes the hold a restore left on each namespace (ADR-0222 Decision 8): restore run's data
// marker and an ended in-place Restore's marker. POST …/hold/release with namespaces, which needs update on each
// Namespace; an in-place Restore still Pending or Running ⇒ fault.Conflict naming it.
func (c *Client) ReleaseNamespaceHold(ctx context.Context, namespaces []string) error {
	body, err := json.Marshal(struct {
		Namespaces []string `json:"namespaces"`
	}{namespaces})
	if err != nil {
		return fault.Internalf("sdk.ReleaseNamespaceHold", "encode the release: %v", err)
	}
	_, err = c.do(ctx, http.MethodPost, c.baseURL+apiPrefix+"/hold/release", body)
	return err
}
