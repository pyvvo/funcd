package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
)

// DeadLetters lists a namespace's parked dead letters, newest first (ADR-0118). Tenant-scoped by the
// caller's identity (get/Sensor in the namespace) — the namespace is the authorized path segment.
func (c *Client) DeadLetters(ctx context.Context, ns v1.NamespaceName) ([]deadletter.DeadLetter, error) {
	const op = "sdk.DeadLetters"
	if ns == "" {
		return nil, fault.Invalidf(op, "namespace is required")
	}
	u, err := c.namespaceURL(ns)
	if err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodGet, u+"/deadletters", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []deadletter.DeadLetter `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fault.Internalf(op, "decode dead letters response: %v", err)
	}
	return out.Items, nil
}

// DeadLetter fetches one parked dead letter by id (ADR-0118), or fault.NotFound if absent.
func (c *Client) DeadLetter(ctx context.Context, ns v1.NamespaceName, id string) (deadletter.DeadLetter, error) {
	const op = "sdk.DeadLetter"
	if ns == "" || id == "" {
		return deadletter.DeadLetter{}, fault.Invalidf(op, "namespace and id are required")
	}
	u, err := c.deadLetterURL(ns, id)
	if err != nil {
		return deadletter.DeadLetter{}, err
	}
	body, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return deadletter.DeadLetter{}, err
	}
	var dl deadletter.DeadLetter
	if err := json.Unmarshal(body, &dl); err != nil {
		return deadletter.DeadLetter{}, fault.Internalf(op, "decode dead letter response: %v", err)
	}
	return dl, nil
}

// ReplayDeadLetter re-injects a parked dead letter through the LIVE Sensor action path (ADR-0118 §3): ONE
// synchronous attempt. Success removes the entry (nil); failure re-parks it (Attempts reset) and returns the
// delivery error; a gone Sensor/action ⇒ fault.NotFound.
func (c *Client) ReplayDeadLetter(ctx context.Context, ns v1.NamespaceName, id string) error {
	const op = "sdk.ReplayDeadLetter"
	if ns == "" || id == "" {
		return fault.Invalidf(op, "namespace and id are required")
	}
	u, err := c.deadLetterURL(ns, id)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, u+"/replay", nil)
	return err
}

// DiscardDeadLetter deletes a parked dead letter (ADR-0118) — a plain CRUD delete; absent is not an error
// server-side.
func (c *Client) DiscardDeadLetter(ctx context.Context, ns v1.NamespaceName, id string) error {
	const op = "sdk.DiscardDeadLetter"
	if ns == "" || id == "" {
		return fault.Invalidf(op, "namespace and id are required")
	}
	u, err := c.deadLetterURL(ns, id)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, u, nil)
	return err
}

// deadLetterURL builds the path of a dead letter. The id is opaque, not a DNS label, so it is escaped to stay one
// path segment (issue #698).
func (c *Client) deadLetterURL(ns v1.NamespaceName, id string) (string, error) {
	u, err := c.namespaceURL(ns)
	if err != nil {
		return "", err
	}
	return u + "/deadletters/" + url.PathEscape(id), nil
}
